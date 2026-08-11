package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func mkDeviceSub(endpoint, deviceID string) subscription {
	s := mkSub(endpoint)
	s.DeviceID = deviceID
	return s
}

func roomNames(t *testing.T, st *store, endpoint string) []string {
	t.Helper()
	rooms, err := st.deviceRooms(endpoint)
	if err != nil {
		t.Fatalf("deviceRooms(%s): %v", endpoint, err)
	}
	out := make([]string, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.Room)
	}
	return out
}

// TestResubscribeCarriesRoomsToNewEndpoint is the core regression: iOS silently
// drops a push subscription and the next subscribe() yields a brand-new
// endpoint. The device is the same, so its room memberships (and their secrets)
// must follow it rather than being stranded on the dead endpoint.
func TestResubscribeCarriesRoomsToNewEndpoint(t *testing.T) {
	st := newTestStore(t)
	const dev = "device-1"
	if err := st.upsertSubscription(mkDeviceSub("https://push/old", dev), "iPhone"); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	secret := "s3cret"
	st.joinRoom("alerts", "https://push/old", &secret)
	st.joinRoom("news", "https://push/old", nil)
	st.setDeviceLabel("https://push/old", "Jasper's iPhone")

	// iOS re-subscribes the same device under a new endpoint.
	if err := st.upsertSubscription(mkDeviceSub("https://push/new", dev), "iPhone"); err != nil {
		t.Fatalf("upsert new: %v", err)
	}

	if got := roomNames(t, st, "https://push/new"); len(got) != 2 {
		t.Fatalf("new endpoint rooms = %v, want [alerts news]", got)
	}
	if got := roomNames(t, st, "https://push/old"); len(got) != 0 {
		t.Fatalf("old endpoint still has rooms %v, want none", got)
	}
	// The per-room secret must survive, or the device stops matching posts.
	subs, err := st.roomRecipients("alerts", secretHash(secret))
	if err != nil {
		t.Fatalf("roomRecipients: %v", err)
	}
	if len(subs) != 1 || subs[0].Endpoint != "https://push/new" {
		t.Fatalf("recipients = %+v, want the new endpoint", subs)
	}

	// One device, not two — and the admin's label follows it.
	devs, _ := st.listDevices()
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want 1 (old row should be replaced)", len(devs))
	}
	if devs[0].Label != "Jasper's iPhone" {
		t.Fatalf("label = %q, want it carried to the new endpoint", devs[0].Label)
	}
}

// A device that re-subscribes on the SAME endpoint keeps everything and does
// not trip the migration path.
func TestResubscribeSameEndpointIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	const dev = "device-1"
	st.upsertSubscription(mkDeviceSub("https://push/a", dev), "UA")
	st.joinRoom("alerts", "https://push/a", nil)
	if err := st.upsertSubscription(mkDeviceSub("https://push/a", dev), "UA"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := roomNames(t, st, "https://push/a"); len(got) != 1 {
		t.Fatalf("rooms = %v, want [alerts]", got)
	}
	if n, _ := st.countSubscriptions(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

// Distinct devices must never be merged, and a device with no id (a legacy row
// stored before device ids existed) must not swallow another's memberships.
func TestResubscribeKeepsDistinctDevicesSeparate(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkDeviceSub("https://push/a", "device-a"), "")
	st.upsertSubscription(mkDeviceSub("https://push/b", "device-b"), "")
	st.upsertSubscription(mkSub("https://push/legacy"), "") // no device id
	st.joinRoom("r", "https://push/a", nil)
	st.joinRoom("r", "https://push/legacy", nil)

	// A second id-less device must not inherit the legacy device's rooms.
	if err := st.upsertSubscription(mkSub("https://push/legacy2"), ""); err != nil {
		t.Fatalf("upsert legacy2: %v", err)
	}
	if got := roomNames(t, st, "https://push/legacy2"); len(got) != 0 {
		t.Fatalf("id-less device inherited rooms %v", got)
	}
	if got := roomNames(t, st, "https://push/legacy"); len(got) != 1 {
		t.Fatalf("legacy device lost its rooms: %v", got)
	}
	if n, _ := st.countSubscriptions(); n != 4 {
		t.Fatalf("count = %d, want 4", n)
	}
}

// A service-worker migration knows no device id, so it must keep the one
// already on the row — otherwise the page's id stops matching and the NEXT
// endpoint rotation strands the rooms.
func TestOldEndpointMigrationKeepsDeviceID(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkDeviceSub("https://push/a", "device-1"), "")
	st.joinRoom("r", "https://push/a", nil)

	// pushsubscriptionchange: new endpoint, no device id, names the old one.
	if err := st.upsertSubscriptionFrom(mkSub("https://push/b"), "", "https://push/a"); err != nil {
		t.Fatalf("upsertSubscriptionFrom: %v", err)
	}
	// The page later re-subscribes again under yet another endpoint, using only
	// the device id it has always had.
	if err := st.upsertSubscription(mkDeviceSub("https://push/c", "device-1"), ""); err != nil {
		t.Fatalf("upsert c: %v", err)
	}
	if got := roomNames(t, st, "https://push/c"); len(got) != 1 {
		t.Fatalf("rooms after two rotations = %v, want [r]", got)
	}
	if n, _ := st.countSubscriptions(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

// TestExpiredSubscriptionKeepsRoomsUntilDeviceReturns proves a 410 from the
// push service no longer destroys the device's memberships. The endpoint stops
// receiving pushes immediately, but the rooms wait for the device to come back.
func TestExpiredSubscriptionKeepsRoomsUntilDeviceReturns(t *testing.T) {
	st := newTestStore(t)
	const dev = "device-1"
	st.upsertSubscription(mkDeviceSub("https://push/old", dev), "")
	st.joinRoom("alerts", "https://push/old", nil)

	if err := st.expireSubscription("https://push/old"); err != nil {
		t.Fatalf("expireSubscription: %v", err)
	}
	// Expired endpoints are skipped when sending...
	subs, _ := st.roomRecipients("alerts", "")
	if len(subs) != 0 {
		t.Fatalf("expired endpoint still a recipient: %+v", subs)
	}
	// ...but the membership is still there to be reclaimed.
	if got := roomNames(t, st, "https://push/old"); len(got) != 1 {
		t.Fatalf("membership lost on expiry: %v", got)
	}

	// The device returns with a fresh endpoint and gets its room back.
	if err := st.upsertSubscription(mkDeviceSub("https://push/new", dev), ""); err != nil {
		t.Fatalf("upsert new: %v", err)
	}
	subs, _ = st.roomRecipients("alerts", "")
	if len(subs) != 1 || subs[0].Endpoint != "https://push/new" {
		t.Fatalf("recipients after return = %+v, want the new endpoint", subs)
	}
}

// Re-subscribing on the same endpoint must clear the expired flag, otherwise a
// device that recovers without an endpoint change stays permanently silent.
func TestResubscribeClearsExpiry(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkSub("https://push/a"), "")
	st.joinRoom("r", "https://push/a", nil)
	st.expireSubscription("https://push/a")
	st.upsertSubscription(mkSub("https://push/a"), "")
	subs, _ := st.roomRecipients("r", "")
	if len(subs) != 1 {
		t.Fatalf("recipients = %d, want 1 (expiry should be cleared)", len(subs))
	}
}

// Long-expired devices are eventually collected so the table cannot grow
// without bound; recently-expired ones are left alone to be reclaimed.
func TestPurgeExpiredSubscriptions(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkSub("https://push/stale"), "")
	st.upsertSubscription(mkSub("https://push/recent"), "")
	st.upsertSubscription(mkSub("https://push/live"), "")

	now := time.Now()
	old := now.Add(-2 * expiredSubscriptionTTL).Unix()
	st.db.Exec(`UPDATE subscriptions SET expired_at = ? WHERE endpoint = ?`, old, "https://push/stale")
	st.expireSubscription("https://push/recent")

	if err := st.purgeExpiredSubscriptions(now.Add(-expiredSubscriptionTTL).Unix()); err != nil {
		t.Fatalf("purgeExpiredSubscriptions: %v", err)
	}
	devs, _ := st.listDevices()
	if len(devs) != 2 {
		t.Fatalf("got %d devices after purge, want 2 (stale removed)", len(devs))
	}
	for _, d := range devs {
		if d.Endpoint == "https://push/stale" {
			t.Fatal("long-expired device survived the purge")
		}
	}
}

