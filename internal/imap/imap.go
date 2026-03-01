package imap

import (
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/LinaKACI-pro/make-me-disappear/internal/config"
	"github.com/LinaKACI-pro/make-me-disappear/internal/db"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

var reFinalRecipient = regexp.MustCompile(`(?i)Final-Recipient:\s*rfc822;\s*([a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,})`)

var folders = []string{"INBOX", "[Gmail]/Spam"}

func Check(cfg config.IMAP, d *sql.DB) error {
	c, err := imapclient.DialTLS(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), nil)
	if err != nil {
		return fmt.Errorf("imap dial: %w", err)
	}
	defer c.Close()

	if err := c.Login(cfg.User, cfg.Password).Wait(); err != nil {
		return fmt.Errorf("imap login: %w", err)
	}
	defer func() { c.Logout().Wait() }()

	criteria := &imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}
	if cfg.Since != "" {
		if t, err := time.Parse("2006-01-02", cfg.Since); err != nil {
			slog.Warn("imap: invalid since date, ignoring", "since", cfg.Since)
		} else {
			criteria.Since = t
		}
	}

	brokers, err := db.AllBrokers(d)
	if err != nil {
		return fmt.Errorf("load brokers: %w", err)
	}
	domainMap := make(map[string]string)
	for _, b := range brokers {
		if domain := extractDomain(b.Contact); domain != "" {
			domainMap[domain] = b.ID
		}
	}

	var total int
	for _, folder := range folders {
		if _, err := c.Select(folder, nil).Wait(); err != nil {
			slog.Warn("imap: skipping folder", "folder", folder)
			continue
		}
		total += processFolder(c, d, criteria, domainMap)
	}
	slog.Info("imap: done", "processed", total)
	return nil
}

func processFolder(c *imapclient.Client, d *sql.DB, criteria *imap.SearchCriteria, domainMap map[string]string) int {
	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		slog.Error("imap: search failed", "err", err)
		return 0
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return 0
	}
	slog.Info("imap: messages found", "count", len(uids))

	// Fetch all at once, then process — avoids STORE during FETCH.
	type msg struct {
		uid          imap.UID
		body         string
		senderLocal  string
		senderDomain string
		subject      string
	}

	uidSet := imap.UIDSet{}
	for _, uid := range uids {
		uidSet.AddNum(uid)
	}
	bodySection := &imap.FetchItemBodySection{}
	fetchCmd := c.Fetch(uidSet, &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	})

	var msgs []msg
	for {
		m := fetchCmd.Next()
		if m == nil {
			break
		}
		buf, err := m.Collect()
		if err != nil {
			slog.Error("imap: collect failed", "err", err)
			continue
		}
		entry := msg{uid: buf.UID}
		if raw := buf.FindBodySection(bodySection); raw != nil {
			entry.body = string(raw)
		}
		if buf.Envelope != nil && len(buf.Envelope.From) > 0 {
			entry.subject = buf.Envelope.Subject
			entry.senderLocal = strings.ToLower(buf.Envelope.From[0].Mailbox)
			entry.senderDomain = strings.ToLower(buf.Envelope.From[0].Host)
		}
		msgs = append(msgs, entry)
	}
	fetchCmd.Close()

	var processed int
	var toMarkSeen []imap.UID

	for _, m := range msgs {
		if strings.Contains(m.senderLocal, "mailer-daemon") {
			addr := extractBounceAddr(m.body)
			if addr == "" {
				slog.Warn("imap: bounce address not found")
				toMarkSeen = append(toMarkSeen, m.uid)
				continue
			}
			reqID, err := db.LatestSentRequestByContact(d, addr)
			if err != nil {
				slog.Warn("imap: bounce not in DB", "contact", addr)
				toMarkSeen = append(toMarkSeen, m.uid)
				continue
			}
			if err := db.MarkError(d, reqID, "bounce: address not found"); err != nil {
				slog.Error("imap: mark error failed", "err", err)
			} else {
				slog.Info("imap: bounce", "contact", addr, "request", reqID)
				processed++
			}
			toMarkSeen = append(toMarkSeen, m.uid)
			continue
		}

		brokerID, ok := domainMap[m.senderDomain]
		if !ok {
			slog.Warn("imap: unmatched", "from", m.senderDomain, "subject", m.subject)
			continue
		}
		reqID, err := db.LatestSentRequest(d, brokerID)
		if err != nil {
			slog.Warn("imap: no sent request", "broker", brokerID)
			continue
		}
		if err := db.MarkNeedsReview(d, reqID, m.body); err != nil {
			slog.Error("imap: mark needs_review failed", "err", err)
			continue
		}
		slog.Info("imap: reply", "from", m.senderDomain, "request", reqID)
		processed++
		toMarkSeen = append(toMarkSeen, m.uid)
	}

	// Mark all handled messages as seen in one batch.
	if len(toMarkSeen) > 0 {
		uidSet := imap.UIDSet{}
		for _, uid := range toMarkSeen {
			uidSet.AddNum(uid)
		}
		storeCmd := c.Store(uidSet, &imap.StoreFlags{
			Op:    imap.StoreFlagsAdd,
			Flags: []imap.Flag{imap.FlagSeen},
		}, nil)
		if _, err := storeCmd.Collect(); err != nil {
			slog.Error("imap: mark seen failed", "err", err)
		}
	}

	return processed
}

func extractBounceAddr(body string) string {
	if m := reFinalRecipient.FindStringSubmatch(body); len(m) > 1 {
		return strings.ToLower(strings.TrimSpace(m[1]))
	}
	return ""
}

func extractDomain(contact string) string {
	parts := strings.SplitN(contact, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}
