package imap

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"regexp"
	"strings"
	"time"

	"github.com/LinaKACI-pro/make-me-disappear/internal/config"
	"github.com/LinaKACI-pro/make-me-disappear/internal/db"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
)

var reFinalRecipient = regexp.MustCompile(`(?i)Final-Recipient:\s*rfc822;\s*([a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,})`)

var folders = []string{"INBOX", "[Gmail]/Spam"}

func Check(ctx context.Context, cfg config.IMAP, d *sql.DB) error {
	c, err := imapclient.DialTLS(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), nil)
	if err != nil {
		return fmt.Errorf("imap dial: %w", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Login(cfg.User, cfg.Password).Wait(); err != nil {
		return fmt.Errorf("imap login: %w", err)
	}
	defer func() { _ = c.Logout().Wait() }()

	criteria := &imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}
	if cfg.Since != "" {
		t, err := time.Parse("2006-01-02", cfg.Since)
		if err != nil {
			slog.Warn("imap: invalid since date, ignoring", "since", cfg.Since)
		} else {
			criteria.Since = t
		}
	}

	var total int
	for _, folder := range folders {
		if ctx.Err() != nil {
			break
		}
		if _, err := c.Select(folder, nil).Wait(); err != nil {
			slog.Warn("imap: skipping folder", "folder", folder)
			continue
		}
		n, err := processFolder(ctx, c, d, criteria)
		if err != nil {
			return fmt.Errorf("imap folder %s: %w", folder, err)
		}
		total += n
	}
	slog.Info("imap: done", "processed", total)
	return nil
}

func matchByName(brokerNames []db.BrokerNameDomain, senderName string) (string, bool) {
	if senderName == "" {
		return "", false
	}
	for _, b := range brokerNames {
		if strings.Contains(b.Name, senderName) || strings.Contains(senderName, b.Name) {
			return b.ID, true
		}
	}
	return "", false
}

func matchByDomain(brokerNames []db.BrokerNameDomain, senderDomain string) (string, bool) {
	for _, b := range brokerNames {
		if b.Domain == senderDomain {
			return b.ID, true
		}
	}
	return "", false
}

type imapUpdate struct {
	uid    imap.UID
	reqID  int
	status string
	raw    string
}

