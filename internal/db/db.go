package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"gopkg.in/yaml.v3"
)

//go:embed schema.sql
var schema string

// Broker represents a data broker in the database.
type Broker struct {
	ID         string
	Name       string
	Region     string
	Method     string
	Contact    string
	OptOutURL  string
	Notes      string
	Active     bool
	LastUpdated time.Time
}

// Request represents a deletion request in the database.
type Request struct {
	ID          int
	BrokerID    string
	Status      string
	MethodUsed  string
	SentAt      sql.NullTime
	LastAction  sql.NullTime
	NextRetry   sql.NullTime
	Attempt     int
	ResponseRaw string
	Notes       string
}

// StatusCount holds a status and its count for the summary.
type StatusCount struct {
	Status string
	Count  int
}

// RequestWithBroker joins request data with broker info.
type RequestWithBroker struct {
	Request
	BrokerName   string
	BrokerRegion string
	BrokerMethod string
	Contact      string
	OptOutURL    string
}

type brokerYAML struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	Region    string `yaml:"region"`
	Method    string `yaml:"method"`
	Contact   string `yaml:"contact"`
	OptOutURL string `yaml:"opt_out_url"`
	Tier      int    `yaml:"tier"`
	Notes     string `yaml:"notes"`
}

type brokersFile struct {
	Brokers []brokerYAML `yaml:"brokers"`
}

// Open opens a SQLite database at path, enables WAL mode, and runs migrations.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate executes the embedded schema.sql against the database.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

