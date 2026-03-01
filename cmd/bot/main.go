package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/LinaKACI-pro/make-me-disappear/internal/config"
	"github.com/LinaKACI-pro/make-me-disappear/internal/dashboard"
	"github.com/LinaKACI-pro/make-me-disappear/internal/db"
	"github.com/LinaKACI-pro/make-me-disappear/internal/email"
	imapcheck "github.com/LinaKACI-pro/make-me-disappear/internal/imap"
	"github.com/LinaKACI-pro/make-me-disappear/internal/notify"

	"github.com/spf13/cobra"
)

var cfgPath string

func main() {
	rootCmd := &cobra.Command{
		Use:   "bot",
		Short: "GDPR personal data removal bot",
	}
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", "config.yaml", "path to config file")

	rootCmd.AddCommand(
		newDBSeedCmd(),
		newCampaignInitCmd(),
		newRunCmd(),
		newStatusCmd(),
		newSendCmd(),
		newManualListCmd(),
		newInboxReviewCmd(),
		newDashboardCmd(),
		newIMAPCheckCmd(),
		newPurgeBouncedCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newDBSeedCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "db-seed",
		Aliases: []string{"db:seed"},
		Short:   "Seed the database with broker data",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			n, err := db.SeedBrokers(d, "brokers.yaml")
			if err != nil {
				return err
			}
			fmt.Printf("Seeded %d brokers\n", n)
			return nil
		},
	}
}

func newCampaignInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "campaign-init",
		Aliases: []string{"campaign:init"},
		Short:   "Create initial requests for all active brokers",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			n, err := db.CreatePendingRequests(d)
			if err != nil {
				return err
			}
			fmt.Printf("Created %d pending requests\n", n)
			return nil
		},
	}
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the scheduler daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			d, err := db.Open("bot.db")
			if err != nil {
				return fmt.Errorf("database: %w", err)
			}
			defer d.Close()

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			return runScheduler(ctx, cfg, d)
		},
	}
}

func runScheduler(ctx context.Context, cfg *config.Config, d *sql.DB) error {
	slog.Info("scheduler started")

	cycleTicker  := time.NewTicker(24 * time.Hour)
	processTicker := time.NewTicker(10 * time.Minute)
	imapTicker   := time.NewTicker(30 * time.Minute)
	notifyTicker := time.NewTicker(24 * time.Hour)
	defer cycleTicker.Stop()
	defer processTicker.Stop()
	defer imapTicker.Stop()
	defer notifyTicker.Stop()

	var processMu, imapMu sync.Mutex
	var wg sync.WaitGroup

	// Run once immediately on start.
	checkAndStartCycle(d)
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered", "task", "processRequests", "err", r)
			}
		}()
		processMu.Lock()
		defer processMu.Unlock()
		processRequests(ctx, cfg, d)
	})
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered", "task", "checkIMAP", "err", r)
			}
		}()
		imapMu.Lock()
		defer imapMu.Unlock()
		checkIMAP(cfg, d)
	})
	sendNotification(cfg, d)

	for {
		select {
		case <-ctx.Done():
			slog.Info("scheduler shutting down")
			wg.Wait()
			return nil
		case <-cycleTicker.C:
			checkAndStartCycle(d)
		case <-processTicker.C:
			wg.Go(func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic recovered", "task", "processRequests", "err", r)
					}
				}()
				if !processMu.TryLock() {
					slog.Info("processRequests already running, skipping")
					return
				}
				defer processMu.Unlock()
				processRequests(ctx, cfg, d)
			})
		case <-imapTicker.C:
			wg.Go(func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic recovered", "task", "checkIMAP", "err", r)
					}
				}()
				if !imapMu.TryLock() {
					slog.Info("checkIMAP already running, skipping")
					return
				}
				defer imapMu.Unlock()
				checkIMAP(cfg, d)
			})
		case <-notifyTicker.C:
			sendNotification(cfg, d)
		}
	}
}

func checkAndStartCycle(d *sql.DB) {
	shouldStart, err := db.ShouldStartNewCycle(d)
	if err != nil {
		slog.Error("cycle check failed", "err", err)
		return
	}
	if shouldStart {
		n, err := db.CreatePendingRequests(d)
		if err != nil {
			slog.Error("auto cycle creation failed", "err", err)
		} else if n > 0 {
			slog.Info("new cycle started", "requests", n)
		}
	}
}

