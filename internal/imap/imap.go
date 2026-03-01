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
		t, err := time.Parse("2006-01-02", cfg.Since)
		if err != nil {
			slog.Warn("imap: invalid since date, ignoring", "since", cfg.Since)
		}

		criteria.Since = t
	}

	brokers, err := db.AllBrokers(d) // todo: trop, reduire !!!!
	if err != nil {
		return fmt.Errorf("load brokers: %w", err)
	}
	domainMap := make(map[string]string)
	var brokerNames []brokerName
	for _, b := range brokers {
		if domain := extractDomain(b.Contact); domain != "" {
			domainMap[domain] = b.ID
		}
		if b.Name != "" {
			brokerNames = append(brokerNames, brokerName{b.ID, strings.ToLower(b.Name)})
		}
	}

	var total int
	for _, folder := range folders {
		if _, err := c.Select(folder, nil).Wait(); err != nil {
			slog.Warn("imap: skipping folder", "folder", folder)
			continue
		}
		total += processFolder(c, d, criteria, domainMap, brokerNames)
	}
	slog.Info("imap: done", "processed", total)
	return nil
}

type brokerName struct{ id, name string }

func matchByName(brokerNames []brokerName, senderName string) (string, bool) {
	if senderName == "" {
		return "", false
	}
	for _, b := range brokerNames {
		if strings.Contains(b.name, senderName) || strings.Contains(senderName, b.name) {
			return b.id, true
		}
	}
	return "", false
}

func processFolder(c *imapclient.Client, d *sql.DB, criteria *imap.SearchCriteria, domainMap map[string]string, brokerNames []brokerName) int {
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
	fetchCmd.Close()

	var processed int
	var toMarkSeen []imap.UID

	for _, m := range msgs {
		if strings.Contains(m.senderLocal, "mailer-daemon") {
			processed += handleBounce(d, m.body, m.uid, &toMarkSeen)
			continue
		}
		if strings.Contains(m.senderLocal, "postmaster") {
			if isBounce(m.body) {
				processed += handleBounce(d, m.body, m.uid, &toMarkSeen)
				continue
			}
			// Not a bounce: postmaster is acting as a human reply, fall through to broker matching.
			slog.Info("imap: postmaster reply, treating as broker response", "from", m.senderDomain)
		}

		brokerID, ok := domainMap[m.senderDomain]
		if !ok {
			brokerID, ok = matchByName(brokerNames, m.senderName)
		}
		if !ok {
			brokerID, ok = matchByName(brokerNames, m.senderDomain)
		}
		if !ok {
			slog.Warn("imap: unmatched", "from", m.senderDomain, "name", m.senderName, "subject", m.subject)
			continue
		}
		reqID, err := db.LatestSentRequest(d, brokerID)
		if err != nil {
			slog.Warn("imap: no sent request", "broker", brokerID)
			continue
		}
		preview := "Subject: " + m.subject + "\n\n" + extractBody(m.body)
		if err := db.MarkNeedsReview(d, reqID, preview); err != nil {
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
		if _, err := storeCmd.Collect(); err != nil { // a check
			slog.Error("imap: mark seen failed", "err", err)
		}
	}

	return processed
}

// isBounce returns true if the raw email body contains DSN delivery-failure markers.
func isBounce(body string) bool {
	return strings.Contains(body, "Final-Recipient:") ||
		strings.Contains(body, "Action: failed") ||
		strings.Contains(body, "Status: 5.")
}

// handleBounce processes a bounce email, marks the matching request as error,
// and appends the UID to toMarkSeen. Returns 1 if successfully processed.
func handleBounce(d *sql.DB, body string, uid imap.UID, toMarkSeen *[]imap.UID) int {
	*toMarkSeen = append(*toMarkSeen, uid)
	addr := extractBounceAddr(body)
	if addr == "" {
		slog.Warn("imap: bounce address not found")
		return 0
	}
	reqID, err := db.LatestSentRequestByContact(d, addr)
	if err != nil {
		slog.Warn("imap: bounce not in DB", "contact", addr)
		return 0
	}
	if err := db.MarkError(d, reqID, "bounce: address not found"); err != nil {
		slog.Error("imap: mark error failed", "err", err)
		return 0
	}
	slog.Info("imap: bounce", "contact", addr, "request", reqID)
	return 1
}

func extractBody(raw string) string {
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

func extractDomain(contact string) string {
	parts := strings.SplitN(contact, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}
