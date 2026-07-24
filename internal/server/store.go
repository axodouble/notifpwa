package server

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// store wraps the SQLite database. One file holds everything: settings
// (VAPID keys, API token, app name, icon) and device subscriptions.
type store struct{ db *sql.DB }

// subscription is one installed device (a browser PushSubscription).
type subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value BLOB
		);
		CREATE TABLE IF NOT EXISTS subscriptions (
			endpoint   TEXT PRIMARY KEY,
			p256dh     TEXT NOT NULL,
			auth       TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
	`); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

// getSetting returns the raw bytes for a key, or nil if it does not exist.
func (s *store) getSetting(key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func (s *store) getSettingStr(key string) (string, error) {
	b, err := s.getSetting(key)
	return string(b), err
}

func (s *store) setSetting(key string, value []byte) error {
	_, err := s.db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// upsertSubscription stores a device, replacing any existing row for the same
// endpoint so re-subscribing is idempotent.
func (s *store) upsertSubscription(sub subscription) error {
	_, err := s.db.Exec(`
		INSERT INTO subscriptions (endpoint, p256dh, auth, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth`,
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, time.Now().Unix())
	return err
}

func (s *store) listSubscriptions() ([]subscription, error) {
	rows, err := s.db.Query(`SELECT endpoint, p256dh, auth FROM subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []subscription
	for rows.Next() {
		var sub subscription
		if err := rows.Scan(&sub.Endpoint, &sub.Keys.P256dh, &sub.Keys.Auth); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func (s *store) deleteSubscription(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

func (s *store) countSubscriptions() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&n)
	return n, err
}
