package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func newTestApp(t *testing.T) *Server {
	t.Helper()
	s := &Server{store: newTestStore(t), subscriber: "mailto:test@localhost", limiter: newRateLimiter(5, 1), sessions: newSessionStore(time.Hour)}
	if err := s.initSecrets(); err != nil {
		t.Fatalf("initSecrets: %v", err)
	}
	return s
}

func stubResp(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}
}

func TestBroadcastCountsAndPrunes(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/ok"), "")
	s.store.upsertSubscription(mkSub("https://push/gone"), "")
	s.store.upsertSubscription(mkSub("https://push/err"), "")

	// Restore the real sender after the test.
	orig := sendOne
	t.Cleanup(func() { sendOne = orig })

	sendOne = func(_ []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		switch {
		case strings.HasSuffix(sub.Endpoint, "/ok"):
			return stubResp(201), nil
		case strings.HasSuffix(sub.Endpoint, "/gone"):
			return stubResp(http.StatusGone), nil
		default:
			return stubResp(500), nil
		}
	}

	res, err := s.broadcast(pushPayload{Title: "hi", Body: "there"})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if res.Sent != 1 || res.Pruned != 1 || res.Failed != 1 {
		t.Fatalf("got %+v, want sent=1 pruned=1 failed=1", res)
	}

	// The 410 endpoint should have been removed from the store.
	if n, _ := s.store.countSubscriptions(); n != 2 {
		t.Fatalf("count after prune = %d, want 2", n)
	}
}

func TestBroadcastSetsUrgency(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/a"), "")

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var gotUrgency webpush.Urgency
	sendOne = func(_ []byte, _ *webpush.Subscription, opts *webpush.Options) (*http.Response, error) {
		gotUrgency = opts.Urgency
		return stubResp(201), nil
	}

	if _, err := s.broadcast(pushPayload{Title: "hi", Urgency: "high"}); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if gotUrgency != webpush.UrgencyHigh {
		t.Fatalf("urgency = %q, want high", gotUrgency)
	}
}