// TestSubscribeMigratesViaDeviceID drives the same recovery over HTTP: this is
// exactly what the PWA does on the launch after iOS drops its subscription.
func TestSubscribeMigratesViaDeviceID(t *testing.T) {
	s := newTestApp(t)
	post := func(endpoint string) {
		t.Helper()
		body := `{"endpoint":"` + endpoint + `","keys":{"p256dh":"abc","auth":"def"},"device_id":"dev-1"}`
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/subscribe", strings.NewReader(body)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("subscribe %s: status %d, want 204", endpoint, rec.Code)
		}
	}
	post("https://push/old")
	s.store.joinRoom("alerts", "https://push/old", nil)
	post("https://push/new")

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/rooms?endpoint=https://push/new", nil))
	if !strings.Contains(rec.Body.String(), "alerts") {
		t.Fatalf("rooms for new endpoint = %s, want alerts", rec.Body.String())
	}
	if n, _ := s.store.countSubscriptions(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

// The service worker's pushsubscriptionchange path has no access to the page's
// device id, so it reports the endpoint it is replacing instead.
func TestSubscribeMigratesViaOldEndpoint(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/old"), "")
	s.store.joinRoom("alerts", "https://push/old", nil)

	body := `{"endpoint":"https://push/new","keys":{"p256dh":"abc","auth":"def"},"old_endpoint":"https://push/old"}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/subscribe", strings.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := roomNames(t, s.store, "https://push/new"); len(got) != 1 || got[0] != "alerts" {
		t.Fatalf("rooms = %v, want [alerts]", got)
	}
	if n, _ := s.store.countSubscriptions(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

// A 410 over the real send path expires the device instead of deleting it, so
// the memberships are still there when the user re-enables notifications.
func TestBroadcastExpiresRatherThanDeletes(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkDeviceSub("https://push/gone", "dev-1"), "")
	s.store.joinRoom("alerts", "https://push/gone", nil)

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	sendOne = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		return stubResp(http.StatusGone), nil
	}
	res, err := s.broadcastRoom("alerts", "", pushPayload{Title: "hi"})
	if err != nil {
		t.Fatalf("broadcastRoom: %v", err)
	}
	if res.Pruned != 1 {
		t.Fatalf("pruned = %d, want 1", res.Pruned)
	}
	if got := roomNames(t, s.store, "https://push/gone"); len(got) != 1 {
		t.Fatalf("410 destroyed the membership: %v", got)
	}
	// The user re-enables: new endpoint, same device, rooms restored.
	s.store.upsertSubscription(mkDeviceSub("https://push/fresh", "dev-1"), "")
	subs, _ := s.store.roomRecipients("alerts", "")
	if len(subs) != 1 || subs[0].Endpoint != "https://push/fresh" {
		t.Fatalf("recipients after re-enable = %+v", subs)
	}
}
