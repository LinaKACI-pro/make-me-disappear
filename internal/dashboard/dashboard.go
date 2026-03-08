package dashboard

import (
	"database/sql"
	"html/template"
	"os"
	"time"

	_ "embed"
)

//go:embed dashboard.html
var tmplHTML string

// Row holds one broker's latest request state for the dashboard.
type Row struct {
	Name        string
	Region      string
	Status      string
	LastContact string
}

// Data holds all dashboard rendering data.
type Data struct {
	GeneratedAt string
	Rows        []Row
	Confirmed   int
	Sent        int
	Pending     int
	NeedsReview int
	Manual      int
	Error       int
	Total       int
}

// Generate queries the database, renders the dashboard HTML, and writes it to outPath.
func Generate(d *sql.DB, outPath string) error {
	data, err := fetchData(d)
	if err != nil {
		return err
	}

	tmpl, err := template.New("dashboard").Parse(tmplHTML)
	if err != nil {
		return err
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	return tmpl.Execute(f, data)
}

func fetchData(d *sql.DB) (*Data, error) {
	rows, err := d.Query(`SELECT b.name, b.region,
		COALESCE(r.status, 'no_request'),
		COALESCE(r.sent_at, '')
		FROM brokers b
		LEFT JOIN requests r ON r.broker_id = b.id
		WHERE b.active = 1
		ORDER BY b.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	data := &Data{
		GeneratedAt: time.Now().Format("2006-01-02 15:04"),
	}

	for rows.Next() {
		var r Row
		var sentAt string
		if err := rows.Scan(&r.Name, &r.Region, &r.Status, &sentAt); err != nil {
			return nil, err
		}
		if len(sentAt) >= 10 {
			r.LastContact = sentAt[:10]
		} else {
			r.LastContact = "—"
		}

		switch r.Status {
		case "confirmed":
			data.Confirmed++
		case "sent":
			data.Sent++
		case "pending", "no_request":
			data.Pending++
		case "needs_review":
			data.NeedsReview++
		case "manual_required":
			data.Manual++
		case "error":
			data.Error++
		}
		data.Total++

		data.Rows = append(data.Rows, r)
	}

	return data, rows.Err()
}
