package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
		CREATE TABLE IF NOT EXISTS tokens (
			id           TEXT PRIMARY KEY,
			label        TEXT NOT NULL DEFAULT '',
			token_hash   TEXT NOT NULL UNIQUE,
			prefix       TEXT NOT NULL DEFAULT '',
			scope_admin  INTEGER NOT NULL DEFAULT 0,
			scope_send   INTEGER NOT NULL DEFAULT 0,
			created_at   INTEGER NOT NULL,
			last_used_at INTEGER NOT NULL DEFAULT 0
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
		return false, err
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

// tokenRecord is one API token's metadata. The secret itself is never stored
// or returned — only its SHA-256 hash (for lookup) and a short display prefix.
type tokenRecord struct {
	ID         string
	Label      string
	Prefix     string
	ScopeAdmin bool
	ScopeSend  bool
	CreatedAt  int64
	LastUsedAt int64
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// addToken inserts a token with a caller-supplied secret. Used by createToken
// (random secret) and by the legacy-token migration (known secret).
func (s *store) addToken(label, secret string, admin, send bool) (string, error) {
	id := randomHex(8)
	prefix := secret
	if len(prefix) > 6 {
		prefix = prefix[:6]
	}
	_, err := s.db.Exec(`
		INSERT INTO tokens (id, label, token_hash, prefix, scope_admin, scope_send, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
		id, label, hashToken(secret), prefix, b2i(admin), b2i(send), time.Now().Unix())
	if err != nil {
		return "", err
	}
	return id, nil
}

// createToken generates a fresh random secret and stores its hash. The returned
// secret is the only time the plaintext exists — surface it once, then forget.
func (s *store) createToken(label string, admin, send bool) (id, secret string, err error) {
	secret = randomHex(32)
	id, err = s.addToken(label, secret, admin, send)
	if err != nil {
		return "", "", err
	}
	return id, secret, nil
}

// lookupToken finds a token by the hash of the presented secret, returning
// nil when there is no match. On a hit it bumps last_used_at (best-effort).
func (s *store) lookupToken(secret string) (*tokenRecord, error) {
	if secret == "" {
		return nil, nil
	}
	var t tokenRecord
	var admin, send int
	err := s.db.QueryRow(`
		SELECT id, label, prefix, scope_admin, scope_send, created_at, last_used_at
		FROM tokens WHERE token_hash = ?`, hashToken(secret)).
		Scan(&t.ID, &t.Label, &t.Prefix, &admin, &send, &t.CreatedAt, &t.LastUsedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.ScopeAdmin, t.ScopeSend = admin == 1, send == 1
	s.db.Exec(`UPDATE tokens SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), t.ID) // best-effort
	return &t, nil
}

func (s *store) countTokens() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens`).Scan(&n)
	return n, err
}

func (s *store) countAdminTokens() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE scope_admin = 1`).Scan(&n)
	return n, err
}

func (s *store) listTokens() ([]tokenRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, label, prefix, scope_admin, scope_send, created_at, last_used_at
		FROM tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tokenRecord
	for rows.Next() {
		var t tokenRecord
		var admin, send int
		if err := rows.Scan(&t.ID, &t.Label, &t.Prefix, &admin, &send, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		t.ScopeAdmin, t.ScopeSend = admin == 1, send == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *store) tokenByID(id string) (*tokenRecord, error) {
	var t tokenRecord
	var admin, send int
	err := s.db.QueryRow(`
		SELECT id, label, prefix, scope_admin, scope_send, created_at, last_used_at
		FROM tokens WHERE id = ?`, id).
		Scan(&t.ID, &t.Label, &t.Prefix, &admin, &send, &t.CreatedAt, &t.LastUsedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.ScopeAdmin, t.ScopeSend = admin == 1, send == 1
	return &t, nil
}

// updateToken applies whichever of label/admin/send are non-nil.
func (s *store) updateToken(id string, label *string, admin, send *bool) error {
	if label != nil {
		if _, err := s.db.Exec(`UPDATE tokens SET label = ? WHERE id = ?`, *label, id); err != nil {
			return err
		}
	}
	if admin != nil {
		if _, err := s.db.Exec(`UPDATE tokens SET scope_admin = ? WHERE id = ?`, b2i(*admin), id); err != nil {
			return err
		}
	}
	if send != nil {
		if _, err := s.db.Exec(`UPDATE tokens SET scope_send = ? WHERE id = ?`, b2i(*send), id); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) deleteToken(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