func processRequests(ctx context.Context, cfg *config.Config, d *sql.DB) {
	// Process pending requests.
	reqs, err := db.PendingRequests(d)
	if err != nil {
		slog.Error("fetch pending", "err", err)
		return
	}

	for _, r := range reqs {
		switch r.BrokerMethod {
		case "email":
			if r.Contact == "" {
				slog.Error("no contact email", "broker", r.BrokerID)
				db.MarkError(d, r.ID, "no contact email")
				continue
			}
			err = sendEmail(cfg, r.BrokerRegion, r.OptOutURL, r.Contact)
			if err != nil {
				slog.Error("send failed", "broker", r.BrokerName, "err", err)
				db.MarkError(d, r.ID, err.Error())
			}

			slog.Info("sent", "broker", r.BrokerName, "contact", r.Contact)
			db.MarkSent(d, r.ID)
		case "form":
			slog.Warn("form not implemented, skipping", "broker", r.BrokerName)
		case "manual":
			slog.Info("manual action required", "broker", r.BrokerName)
			db.MarkManualRequired(d, r.ID)
		default:
			slog.Error("unknown method", "method", r.BrokerMethod, "broker", r.BrokerName)
			db.MarkError(d, r.ID, "unknown method: "+r.BrokerMethod)
		}

		// Random delay between sends (2-10s), interruptible on shutdown.
		delay := time.Duration(2+rand.Intn(9)) * time.Second
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}

	// Process retries: sent requests whose next_retry has passed.
	retries, err := db.DueRetries(d)
	if err != nil {
		slog.Error("fetch retries", "err", err)
		return
	}
	for _, r := range retries {
		slog.Info("retrying", "broker", r.BrokerName, "attempt", r.Attempt+1)
		db.MarkRetry(d, r.ID, r.Attempt)
	}
}

func checkIMAP(cfg *config.Config, d *sql.DB) {
	if cfg.IMAP.Host == "" {
		return
	}
	slog.Info("checking inbox")
	if err := imapcheck.Check(cfg.IMAP, d); err != nil {
		slog.Error("imap check failed", "err", err)
	}
}

func sendNotification(cfg *config.Config, d *sql.DB) {
	if cfg.SMTP.Host == "" {
		return
	}
	if err := notify.SendDigest(cfg.SMTP, cfg.Identity.Email, d); err != nil {
		slog.Error("notification failed", "err", err)
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show global status of requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			counts, err := db.StatusSummary(d)
			if err != nil {
				return err
			}
			if len(counts) == 0 {
				fmt.Println("No requests yet. Run 'bot campaign-init' first.")
				return nil
			}

			total := 0
			fmt.Printf("%-20s %s\n", "Status", "Count")
			fmt.Println(strings.Repeat("─", 30))
			for _, sc := range counts {
				fmt.Printf("%-20s %d\n", sc.Status, sc.Count)
				total += sc.Count
			}
			fmt.Println(strings.Repeat("─", 30))
			fmt.Printf("%-20s %d\n", "total", total)
			return nil
		},
	}
}

func newSendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a request to a specific broker",
		RunE: func(cmd *cobra.Command, args []string) error {
			brokerID, _ := cmd.Flags().GetString("broker")
			if brokerID == "" {
				return fmt.Errorf("--broker flag is required")
			}

			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			broker, err := db.BrokerByID(d, brokerID)
			if err != nil {
				return fmt.Errorf("broker %q not found: %w", brokerID, err)
			}
			if broker.Method != "email" {
				return fmt.Errorf("broker %q uses method %q, not email", brokerID, broker.Method)
			}
			if broker.Contact == "" {
				return fmt.Errorf("broker %q has no contact email", brokerID)
			}

			reqID, err := db.FindOrCreateRequest(d, brokerID)
			if err != nil {
				return err
			}

			if err := sendEmail(cfg, broker.Region, broker.OptOutURL, broker.Contact); err != nil {
				db.MarkError(d, reqID, err.Error())
				return fmt.Errorf("send failed: %w", err)
			}

			db.MarkSent(d, reqID)
			fmt.Printf("Sent deletion request to %s (%s)\n", broker.Name, broker.Contact)
			return nil
		},
	}
	cmd.Flags().String("broker", "", "broker ID to send to")
	return cmd
}

func newManualListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "manual-list",
		Aliases: []string{"manual:list"},
		Short:   "List brokers requiring manual action",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			reqs, err := db.ManualRequests(d)
			if err != nil {
				return err
			}
			if len(reqs) == 0 {
				fmt.Println("No brokers requiring manual action.")
				return nil
			}

			fmt.Printf("%-20s %-8s %-10s %s\n", "Broker", "Region", "Method", "Contact")
			fmt.Println(strings.Repeat("─", 60))
			for _, r := range reqs {
				fmt.Printf("%-20s %-8s %-10s %s\n", r.BrokerName, r.BrokerRegion, r.BrokerMethod, r.Contact)
			}
			return nil
		},
	}
}

func newInboxReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "inbox-review",
		Aliases: []string{"inbox:review"},
		Short:   "Review unclassified incoming emails",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			reqs, err := db.NeedsReviewRequests(d)
			if err != nil {
				return err
			}
			if len(reqs) == 0 {
				fmt.Println("No emails to review.")
				return nil
			}

			scanner := bufio.NewScanner(os.Stdin)
			for _, r := range reqs {
				fmt.Printf("\n══════ Request #%d — %s (%s) ══════\n", r.ID, r.BrokerName, r.BrokerRegion)
				if r.ResponseRaw != "" {
					preview := stripEmailHeaders(r.ResponseRaw)
					if len(preview) > 500 {
						preview = preview[:500] + "\n[... truncated]"
					}
					fmt.Println(preview)
				}
				fmt.Println("──────")
				fmt.Print("Status? [c]onfirmed / [r]ejected / [m]anual / [s]kip: ")

				if !scanner.Scan() {
					return nil
				}
				input := strings.TrimSpace(strings.ToLower(scanner.Text()))

				var status string
				switch input {
				case "c", "confirmed":
					status = "confirmed"
				case "r", "rejected":
					status = "rejected"
				case "m", "manual":
					status = "manual_required"
				case "s", "skip", "":
					continue
				default:
					fmt.Println("Unknown input, skipping.")
					continue
				}

				if err := db.UpdateStatus(d, r.ID, status); err != nil {
					slog.Error("update request", "id", r.ID, "err", err)
				} else {
					fmt.Printf("-> Marked as %s\n", status)
				}
			}
			return nil
		},
	}
}

func newDashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Generate and open HTML dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			outPath := "/tmp/bot-dashboard.html"
			if err := dashboard.Generate(d, outPath); err != nil {
				return err
			}
			fmt.Printf("Dashboard written to %s\n", outPath)
			exec.Command("xdg-open", outPath).Start()
			return nil
		},
	}
}

func newIMAPCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "imap-check",
		Short: "Check inbox once and process bounces/replies",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()
			checkIMAP(cfg, d)
			return nil
		},
	}
}

func newPurgeBouncedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "purge-bounced",
		Short: "Remove brokers with address-not-found bounces from DB and brokers.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open("bot.db")
			if err != nil {
				return err
			}
			defer d.Close()

			brokers, err := db.BouncedBrokers(d)
			if err != nil {
				return fmt.Errorf("fetch bounced brokers: %w", err)
			}
			if len(brokers) == 0 {
				fmt.Println("No bounced brokers found.")
				return nil
			}

			fmt.Printf("Found %d bounced broker(s):\n", len(brokers))
			for _, b := range brokers {
				fmt.Printf("  - %s\n", b.Name)
			}

			fmt.Print("\nPurge these from DB and brokers.yaml? [y/N]: ")
			var answer string
			fmt.Scan(&answer)
			if strings.ToLower(answer) != "y" {
				fmt.Println("Aborted.")
				return nil
			}

			ids := make([]string, len(brokers))
			for i, b := range brokers {
				ids[i] = b.ID
			}

			removed, err := db.PurgeBrokersFromYAML("brokers.yaml", ids)
			if err != nil {
				return fmt.Errorf("yaml: %w", err)
			}
			if err := db.PurgeBrokers(d, ids); err != nil {
				return fmt.Errorf("db: %w", err)
			}

			fmt.Printf("Done: purged %d broker(s) from DB and %d from brokers.yaml\n", len(ids), removed)
			return nil
		},
	}
}

// stripEmailHeaders removes raw email headers, returning only the body.
// If the input has no header block (e.g. already cleaned), it is returned as-is.
func stripEmailHeaders(raw string) string {
	for _, sep := range []string{"\r\n\r\n", "\n\n"} {
		if i := strings.Index(raw, sep); i != -1 {
			return strings.TrimSpace(raw[i+len(sep):])
		}
	}
	return raw
}

func sendEmail(cfg *config.Config, region, optOutURL, contact string) error {
	subject, body, err := email.Render("templates", region, optOutURL, cfg.Identity)
	if err != nil {
		return err
	}
	return email.Send(cfg.SMTP, cfg.SMTP.User, contact, subject, body)
}
