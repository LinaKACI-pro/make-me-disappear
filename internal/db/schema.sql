CREATE TABLE IF NOT EXISTS brokers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    region TEXT NOT NULL,
    method TEXT NOT NULL,
    contact TEXT,
    opt_out_url TEXT,
    notes TEXT,
    active BOOLEAN DEFAULT 1,
    last_updated DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    broker_id TEXT NOT NULL REFERENCES brokers(id),
    status TEXT NOT NULL DEFAULT 'pending',
    method_used TEXT,
    sent_at DATETIME,
    last_action DATETIME,
    next_retry DATETIME,
    attempt INTEGER DEFAULT 1,
    response_raw TEXT,
    notes TEXT
);

-- One request per broker: clean up legacy duplicates and enforce uniqueness.
DELETE FROM requests WHERE id NOT IN (
    SELECT MAX(id) FROM requests GROUP BY broker_id
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_requests_broker_id ON requests(broker_id);
