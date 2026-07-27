package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	s.store.upsertSubscription(mkSub("https://push/a"))

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	called := 0
	sendOne = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		called++
		return stubResp(201), nil
	}

	req := httptest.NewRequest("POST", "/api/send", strings.NewReader(`{"title":"hi","body":"yo"}`))
	req.Header.Set("Authorization", "Bearer "+s.token)
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
