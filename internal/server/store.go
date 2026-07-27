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
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite allows one writer; serialize to avoid "database is locked".
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
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
	st := &store{db: db}
	if err := st.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

// migrate adds columns introduced after the initial schema. Additive and
// idempotent so existing databases upgrade in place.
func (s *store) migrate() error {
	for _, col := range []struct{ name, ddl string }{
		{"user_agent", "ALTER TABLE subscriptions ADD COLUMN user_agent TEXT NOT NULL DEFAULT ''"},
		{"last_seen", "ALTER TABLE subscriptions ADD COLUMN last_seen INTEGER NOT NULL DEFAULT 0"},
		{"label", "ALTER TABLE subscriptions ADD COLUMN label TEXT NOT NULL DEFAULT ''"},
	} {
		has, err := s.hasColumn("subscriptions", col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		// Fall back for sqlite builds without the pragma table-valued function.
		return s.hasColumnFallback(table, column)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// hasColumnFallback uses PRAGMA table_info via db.Query, scanning all
// columns since PRAGMA statements can't be parameterized.
func (s *store) hasColumnFallback(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  any
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *store) close() error { return s.db.Close() }

// ping verifies the database connection is alive.
func (s *store) ping() error { return s.db.Ping() }

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
// endpoint so re-subscribing is idempotent. created_at is preserved on
// conflict; user_agent and last_seen are refreshed.
func (s *store) upsertSubscription(sub subscription, userAgent string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`
		INSERT INTO subscriptions (endpoint, p256dh, auth, created_at, user_agent, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET
			p256dh = excluded.p256dh,
			auth = excluded.auth,
			user_agent = excluded.user_agent,
			last_seen = excluded.last_seen`,
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, now, userAgent, now)
	return err
}

// device is a stored subscription with its management metadata.
type device struct {
	Endpoint  string `json:"endpoint"`
	Label     string `json:"label"`
	UserAgent string `json:"user_agent"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
}

func (s *store) listDevices() ([]device, error) {
	rows, err := s.db.Query(`SELECT endpoint, label, user_agent, created_at, last_seen FROM subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []device
	for rows.Next() {
		var d device
		if err := rows.Scan(&d.Endpoint, &d.Label, &d.UserAgent, &d.CreatedAt, &d.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *store) setDeviceLabel(endpoint, label string) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET label = ? WHERE endpoint = ?`, label, endpoint)
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
