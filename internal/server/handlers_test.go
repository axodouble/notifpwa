package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubscribeStoresDevice(t *testing.T) {
	s := newTestApp(t)
	body := `{"endpoint":"https://push/x","keys":{"p256dh":"abc","auth":"def"}}`
	req := httptest.NewRequest("POST", "/api/subscribe", strings.NewReader(body))
	rec := httptest.NewRecorder()

	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n, _ := s.store.countSubscriptions(); n != 1 {
		t.Fatalf("stored %d subscriptions, want 1", n)
	}
}

func TestSubscribeRejectsIncomplete(t *testing.T) {
	s := newTestApp(t)
	req := httptest.NewRequest("POST", "/api/subscribe", strings.NewReader(`{"endpoint":"https://push/x"}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDeviceEndpoints(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/a"), "UA-A")
	bearer := "Bearer " + s.InitialToken()

	// List requires auth.
	req := httptest.NewRequest("GET", "/api/devices", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list = %d, want 401", rec.Code)
	}

	// List returns the device.
	req = httptest.NewRequest("GET", "/api/devices", nil)
	req.Header.Set("Authorization", bearer)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	var devs []device
	json.Unmarshal(rec.Body.Bytes(), &devs)
	if len(devs) != 1 || devs[0].UserAgent != "UA-A" {
		t.Fatalf("list body = %+v", devs)
	}

	// Label it.
	req = httptest.NewRequest("POST", "/api/devices/label", strings.NewReader(`{"endpoint":"https://push/a","label":"Phone"}`))
	req.Header.Set("Authorization", bearer)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("label = %d, want 204", rec.Code)
	}

	// Delete it.
	req = httptest.NewRequest("DELETE", "/api/devices", strings.NewReader(`{"endpoint":"https://push/a"}`))
	req.Header.Set("Authorization", bearer)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
	if n, _ := s.store.countSubscriptions(); n != 0 {
		t.Fatalf("count after delete = %d, want 0", n)
	}
}

func TestSubscribeRateLimited(t *testing.T) {
	s := newTestApp(t)
	s.limiter = newRateLimiter(1, 0) // burst 1, no refill

	body := `{"endpoint":"https://push/x","keys":{"p256dh":"abc","auth":"def"}}`
	do := func() int {
		req := httptest.NewRequest("POST", "/api/subscribe", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.7:5555"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do(); code != http.StatusNoContent {
		t.Fatalf("1st subscribe = %d, want 204", code)
	}
	if code := do(); code != http.StatusTooManyRequests {
		t.Fatalf("2nd subscribe = %d, want 429", code)
	}
}

func TestManifestReflectsAppName(t *testing.T) {
	s := newTestApp(t)
	s.store.setSetting("app_name", []byte("My Alerts"))

	req := httptest.NewRequest("GET", "/manifest.webmanifest", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest not JSON: %v", err)
	}
	if m["name"] != "My Alerts" {
		t.Fatalf("name = %v, want My Alerts", m["name"])
	}
}

func TestAdminSetsAndAcceptsCookie(t *testing.T) {
	s := newTestApp(t)

	// Valid ?token= issues a session cookie.
	req := httptest.NewRequest("GET", "/admin?token="+s.InitialToken(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token visit = %d, want 200", rec.Code)
	}
	cookies := rec.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == adminCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no admin cookie set")
	}

	// The cookie alone (no ?token=) is accepted.
	req = httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie visit = %d, want 200", rec.Code)
	}

	// No token, no cookie -> 401.
	req = httptest.NewRequest("GET", "/admin", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous visit = %d, want 401", rec.Code)
	}
}

func TestAdminReMintsExpiredCookie(t *testing.T) {
	s := newTestApp(t)

	// A cookie value that was never issued by the session store is a clean
	// "present but invalid" case.
	req := httptest.NewRequest("GET", "/admin?token="+s.InitialToken(), nil)
	req.AddCookie(&http.Cookie{Name: adminCookie, Value: "stale-never-issued"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var fresh *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminCookie {
			fresh = c
		}
	}
	if fresh == nil {
		t.Fatal("no admin cookie set for stale cookie + valid token")
	}
	if fresh.Value == "" || fresh.Value == "stale-never-issued" {
		t.Fatalf("cookie value = %q, want a fresh non-empty session id", fresh.Value)
	}
	if !s.sessions.valid(fresh.Value, time.Now()) {
		t.Fatal("newly minted cookie should be a valid session")
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	s := newTestApp(t)
	id := s.sessions.create(time.Now())
	req := httptest.NewRequest("POST", "/admin/logout", nil)
	req.AddCookie(&http.Cookie{Name: adminCookie, Value: id})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if s.sessions.valid(id, time.Now()) {
		t.Fatal("session should be invalid after logout")
	}
}

func TestHealthzOK(t *testing.T) {
	s := newTestApp(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("body = %q, want ok", rec.Body.String())
	}
}

func TestTokenCRUDEndpoints(t *testing.T) {
	s := newTestApp(t)
	admin := "Bearer " + s.InitialToken()

	// Create a token.
	req := httptest.NewRequest("POST", "/api/tokens", strings.NewReader(`{"label":"CI"}`))
	req.Header.Set("Authorization", admin)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200", rec.Code)
	}
	var created struct {
		ID, Secret, Prefix, Label string
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" || len(created.Secret) != 64 || created.Prefix == "" || created.Label != "CI" {
		t.Fatalf("created = %+v", created)
	}

	// The new secret is an admin credential and can reach an admin endpoint.
	req = httptest.NewRequest("GET", "/api/devices", nil)
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new token on admin endpoint = %d, want 200", rec.Code)
	}

	// List shows two tokens (bootstrap + new), never a secret.
	req = httptest.NewRequest("GET", "/api/tokens", nil)
	req.Header.Set("Authorization", admin)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), created.Secret) {
		t.Fatal("list leaked a secret")
	}
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}

	// Delete the new token.
	req = httptest.NewRequest("DELETE", "/api/tokens/"+created.ID, nil)
	req.Header.Set("Authorization", admin)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
}

func TestTokenDeleteGuardsLastToken(t *testing.T) {
	s := newTestApp(t) // no root token; the bootstrap token is the only one
	bearer := "Bearer " + s.InitialToken()

	// Find the sole token's id.
	req := httptest.NewRequest("GET", "/api/tokens", nil)
	req.Header.Set("Authorization", bearer)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var toks []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &toks)
	if len(toks) != 1 {
		t.Fatalf("want 1 token, got %d", len(toks))
	}
	lastID := toks[0]["id"].(string)

	// Deleting the last token is refused (409).
	req = httptest.NewRequest("DELETE", "/api/tokens/"+lastID, nil)
	req.Header.Set("Authorization", bearer)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete last token: status = %d, want 409", rec.Code)
	}

	// Create a second token, then deleting the first is allowed.
	req = httptest.NewRequest("POST", "/api/tokens", strings.NewReader(`{"label":"second"}`))
	req.Header.Set("Authorization", bearer)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create second token: status = %d", rec.Code)
	}
	req = httptest.NewRequest("DELETE", "/api/tokens/"+lastID, nil)
	req.Header.Set("Authorization", bearer)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete with another token present: status = %d, want 204", rec.Code)
	}
}

func TestAdminPageHasTokensUIAndNoInjectedToken(t *testing.T) {
	s := newTestApp(t)
	req := httptest.NewRequest("GET", "/admin?token="+s.InitialToken(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "New token") {
		t.Fatal("admin page missing the Tokens UI")
	}
	if strings.Contains(body, "window.TOKEN") {
		t.Fatal("admin page still injects window.TOKEN")
	}
}

func TestTokenUpdateDeleteMissingID(t *testing.T) {
	s := newTestApp(t)
	admin := "Bearer " + s.InitialToken()

	// PATCH a nonexistent token returns 404.
	req := httptest.NewRequest("PATCH", "/api/tokens/does-not-exist", strings.NewReader(`{"label":"x"}`))
	req.Header.Set("Authorization", admin)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch missing = %d, want 404", rec.Code)
	}

	// DELETE a nonexistent token returns 404.
	req = httptest.NewRequest("DELETE", "/api/tokens/does-not-exist", nil)
	req.Header.Set("Authorization", admin)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", rec.Code)
	}
}
