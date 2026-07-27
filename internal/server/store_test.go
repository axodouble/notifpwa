package server

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *store {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { st.close() })
	return st
}

func TestSettingsRoundTrip(t *testing.T) {
	st := newTestStore(t)

	if v, _ := st.getSetting("missing"); v != nil {
		t.Fatalf("expected nil for missing key, got %q", v)
	}

	if err := st.setSetting("app_name", []byte("Alerts")); err != nil {
		t.Fatalf("setSetting: %v", err)
	}
	if got, _ := st.getSettingStr("app_name"); got != "Alerts" {
		t.Fatalf("got %q, want Alerts", got)
	}

	// Upsert overwrites.
	st.setSetting("app_name", []byte("Pings"))
	if got, _ := st.getSettingStr("app_name"); got != "Pings" {
		t.Fatalf("got %q, want Pings", got)
	}
}

func mkSub(endpoint string) subscription {
	var s subscription
	s.Endpoint = endpoint
	s.Keys.P256dh = "p256-" + endpoint
	s.Keys.Auth = "auth-" + endpoint
	return s
}

func TestStoreUsesWAL(t *testing.T) {
	st := newTestStore(t)
	var mode string
	if err := st.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestSubscriptionUpsertAndDelete(t *testing.T) {
	st := newTestStore(t)

	if err := st.upsertSubscription(mkSub("https://push/a"), ""); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	// Re-subscribing the same endpoint is idempotent (no duplicate row).
	if err := st.upsertSubscription(mkSub("https://push/a"), ""); err != nil {
		t.Fatalf("upsert a again: %v", err)
	}
	st.upsertSubscription(mkSub("https://push/b"), "")

	if n, _ := st.countSubscriptions(); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}

	subs, err := st.listSubscriptions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("listed %d, want 2", len(subs))
	}

	if err := st.deleteSubscription("https://push/a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := st.countSubscriptions(); n != 1 {
		t.Fatalf("count after delete = %d, want 1", n)
	}
}

func TestDeviceMetadataAndLabel(t *testing.T) {
	st := newTestStore(t)
	if err := st.upsertSubscription(mkSub("https://push/a"), "Mozilla/5.0 Test"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	devs, err := st.listDevices()
	if err != nil {
		t.Fatalf("listDevices: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want 1", len(devs))
	}
	if devs[0].UserAgent != "Mozilla/5.0 Test" {
		t.Fatalf("user_agent = %q", devs[0].UserAgent)
	}
	if devs[0].CreatedAt == 0 || devs[0].LastSeen == 0 {
		t.Fatalf("timestamps not set: %+v", devs[0])
	}

	if err := st.setDeviceLabel("https://push/a", "Pixel 8"); err != nil {
		t.Fatalf("setDeviceLabel: %v", err)
	}
	devs, _ = st.listDevices()
	if devs[0].Label != "Pixel 8" {
		t.Fatalf("label = %q, want Pixel 8", devs[0].Label)
	}
}

// TestMigrationUpgradesLegacyDBIdempotently proves migrate() upgrades a
// pre-existing database created under the original 4-column schema (no
// user_agent/last_seen/label) without losing data, and that opening the
// upgraded database a second time is a safe no-op.
func TestMigrationUpgradesLegacyDBIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"

	// 1. Seed a raw DB using only the original legacy schema.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE subscriptions (
			endpoint   TEXT PRIMARY KEY,
			p256dh     TEXT NOT NULL,
			auth       TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	const seededCreatedAt = 1700000000
	if _, err := raw.Exec(`
		INSERT INTO subscriptions (endpoint, p256dh, auth, created_at) VALUES (?, ?, ?, ?)`,
		"https://push/legacy", "p256-legacy", "auth-legacy", seededCreatedAt); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// 2. Open via openStore (runs migrate()) and check the row survived with
	// new columns defaulted and created_at preserved.
	st, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore (first, migrate): %v", err)
	}
	devs, err := st.listDevices()
	if err != nil {
		t.Fatalf("listDevices after migrate: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices after migrate, want 1", len(devs))
	}
	d := devs[0]
	if d.Endpoint != "https://push/legacy" {
		t.Fatalf("endpoint = %q, want https://push/legacy", d.Endpoint)
	}
	if d.CreatedAt != seededCreatedAt {
		t.Fatalf("created_at = %d, want %d (preserved)", d.CreatedAt, seededCreatedAt)
	}
	if d.Label != "" {
		t.Fatalf("label = %q, want empty default", d.Label)
	}
	if d.UserAgent != "" {
		t.Fatalf("user_agent = %q, want empty default", d.UserAgent)
	}
	if d.LastSeen != 0 {
		t.Fatalf("last_seen = %d, want 0 default", d.LastSeen)
	}
	if err := st.close(); err != nil {
		t.Fatalf("close after first open: %v", err)
	}

	// 3. Open a second time: migrate() runs again against an already-migrated
	// DB and must be a safe no-op, with the row still intact.
	st2, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore (second, re-migrate): %v", err)
	}
	defer st2.close()
	devs2, err := st2.listDevices()
	if err != nil {
		t.Fatalf("listDevices after second open: %v", err)
	}
	if len(devs2) != 1 {
		t.Fatalf("got %d devices after second open, want 1", len(devs2))
	}
	if devs2[0].CreatedAt != seededCreatedAt {
		t.Fatalf("created_at after second open = %d, want %d", devs2[0].CreatedAt, seededCreatedAt)
	}
}

func TestTokenCreateAndLookup(t *testing.T) {
	st := newTestStore(t)

	id, secret, err := st.createToken("CI", false, true)
	if err != nil {
		t.Fatalf("createToken: %v", err)
	}
	if id == "" || len(secret) != 64 {
		t.Fatalf("id=%q secret len=%d, want non-empty id and 64-char secret", id, len(secret))
	}

	// The plaintext secret must never be stored.
	var found int
	st.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE token_hash = ?`, secret).Scan(&found)
	if found != 0 {
		t.Fatal("plaintext secret found in token_hash column")
	}

	rec, err := st.lookupToken(secret)
	if err != nil || rec == nil {
		t.Fatalf("lookupToken: rec=%v err=%v", rec, err)
	}
	if rec.ID != id || rec.ScopeAdmin || !rec.ScopeSend || rec.Prefix != secret[:6] {
		t.Fatalf("rec = %+v, want send-only with prefix %q", rec, secret[:6])
	}

	if bad, _ := st.lookupToken("nope"); bad != nil {
		t.Fatalf("lookupToken(wrong) = %+v, want nil", bad)
	}
	if empty, _ := st.lookupToken(""); empty != nil {
		t.Fatalf("lookupToken(\"\") = %+v, want nil", empty)
	}
}
