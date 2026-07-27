package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
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

func TestSendRequiresToken(t *testing.T) {
	s := newTestApp(t)

	// No token.
	req := httptest.NewRequest("POST", "/api/send", strings.NewReader(`{"title":"hi"}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}

	// Wrong token.
	req = httptest.NewRequest("POST", "/api/send", strings.NewReader(`{"title":"hi"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", rec.Code)
	}
}

func TestSendWithValidTokenBroadcasts(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/a"), "")

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	called := 0
	sendOne = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		called++
		return stubResp(201), nil
	}

	req := httptest.NewRequest("POST", "/api/send", strings.NewReader(`{"title":"hi","body":"yo"}`))
	req.Header.Set("Authorization", "Bearer "+s.getToken())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if called != 1 {
		t.Fatalf("sendOne called %d times, want 1", called)
	}
	var res sendResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Sent != 1 {
		t.Fatalf("sent = %d, want 1", res.Sent)
	}
}

func TestSendCapsActionsToTwo(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/a"), "")

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var payload pushPayload
	sendOne = func(msg []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		json.Unmarshal(msg, &payload)
		return stubResp(201), nil
	}

	body := `{"title":"hi","actions":[{"title":"a","url":"/a"},{"title":"b","url":"/b"},{"title":"c","url":"/c"}]}`
	req := httptest.NewRequest("POST", "/api/send", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d, want 200", rec.Code)
	}
	if len(payload.Actions) != 2 {
		t.Fatalf("actions len = %d, want 2 (capped)", len(payload.Actions))
	}
}

func TestDeviceEndpoints(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/a"), "UA-A")
	bearer := "Bearer " + s.getToken()

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
	req := httptest.NewRequest("GET", "/admin?token="+s.getToken(), nil)
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

func TestRotateTokenChangesAcceptedToken(t *testing.T) {
	s := newTestApp(t)
	old := s.getToken()

	req := httptest.NewRequest("POST", "/api/rotate-token", nil)
	req.Header.Set("Authorization", "Bearer "+old)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d, want 200", rec.Code)
	}
	var out struct{ Token string `json:"token"` }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Token == "" || out.Token == old {
		t.Fatalf("token = %q, want a new non-empty value", out.Token)
	}
	if s.getToken() != out.Token {
		t.Fatalf("in-memory token not updated")
	}

	// Old token is now rejected on a protected endpoint.
	req = httptest.NewRequest("POST", "/api/send", strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Authorization", "Bearer "+old)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token still accepted: %d, want 401", rec.Code)
	}
}
