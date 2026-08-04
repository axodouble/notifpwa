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
	s := &Server{store: newTestStore(t), subscriber: "mailto:test@localhost", limiter: newRateLimiter(5, 1), postLimiter: newRateLimiter(20, 5), sessions: newSessionStore(time.Hour)}
	if err := s.initSecrets(); err != nil {
		t.Fatalf("initSecrets: %v", err)
	}
	return s
}

func stubResp(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}
}

func TestBroadcastRoomCountsPrunesAndUrgency(t *testing.T) {
	s := newTestApp(t)
	for _, ep := range []string{"https://push/ok", "https://push/gone", "https://push/err"} {
		s.store.upsertSubscription(mkSub(ep), "")
		if err := s.store.joinRoom("r", ep, nil); err != nil { // no secret
			t.Fatalf("joinRoom: %v", err)
		}
	}
	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var gotUrgency webpush.Urgency
	sendOne = func(_ []byte, sub *webpush.Subscription, opts *webpush.Options) (*http.Response, error) {
		gotUrgency = opts.Urgency
		switch {
		case strings.HasSuffix(sub.Endpoint, "/ok"):
			return stubResp(201), nil
		case strings.HasSuffix(sub.Endpoint, "/gone"):
			return stubResp(http.StatusGone), nil
		default:
			return stubResp(500), nil
		}
	}
	res, err := s.broadcastRoom("r", "", pushPayload{Title: "hi", Urgency: "high"})
	if err != nil {
		t.Fatalf("broadcastRoom: %v", err)
	}
	if res.Sent != 1 || res.Pruned != 1 || res.Failed != 1 || res.Recipients != 3 {
		t.Fatalf("got %+v, want sent=1 pruned=1 failed=1 recipients=3", res)
	}
	if gotUrgency != webpush.Urgency("high") {
		t.Fatalf("urgency = %q, want high", gotUrgency)
	}
	if n, _ := s.store.countSubscriptions(); n != 2 {
		t.Fatalf("count after prune = %d, want 2", n)
	}
}
