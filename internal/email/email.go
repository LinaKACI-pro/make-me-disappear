package email

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"text/template"
	"time"

	"github.com/LinaKACI-pro/make-me-disappear/internal/config"
)

// TemplateData holds the data used to render email templates.
type TemplateData struct {
	FullName  string
	Email     string
	Phone     string
	Address   string
	OptOutURL string
}

// Render reads the appropriate template (FR for EU, EN otherwise), renders it
// with identity and broker data, and returns the subject and body.
func Render(templateDir string, brokerRegion, optOutURL string, identity config.Identity) (subject, body string, err error) {
	file := "rgpd_en.txt"
	if brokerRegion == "EU" {
		file = "rgpd_fr.txt"
	}

	tmpl, err := template.ParseFiles(templateDir + "/" + file)
	if err != nil {
		return "", "", fmt.Errorf("parse template %s: %w", file, err)
	}

	data := TemplateData{
		FullName:  identity.FirstName + " " + identity.LastName,
		Email:     identity.Email,
		Phone:     identity.Phone,
		Address:   identity.Address,
		OptOutURL: optOutURL,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("render template: %w", err)
	}

	raw := buf.String()
	// First line is "Subject: ...", rest is body.
	parts := strings.SplitN(raw, "\n", 2)
	if len(parts) == 2 && strings.HasPrefix(parts[0], "Subject: ") {
		subject = strings.TrimPrefix(parts[0], "Subject: ")
		body = strings.TrimLeft(parts[1], "\n")
	} else {
		subject = "Data Deletion Request"
		body = raw
	}

	return subject, body, nil
}

// Send sends an email via SMTP with STARTTLS.
// Dial timeout: 30s. Total operation deadline: 60s.
func Send(cfg config.SMTP, from, to, subject, body string) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	msgID := fmt.Sprintf("<%d.%s@github.com/LinaKACI-pro/make-me-disappear>", time.Now().UnixNano(), strings.ReplaceAll(from, "@", "."))
	date := time.Now().Format(time.RFC1123Z)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		from, to, subject, date, msgID, body)

	conn, err := (&net.Dialer{Timeout: 30 * time.Second}).Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	conn.SetDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
		return fmt.Errorf("smtp starttls: %w", err)
	}
	auth := smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := fmt.Fprint(w, msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	return w.Close()
}