func processFolder(ctx context.Context, c *imapclient.Client, d *sql.DB, criteria *imap.SearchCriteria) (int, error) {
	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("imap search: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return 0, nil
	}
	slog.Info("imap: messages found", "count", len(uids))

	// Fetch all at once, then process — avoids STORE during FETCH.
	type msg struct {
		uid          imap.UID
		body         string
		senderLocal  string
		senderDomain string
		senderName   string
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
			entry.senderName = strings.ToLower(buf.Envelope.From[0].Name)
		}
		msgs = append(msgs, entry)
	}
	_ = fetchCmd.Close()

	brokerNames, err := db.ListBrokersNameDomain(d)
	if err != nil {
		return 0, fmt.Errorf("db.ListBrokersNameDomain: %w", err)
	}

	// Phase 2: classify messages and collect DB updates (no DB writes yet).
	var updates []imapUpdate
	var bounceUpdates []imapUpdate

	for _, m := range msgs {
		if ctx.Err() != nil {
			break
		}
		if strings.Contains(m.senderLocal, "mailer-daemon") {
			if bu, ok := classifyBounce(d, m.body, m.uid); ok {
				bounceUpdates = append(bounceUpdates, bu)
			}
			continue
		}
		if strings.Contains(m.senderLocal, "postmaster") {
			if isBounce(m.body) {
				if bu, ok := classifyBounce(d, m.body, m.uid); ok {
					bounceUpdates = append(bounceUpdates, bu)
				}
				continue
			}
			slog.Info("imap: postmaster reply, treating as broker response", "from", m.senderDomain)
		}

		brokerID, ok := matchByDomain(brokerNames, m.senderDomain)
		if !ok {
			brokerID, ok = matchByName(brokerNames, m.senderDomain)
		}
		if !ok {
			brokerID, ok = matchByName(brokerNames, m.senderName)
		}
		if !ok {
			slog.Warn("imap: unmatched", "from", m.senderDomain, "name", m.senderName, "subject", m.subject)
			continue
		}

		reqID, err := db.RequestForBroker(d, brokerID)
		if err != nil {
			slog.Error("imap: db error looking up request", "broker", brokerID, "err", err)
			continue
		}

		decoded := decodeMIMEText(m.body)
		preview := "Subject: " + m.subject + "\n\n" + decoded
		cl := classifyReply(decoded)
		status := "needs_review"
		switch cl.kind {
		case replyNoData:
			status = "no_data"
			slog.Info("imap: auto no_data", "from", m.senderDomain, "request", reqID, "score", cl.score)
		case replyConfirmed:
			status = "confirmed"
			slog.Info("imap: auto-confirmed (deletion)", "from", m.senderDomain, "request", reqID, "score", cl.score)
		default:
			slog.Info("imap: reply needs review", "from", m.senderDomain, "request", reqID)
		}
		updates = append(updates, imapUpdate{uid: m.uid, reqID: reqID, status: status, raw: preview})
	}

	if len(updates) == 0 && len(bounceUpdates) == 0 {
		return 0, nil
	}

	// Phase 3: apply all DB writes in a single transaction.
	tx, err := d.Begin()
	if err != nil {
		return 0, fmt.Errorf("imap begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, u := range bounceUpdates {
		if err := db.MarkError(tx, u.reqID, u.raw); err != nil {
			return 0, fmt.Errorf("imap mark error: %w", err)
		}
	}
	for _, u := range updates {
		switch u.status {
		case "no_data":
			err = db.MarkNoData(tx, u.reqID, u.raw)
		case "confirmed":
			err = db.MarkConfirmed(tx, u.reqID, u.raw)
		default:
			err = db.MarkNeedsReview(tx, u.reqID, u.raw)
		}
		if err != nil {
			return 0, fmt.Errorf("imap mark %s: %w", u.status, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("imap commit: %w", err)
	}

	// Phase 4: mark all handled messages as seen in one IMAP batch.
	var toMarkSeen []imap.UID
	for _, u := range bounceUpdates {
		toMarkSeen = append(toMarkSeen, u.uid)
	}
	for _, u := range updates {
		toMarkSeen = append(toMarkSeen, u.uid)
	}
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

	return len(updates) + len(bounceUpdates), nil
}

// isBounce returns true if the raw email body contains DSN delivery-failure markers.
func isBounce(body string) bool {
	return strings.Contains(body, "Final-Recipient:") ||
		strings.Contains(body, "Action: failed") ||
		strings.Contains(body, "Status: 5.")
}

// classifyBounce extracts the bounce address, looks up the request, and returns
// an update to apply later. The DB write is deferred to the transaction phase.
func classifyBounce(d *sql.DB, body string, uid imap.UID) (imapUpdate, bool) {
	addr := extractBounceAddr(body)
	if addr == "" {
		slog.Warn("imap: bounce address not found")
		return imapUpdate{}, false
	}
	reqID, err := db.RequestByContact(d, addr)
	if err == sql.ErrNoRows {
		slog.Warn("imap: bounce not in DB", "contact", addr)
		return imapUpdate{}, false
	}
	if err != nil {
		slog.Error("imap: db error looking up bounce", "contact", addr, "err", err)
		return imapUpdate{}, false
	}
	slog.Info("imap: bounce", "contact", addr, "request", reqID)
	return imapUpdate{uid: uid, reqID: reqID, raw: "bounce: address not found"}, true
}

func ExtractBody(raw string) string {
	// Headers and body are separated by a blank line.
	for _, sep := range []string{"\r\n\r\n", "\n\n"} {
		if i := strings.Index(raw, sep); i != -1 {
			return strings.TrimSpace(raw[i+len(sep):])
		}
	}
	return raw
}

func extractBounceAddr(body string) string {
	if m := reFinalRecipient.FindStringSubmatch(body); len(m) > 1 {
		return strings.ToLower(strings.TrimSpace(m[1]))
	}
	return ""
}

// decodeMIMEText parses a raw MIME message and returns the decoded plain text.
// This handles base64/quoted-printable encoded parts that would otherwise
// be invisible to regex matching.
func decodeMIMEText(raw string) string {
	e, err := message.Read(strings.NewReader(raw))
	if e == nil {
		// Parsing failed entirely, fall back to stripping headers manually.
		return ExtractBody(raw)
	}
	if err != nil {
		// Partial error (e.g. unknown charset) — entity is still usable.
		slog.Debug("imap: mime parse warning", "err", err)
	}
	var sb strings.Builder
	collectMIMEText(e, &sb)
	if sb.Len() == 0 {
		return ExtractBody(raw)
	}
	return sb.String()
}

func collectMIMEText(e *message.Entity, sb *strings.Builder) {
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			collectMIMEText(part, sb)
		}
		return
	}
	ct, _, _ := mime.ParseMediaType(e.Header.Get("Content-Type"))
	if ct == "" || strings.HasPrefix(strings.ToLower(ct), "text/") {
		b, _ := io.ReadAll(e.Body)
		sb.Write(b)
		sb.WriteByte('\n')
	}
}