// SeedBrokers reads a brokers YAML file and upserts brokers into the database.
func SeedBrokers(d *sql.DB, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read brokers file: %w", err)
	}
	var f brokersFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("parse brokers file: %w", err)
	}

	stmt, err := d.Prepare(`INSERT INTO brokers (id, name, region, method, contact, opt_out_url, notes, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, region=excluded.region, method=excluded.method,
			contact=excluded.contact, opt_out_url=excluded.opt_out_url, notes=excluded.notes,
			last_updated=CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, b := range f.Brokers {
		if _, err := stmt.Exec(b.ID, b.Name, b.Region, b.Method, b.Contact, b.OptOutURL, b.Notes); err != nil {
			return 0, fmt.Errorf("insert broker %s: %w", b.ID, err)
		}
	}
	return len(f.Brokers), nil
}

// CreatePendingRequests creates a pending request for each active broker that
// doesn't already have one with status pending or sent.
func CreatePendingRequests(d *sql.DB) (int, error) {
	res, err := d.Exec(`INSERT INTO requests (broker_id, status)
		SELECT b.id, 'pending' FROM brokers b
		WHERE b.active = 1
		AND NOT EXISTS (
			SELECT 1 FROM requests r
			WHERE r.broker_id = b.id AND r.status IN ('pending', 'sent')
		)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PendingRequests returns requests with status 'pending'.
func PendingRequests(d *sql.DB) ([]RequestWithBroker, error) {
	rows, err := d.Query(`SELECT r.id, r.broker_id, r.status, r.attempt,
		b.name, b.region, b.method, b.contact, b.opt_out_url
		FROM requests r JOIN brokers b ON r.broker_id = b.id
		WHERE r.status = 'pending'
		ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RequestWithBroker
	for rows.Next() {
		var rw RequestWithBroker
		if err := rows.Scan(&rw.ID, &rw.BrokerID, &rw.Status, &rw.Attempt,
			&rw.BrokerName, &rw.BrokerRegion, &rw.BrokerMethod, &rw.Contact, &rw.OptOutURL); err != nil {
			return nil, err
		}
		result = append(result, rw)
	}
	return result, rows.Err()
}

// MarkSent updates a request as sent with a retry scheduled in 35 days.
func MarkSent(d *sql.DB, requestID int) error {
	_, err := d.Exec(`UPDATE requests SET status='sent', method_used='email',
		sent_at=datetime('now'), last_action=datetime('now'),
		next_retry=datetime('now', '+35 days')
		WHERE id=?`, requestID)
	return err
}

// MarkError updates a request as error with notes.
func MarkError(d *sql.DB, requestID int, notes string) error {
	_, err := d.Exec(`UPDATE requests SET status='error', last_action=datetime('now'), notes=?
		WHERE id=?`, notes, requestID)
	return err
}

// MarkManualRequired updates a request as manual_required.
func MarkManualRequired(d *sql.DB, requestID int) error {
	_, err := d.Exec(`UPDATE requests SET status='manual_required', last_action=datetime('now')
		WHERE id=?`, requestID)
	return err
}

// MarkRetry increments the attempt counter and reschedules. If attempt > 3,
// marks as manual_required instead.
func MarkRetry(d *sql.DB, requestID, currentAttempt int) error {
	if currentAttempt >= 3 {
		return MarkManualRequired(d, requestID)
	}
	_, err := d.Exec(`UPDATE requests SET status='pending', attempt=attempt+1,
		last_action=datetime('now'), next_retry=NULL
		WHERE id=?`, requestID)
	return err
}

// MarkNeedsReview updates a request with response data for manual review.
func MarkNeedsReview(d *sql.DB, requestID int, responseRaw string) error {
	_, err := d.Exec(`UPDATE requests SET status='needs_review',
		last_action=datetime('now'), response_raw=?, next_retry=NULL
		WHERE id=?`, responseRaw, requestID)
	return err
}

// UpdateStatus sets the status of a request (used by inbox review).
func UpdateStatus(d *sql.DB, requestID int, status string) error {
	_, err := d.Exec(`UPDATE requests SET status=?, last_action=datetime('now')
		WHERE id=?`, status, requestID)
	return err
}

// DueRetries returns sent requests older than 35 days that haven't been retried yet.
func DueRetries(d *sql.DB) ([]RequestWithBroker, error) {
	rows, err := d.Query(`SELECT r.id, r.broker_id, r.status, r.attempt,
		b.name, b.region, b.method, b.contact, b.opt_out_url
		FROM requests r JOIN brokers b ON r.broker_id = b.id
		WHERE r.status = 'sent'
		AND r.next_retry IS NOT NULL AND r.next_retry <= datetime('now')
		ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RequestWithBroker
	for rows.Next() {
		var rw RequestWithBroker
		if err := rows.Scan(&rw.ID, &rw.BrokerID, &rw.Status, &rw.Attempt,
			&rw.BrokerName, &rw.BrokerRegion, &rw.BrokerMethod, &rw.Contact, &rw.OptOutURL); err != nil {
			return nil, err
		}
		result = append(result, rw)
	}
	return result, rows.Err()
}

// StatusSummary returns counts grouped by status.
func StatusSummary(d *sql.DB) ([]StatusCount, error) {
	rows, err := d.Query(`SELECT status, COUNT(*) FROM requests GROUP BY status ORDER BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []StatusCount
	for rows.Next() {
		var sc StatusCount
		if err := rows.Scan(&sc.Status, &sc.Count); err != nil {
			return nil, err
		}
		result = append(result, sc)
	}
	return result, rows.Err()
}

// NeedsReviewRequests returns requests needing manual review.
func NeedsReviewRequests(d *sql.DB) ([]RequestWithBroker, error) {
	return queryRequests(d, "needs_review")
}

// ManualRequests returns requests requiring manual action.
func ManualRequests(d *sql.DB) ([]RequestWithBroker, error) {
	return queryRequests(d, "manual_required")
}

func queryRequests(d *sql.DB, status string) ([]RequestWithBroker, error) {
	rows, err := d.Query(`SELECT r.id, r.broker_id, r.status, r.attempt, r.response_raw,
		b.name, b.region, b.method, b.contact, b.opt_out_url
		FROM requests r JOIN brokers b ON r.broker_id = b.id
		WHERE r.status = ?
		ORDER BY r.id`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RequestWithBroker
	for rows.Next() {
		var rw RequestWithBroker
		var raw sql.NullString
		if err := rows.Scan(&rw.ID, &rw.BrokerID, &rw.Status, &rw.Attempt, &raw,
			&rw.BrokerName, &rw.BrokerRegion, &rw.BrokerMethod, &rw.Contact, &rw.OptOutURL); err != nil {
			return nil, err
		}
		rw.ResponseRaw = raw.String
		result = append(result, rw)
	}
	return result, rows.Err()
}

// BrokerByID fetches a single broker by ID.
func BrokerByID(d *sql.DB, id string) (*Broker, error) {
	var b Broker
	err := d.QueryRow(`SELECT id, name, region, method, contact, opt_out_url, notes, active
		FROM brokers WHERE id = ?`, id).Scan(&b.ID, &b.Name, &b.Region, &b.Method, &b.Contact, &b.OptOutURL, &b.Notes, &b.Active)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// FindOrCreateRequest finds an existing pending/sent request for a broker, or creates one.
func FindOrCreateRequest(d *sql.DB, brokerID string) (int, error) {
	var id int
	err := d.QueryRow(`SELECT id FROM requests WHERE broker_id = ? AND status IN ('pending', 'sent')
		ORDER BY id DESC LIMIT 1`, brokerID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := d.Exec(`INSERT INTO requests (broker_id, status) VALUES (?, 'pending')`, brokerID)
	if err != nil {
		return 0, err
	}
	insertedID, _ := res.LastInsertId()
	return int(insertedID), nil
}

// AllBrokers returns all brokers with their contact domain for IMAP matching.
func AllBrokers(d *sql.DB) ([]Broker, error) {
	rows, err := d.Query(`SELECT id, name, region, method, contact, opt_out_url, notes, active FROM brokers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Broker
	for rows.Next() {
		var b Broker
		if err := rows.Scan(&b.ID, &b.Name, &b.Region, &b.Method, &b.Contact, &b.OptOutURL, &b.Notes, &b.Active); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

// ShouldStartNewCycle returns true if the most recent sent_at is older than 90
// days or if there are no requests at all.
func ShouldStartNewCycle(d *sql.DB) (bool, error) {
	var maxSentAt sql.NullString
	err := d.QueryRow(`SELECT MAX(sent_at) FROM requests`).Scan(&maxSentAt)
	if err != nil {
		return false, err
	}
	if !maxSentAt.Valid {
		return true, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", maxSentAt.String)
	if err != nil {
		return true, nil
	}
	return time.Since(t) > 90*24*time.Hour, nil
}

// LatestSentRequest finds the most recent sent request for a broker.
func LatestSentRequest(d *sql.DB, brokerID string) (int, error) {
	var id int
	err := d.QueryRow(`SELECT id FROM requests WHERE broker_id = ? AND status = 'sent'
		ORDER BY sent_at DESC LIMIT 1`, brokerID).Scan(&id)
	return id, err
}

// LatestSentRequestByContact finds the most recent sent request for the broker
// whose contact email matches (case-insensitive).
func LatestSentRequestByContact(d *sql.DB, contact string) (int, error) {
	var id int
	err := d.QueryRow(`
		SELECT r.id FROM requests r
		JOIN brokers b ON b.id = r.broker_id
		WHERE LOWER(b.contact) = LOWER(?) AND r.status = 'sent'
		ORDER BY r.id DESC LIMIT 1`, contact).Scan(&id)
	return id, err
}
