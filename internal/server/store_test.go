package server

import (
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
