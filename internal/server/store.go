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

// subscription is one installed device (a browser PushSubscription) plus the
// client-generated DeviceID that survives it. A push endpoint is not a stable
// identity: iOS in particular drops a PWA's subscription after inactivity, and
// the replacement has a different endpoint. DeviceID is what lets the server
// recognise the returning device and carry its rooms over.
type subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	DeviceID string `json:"device_id"`
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
		CREATE TABLE IF NOT EXISTS rooms (
			name       TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS room_subscriptions (
			room        TEXT    NOT NULL,
			endpoint    TEXT    NOT NULL,
			secret_hash TEXT    NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			PRIMARY KEY (room, endpoint),
			FOREIGN KEY (endpoint) REFERENCES subscriptions(endpoint) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS notification_log (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			room        TEXT    NOT NULL,
			secret_hash TEXT    NOT NULL DEFAULT '',
			title       TEXT    NOT NULL DEFAULT '',
			body        TEXT    NOT NULL DEFAULT '',
			url         TEXT    NOT NULL DEFAULT '',
			sent        INTEGER NOT NULL DEFAULT 0,
			failed      INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_notification_log_room ON notification_log(room, id);
		CREATE INDEX IF NOT EXISTS idx_room_subs_endpoint ON room_subscriptions(endpoint);
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
		{"device_id", "ALTER TABLE subscriptions ADD COLUMN device_id TEXT NOT NULL DEFAULT ''"},
		{"expired_at", "ALTER TABLE subscriptions ADD COLUMN expired_at INTEGER NOT NULL DEFAULT 0"},
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

	// One row per device id. Partial so the many pre-device-id rows, which all
	// carry '', do not collide with each other.
	if _, err := s.db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_device_id
		ON subscriptions(device_id) WHERE device_id != ''`); err != nil {
		return err
	}

	// Rooms-only: a token without the admin scope grants nothing now — drop it.
	if _, err := s.db.Exec(`DELETE FROM tokens WHERE scope_admin = 0`); err != nil {
		return err
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

func (s *store) deleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}

// expiredSubscriptionTTL is how long a subscription the push service reported
// gone is kept before being collected. It is generous on purpose: the row is
// the only thing holding the device's room memberships, and a user who lets a
// PWA sit unopened for weeks should still find their rooms when they return.
const expiredSubscriptionTTL = 90 * 24 * time.Hour

// upsertSubscription stores a device, replacing any existing row for the same
// endpoint so re-subscribing is idempotent. created_at is preserved on
// conflict; user_agent and last_seen are refreshed.
func (s *store) upsertSubscription(sub subscription, userAgent string) error {
	return s.upsertSubscriptionFrom(sub, userAgent, "")
}

// upsertSubscriptionFrom is upsertSubscription with recovery: when this device
// is already known under a different endpoint, its rooms, label and created_at
// move to the new endpoint and the dead row is dropped. The previous endpoint
// is found by device id, or named explicitly by prevEndpoint for callers that
// have no device id to work with (the service worker's pushsubscriptionchange
// handler, which cannot read the page's storage).
func (s *store) upsertSubscriptionFrom(sub subscription, userAgent, prevEndpoint string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Identify the row this device previously occupied, if any.
	var old struct {
		endpoint  string
		createdAt int64
		label     string
		deviceID  string
	}
	lookup := func(where string, arg any) error {
		err := tx.QueryRow(
			`SELECT endpoint, created_at, label, device_id FROM subscriptions WHERE `+where, arg).
			Scan(&old.endpoint, &old.createdAt, &old.label, &old.deviceID)
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if sub.DeviceID != "" {
		if err := lookup("device_id = ?", sub.DeviceID); err != nil {
			return err
		}
	}
	if old.endpoint == "" && prevEndpoint != "" {
		if err := lookup("endpoint = ?", prevEndpoint); err != nil {
			return err
		}
	}

	now := time.Now().Unix()
	createdAt, label, deviceID := now, "", sub.DeviceID
	migrating := old.endpoint != "" && old.endpoint != sub.Endpoint
	if migrating {
		createdAt, label = old.createdAt, old.label
		if deviceID == "" {
			// The service worker has no way to read the page's device id, so
			// keep the one already on record rather than blanking it — the next
			// rotation still needs something to recognise this device by.
			deviceID = old.deviceID
		}
		// Free the device id before the new row claims it: the unique index is
		// checked on insert, before the old row is deleted below.
		if _, err := tx.Exec(
			`UPDATE subscriptions SET device_id = '' WHERE endpoint = ?`, old.endpoint); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`
		INSERT INTO subscriptions (endpoint, p256dh, auth, created_at, user_agent, last_seen, label, device_id, expired_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(endpoint) DO UPDATE SET
			p256dh = excluded.p256dh,
			auth = excluded.auth,
			user_agent = excluded.user_agent,
			last_seen = excluded.last_seen,
			device_id = excluded.device_id,
			expired_at = 0`,
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, createdAt, userAgent, now, label, deviceID); err != nil {
		return err
	}

	if migrating {
		// Copy memberships onto the live row first so the foreign key holds at
		// every step; OR IGNORE keeps the new endpoint's own row if it somehow
		// already belongs to the same group. Deleting the old row then cascades
		// away whatever is left behind.
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO room_subscriptions (room, endpoint, secret_hash, created_at)
			SELECT room, ?, secret_hash, created_at FROM room_subscriptions WHERE endpoint = ?`,
			sub.Endpoint, old.endpoint); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM subscriptions WHERE endpoint = ?`, old.endpoint); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// expireSubscription marks an endpoint the push service reported gone. The row
// is kept rather than deleted so the device's room memberships survive until it
// re-subscribes; see upsertSubscriptionFrom. Expired endpoints are skipped when
// sending (roomRecipients) and collected by purgeExpiredSubscriptions.
func (s *store) expireSubscription(endpoint string) error {
	_, err := s.db.Exec(
		`UPDATE subscriptions SET expired_at = ? WHERE endpoint = ? AND expired_at = 0`,
		time.Now().Unix(), endpoint)
	return err
}

// purgeExpiredSubscriptions deletes subscriptions that expired before cutoff,
// cascading to their room memberships.
func (s *store) purgeExpiredSubscriptions(cutoff int64) error {
	_, err := s.db.Exec(
		`DELETE FROM subscriptions WHERE expired_at != 0 AND expired_at < ?`, cutoff)
	return err
}

// device is a stored subscription with its management metadata. A non-zero
// ExpiredAt means the push service reported the endpoint gone; the row is held
// so the device's rooms are waiting for it if it re-subscribes.
type device struct {
	Endpoint  string `json:"endpoint"`
	Label     string `json:"label"`
	UserAgent string `json:"user_agent"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	ExpiredAt int64  `json:"expired_at"`
}

func (s *store) listDevices() ([]device, error) {
	rows, err := s.db.Query(`SELECT endpoint, label, user_agent, created_at, last_seen, expired_at FROM subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []device
	for rows.Next() {
		var d device
		if err := rows.Scan(&d.Endpoint, &d.Label, &d.UserAgent, &d.CreatedAt, &d.LastSeen, &d.ExpiredAt); err != nil {
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

func (s *store) deleteSubscription(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// countSubscriptions counts devices that can still be reached; expired rows are
// bookkeeping for a possible return, not live subscribers.
func (s *store) countSubscriptions() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE expired_at = 0`).Scan(&n)
	return n, err
}

// tokenRecord is one API token's metadata. The secret itself is never stored
// or returned — only its SHA-256 hash (for lookup) and a short display prefix.
type tokenRecord struct {
	ID         string
	Label      string
	Prefix     string
	CreatedAt  int64
	LastUsedAt int64
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// addToken inserts a token with a caller-supplied secret. All tokens are admin
// credentials (scope_admin=1); scope_send is retained as a dormant column.
func (s *store) addToken(label, secret string) (string, error) {
	id := randomHex(8)
	prefix := secret
	if len(prefix) > 6 {
		prefix = prefix[:6]
	}
	_, err := s.db.Exec(`
		INSERT INTO tokens (id, label, token_hash, prefix, scope_admin, scope_send, created_at, last_used_at)
		VALUES (?, ?, ?, ?, 1, 0, ?, 0)`,
		id, label, hashToken(secret), prefix, time.Now().Unix())
	if err != nil {
		return "", err
	}
	return id, nil
}

// createToken generates a fresh random admin token and returns the one-time secret.
func (s *store) createToken(label string) (id, secret string, err error) {
	secret = randomHex(32)
	id, err = s.addToken(label, secret)
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
	err := s.db.QueryRow(`
		SELECT id, label, prefix, created_at, last_used_at
		FROM tokens WHERE token_hash = ?`, hashToken(secret)).
		Scan(&t.ID, &t.Label, &t.Prefix, &t.CreatedAt, &t.LastUsedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.db.Exec(`UPDATE tokens SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), t.ID) // best-effort
	return &t, nil
}

func (s *store) countTokens() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens`).Scan(&n)
	return n, err
}

func (s *store) listTokens() ([]tokenRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, label, prefix, created_at, last_used_at
		FROM tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tokenRecord
	for rows.Next() {
		var t tokenRecord
		if err := rows.Scan(&t.ID, &t.Label, &t.Prefix, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *store) tokenByID(id string) (*tokenRecord, error) {
	var t tokenRecord
	err := s.db.QueryRow(`
		SELECT id, label, prefix, created_at, last_used_at
		FROM tokens WHERE id = ?`, id).
		Scan(&t.ID, &t.Label, &t.Prefix, &t.CreatedAt, &t.LastUsedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *store) updateToken(id string, label *string) error {
	if label != nil {
		if _, err := s.db.Exec(`UPDATE tokens SET label = ? WHERE id = ?`, *label, id); err != nil {
			return err
		}
	}
	return nil
}

// wouldStrandAdmin reports whether deleting token id would remove the last
// token. Only meaningful when no root token is configured.
func (s *store) wouldStrandAdmin(id string) (bool, error) {
	count, err := s.countTokens()
	if err != nil {
		return false, err
	}
	if count > 1 {
		return false, nil
	}
	rec, err := s.tokenByID(id)
	if err != nil {
		return false, err
	}
	return rec != nil, nil
}

func (s *store) deleteToken(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
