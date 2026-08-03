package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestJoinLeaveAndDeviceRooms(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkSub("https://push/a"), "")

	// Join without a secret.
	if err := st.joinRoom("alerts", "https://push/a", nil); err != nil {
		t.Fatalf("joinRoom: %v", err)
	}
	rooms, err := st.deviceRooms("https://push/a")
	if err != nil {
		t.Fatalf("deviceRooms: %v", err)
	}
	if len(rooms) != 1 || rooms[0].Room != "alerts" || rooms[0].HasSecret {
		t.Fatalf("got %+v, want one room 'alerts' with HasSecret=false", rooms)
	}

	// Set a secret on the existing membership.
	sec := "hunter2"
	if err := st.joinRoom("alerts", "https://push/a", &sec); err != nil {
		t.Fatalf("joinRoom set secret: %v", err)
	}
	rooms, _ = st.deviceRooms("https://push/a")
	if !rooms[0].HasSecret {
		t.Fatalf("expected HasSecret=true after setting secret")
	}

	// nil secret must NOT clear the existing secret.
	st.joinRoom("alerts", "https://push/a", nil)
	rooms, _ = st.deviceRooms("https://push/a")
	if !rooms[0].HasSecret {
		t.Fatalf("nil secret cleared the secret; want preserved")
	}

	// Empty string clears it.
	empty := ""
	st.joinRoom("alerts", "https://push/a", &empty)
	rooms, _ = st.deviceRooms("https://push/a")
	if rooms[0].HasSecret {
		t.Fatalf("empty secret did not clear; want HasSecret=false")
	}

	// Leave.
	if err := st.leaveRoom("alerts", "https://push/a"); err != nil {
		t.Fatalf("leaveRoom: %v", err)
	}
	rooms, _ = st.deviceRooms("https://push/a")
	if len(rooms) != 0 {
		t.Fatalf("got %d rooms after leave, want 0", len(rooms))
	}
}

func TestRoomRecipientsFilterBySecret(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkSub("https://push/x"), "")
	st.upsertSubscription(mkSub("https://push/y"), "")
	st.upsertSubscription(mkSub("https://push/z"), "")
	sx, sy := "s1", "s1"
	st.joinRoom("r", "https://push/x", &sx)
	st.joinRoom("r", "https://push/y", &sy)
	st.joinRoom("r", "https://push/z", nil) // no secret

	// Posted secret "s1" reaches x and y.
	got, _ := st.roomRecipients("r", secretHash("s1"))
	if len(got) != 2 {
		t.Fatalf("secret match: got %d recipients, want 2", len(got))
	}
	// No secret reaches only z.
	got, _ = st.roomRecipients("r", "")
	if len(got) != 1 || got[0].Endpoint != "https://push/z" {
		t.Fatalf("empty secret: got %+v, want only z", got)
	}
	// Non-matching secret reaches nobody.
	got, _ = st.roomRecipients("r", secretHash("nope"))
	if len(got) != 0 {
		t.Fatalf("bad secret: got %d, want 0", len(got))
	}
}

func TestEndpointCascadeRemovesMembership(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkSub("https://push/c"), "")
	st.joinRoom("r", "https://push/c", nil)
	if err := st.deleteSubscription("https://push/c"); err != nil {
		t.Fatalf("deleteSubscription: %v", err)
	}
	rooms, _ := st.deviceRooms("https://push/c")
	if len(rooms) != 0 {
		t.Fatalf("cascade failed: got %d memberships, want 0", len(rooms))
	}
}

func TestLogNotificationTrimsAndFiltersPerDevice(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkSub("https://push/d"), "")
	sec := "k"
	st.joinRoom("r", "https://push/d", &sec) // device's secret is "k"

	// A post that matches the device's secret.
	st.logNotification("r", secretHash("k"), "Match", "body", "", 1, 0)
	// A post with no secret (device should NOT see it).
	st.logNotification("r", "", "NoSecret", "body", "", 0, 0)

	got, err := st.deviceRoomLog("https://push/d", "r")
	if err != nil {
		t.Fatalf("deviceRoomLog: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Match" || !got[0].HadSecret {
		t.Fatalf("got %+v, want only the matching 'Match' post", got)
	}

	// Admin sees both.
	all, _ := st.adminRoomLog("r")
	if len(all) != 2 {
		t.Fatalf("adminRoomLog got %d, want 2", len(all))
	}
}

func TestLogCapsAt200(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 205; i++ {
		st.logNotification("r", "", "t", "b", "", 0, 0)
	}
	all, _ := st.adminRoomLog("r")
	if len(all) != 200 {
		t.Fatalf("log length = %d, want 200 (capped)", len(all))
	}
}

func TestListRoomsCounts(t *testing.T) {
	st := newTestStore(t)
	st.upsertSubscription(mkSub("https://push/1"), "")
	st.upsertSubscription(mkSub("https://push/2"), "")
	st.joinRoom("alpha", "https://push/1", nil)
	st.joinRoom("alpha", "https://push/2", nil)
	st.upsertRoom("empty")

	rooms, err := st.listRooms()
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	counts := map[string]int{}
	for _, r := range rooms {
		counts[r.Room] = r.Subscribers
	}
	if counts["alpha"] != 2 || counts["empty"] != 0 {
		t.Fatalf("counts = %+v, want alpha=2 empty=0", counts)
	}
}

func TestBroadcastRoomFiltersAndLogs(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/m"), "")
	s.store.upsertSubscription(mkSub("https://push/n"), "")
	sm := "sec"
	s.store.joinRoom("r", "https://push/m", &sm) // secret "sec"
	s.store.joinRoom("r", "https://push/n", nil) // no secret

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var called int
	sendOne = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		called++
		return stubResp(201), nil
	}

	res, err := s.broadcastRoom("r", "sec", pushPayload{Title: "Hi", Body: "b"})
	if err != nil {
		t.Fatalf("broadcastRoom: %v", err)
	}
	if called != 1 || res.Sent != 1 || res.Recipients != 1 {
		t.Fatalf("got called=%d %+v, want 1 send to the matching device", called, res)
	}
	// Logged once for the matching secret; the matching device can read it.
	posts, _ := s.store.deviceRoomLog("https://push/m", "r")
	if len(posts) != 1 || posts[0].Title != "Hi" {
		t.Fatalf("deviceRoomLog = %+v, want the logged post", posts)
	}
}

func TestRoomPostNoRecipientLeavesNoTrace(t *testing.T) {
	s := newTestApp(t)
	// A real room "known" with one member whose secret is "right".
	s.store.upsertSubscription(mkSub("https://push/known"), "")
	sec := "right"
	s.store.joinRoom("known", "https://push/known", &sec)

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var called int
	sendOne = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		called++
		return stubResp(201), nil
	}

	// (a) Post to a brand-new name with no members -> 200 sent:0, and the room is NOT created.
	req := httptest.NewRequest("POST", "/n/ghost", strings.NewReader("hi"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ghost post status = %d, want 200", rec.Code)
	}
	rooms, _ := s.store.listRooms()
	for _, r := range rooms {
		if r.Room == "ghost" {
			t.Fatalf("posting minted an empty room %q", r.Room)
		}
	}

	// (b) Wrong secret to the known room -> 200 sent:0, and NO log row written (no poisoning).
	req = httptest.NewRequest("POST", "/n/known?secret=wrong", strings.NewReader("spam"))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wrong-secret post status = %d, want 200", rec.Code)
	}
	posts, _ := s.store.adminRoomLog("known")
	if len(posts) != 0 {
		t.Fatalf("wrong-secret post wrote %d log rows, want 0 (no poisoning)", len(posts))
	}
	if called != 0 {
		t.Fatalf("wrong-secret post called sendOne %d times, want 0", called)
	}

	// (c) Right secret DOES deliver and log.
	req = httptest.NewRequest("POST", "/n/known?secret=right", strings.NewReader("real"))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("right-secret post status = %d, want 200", rec.Code)
	}
	if called != 1 {
		t.Fatalf("right-secret post called sendOne %d times, want 1", called)
	}
	posts, _ = s.store.adminRoomLog("known")
	if len(posts) != 1 {
		t.Fatalf("right-secret post wrote %d log rows, want 1", len(posts))
	}
}

func TestRoomPostPlaintextAndJSON(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/p"), "")
	s.store.joinRoom("alerts", "https://push/p", nil) // no secret

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var lastMsg string
	sendOne = func(msg []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		lastMsg = string(msg)
		return stubResp(201), nil
	}

	// Plaintext: body becomes the notification body, title defaults to room.
	req := httptest.NewRequest("POST", "/n/alerts", strings.NewReader("hello world"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("plaintext status = %d, want 200", rec.Code)
	}
	if !strings.Contains(lastMsg, `"title":"alerts"`) || !strings.Contains(lastMsg, `"body":"hello world"`) {
		t.Fatalf("plaintext payload = %s", lastMsg)
	}

	// JSON: full structured payload.
	req = httptest.NewRequest("POST", "/n/alerts", strings.NewReader(`{"title":"T","body":"B","url":"/x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("json status = %d, want 200", rec.Code)
	}
	if !strings.Contains(lastMsg, `"title":"T"`) || !strings.Contains(lastMsg, `"url":"/x"`) {
		t.Fatalf("json payload = %s", lastMsg)
	}
}

func TestRoomPostSecretTransport(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/q"), "")
	sec := "letmein"
	s.store.joinRoom("r", "https://push/q", &sec)

	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var called int
	sendOne = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		called++
		return stubResp(201), nil
	}

	// Header secret matches -> delivered.
	req := httptest.NewRequest("POST", "/n/r", strings.NewReader("hi"))
	req.Header.Set("X-Room-Secret", "letmein")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if called != 1 {
		t.Fatalf("header secret: called=%d, want 1", called)
	}

	// Query secret matches -> delivered.
	called = 0
	req = httptest.NewRequest("POST", "/n/r?secret=letmein", strings.NewReader("hi"))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if called != 1 {
		t.Fatalf("query secret: called=%d, want 1", called)
	}

	// Wrong secret -> nobody, still 200 sent:0.
	called = 0
	req = httptest.NewRequest("POST", "/n/r?secret=wrong", strings.NewReader("hi"))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || called != 0 {
		t.Fatalf("wrong secret: code=%d called=%d, want 200 & 0", rec.Code, called)
	}
}

func TestRoomPostValidationAndRateLimit(t *testing.T) {
	s := newTestApp(t)

	// Invalid room name -> 400.
	req := httptest.NewRequest("POST", "/n/bad%20name", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name status = %d, want 400", rec.Code)
	}

	// Empty plaintext body -> 400.
	req = httptest.NewRequest("POST", "/n/ok", strings.NewReader(""))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400", rec.Code)
	}

	// Rate limit: force a burst-1 limiter, second request is 429.
	s.postLimiter = newRateLimiter(1, 0)
	req = httptest.NewRequest("POST", "/n/ok", strings.NewReader("a"))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	req = httptest.NewRequest("POST", "/n/ok", strings.NewReader("b"))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second post status = %d, want 429", rec.Code)
	}
}

func TestSelfServiceRoomLifecycle(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/self"), "")
	ep := "https://push/self"

	// Join with a secret.
	join := `{"endpoint":"` + ep + `","room":"alerts","secret":"s"}`
	req := httptest.NewRequest("POST", "/api/rooms", strings.NewReader(join))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("join status = %d, want 204", rec.Code)
	}

	// List shows the room with has_secret true.
	req = httptest.NewRequest("GET", "/api/rooms?endpoint="+ep, nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"room":"alerts"`) || !strings.Contains(rec.Body.String(), `"has_secret":true`) {
		t.Fatalf("list body = %s", rec.Body.String())
	}

	// Leave.
	req = httptest.NewRequest("DELETE", "/api/rooms", strings.NewReader(`{"endpoint":"`+ep+`","room":"alerts"}`))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("leave status = %d, want 204", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/rooms?endpoint="+ep, nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "alerts") {
		t.Fatalf("still a member after leave: %s", rec.Body.String())
	}
}

func TestSelfServiceRejectsUnknownEndpoint(t *testing.T) {
	s := newTestApp(t)
	req := httptest.NewRequest("POST", "/api/rooms",
		strings.NewReader(`{"endpoint":"https://push/nope","room":"r"}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown endpoint status = %d, want 400", rec.Code)
	}
}

func TestSelfServiceRoomLog(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/log"), "")
	ep := "https://push/log"
	sec := "secret123"

	// Device joins room with a secret.
	s.store.joinRoom("r", ep, &sec)

	// Log a matching post (secret matches device's secret).
	s.store.logNotification("r", secretHash("secret123"), "Hi", "body text", "", 1, 0)

	// GET /api/rooms/log with matching endpoint and room returns 200 and includes the post.
	req := httptest.NewRequest("GET", "/api/rooms/log?endpoint="+ep+"&room=r", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("log status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"title":"Hi"`) {
		t.Fatalf("log body missing title 'Hi': %s", rec.Body.String())
	}

	// Missing endpoint → 400.
	req = httptest.NewRequest("GET", "/api/rooms/log?room=r", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing endpoint status = %d, want 400", rec.Code)
	}

	// Invalid room name → 400.
	req = httptest.NewRequest("GET", "/api/rooms/log?endpoint="+ep+"&room=bad%20name", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid room status = %d, want 400", rec.Code)
	}

	// Member room with no matching posts returns 200 with "[]" (not "null").
	s.store.joinRoom("empty_room", ep, nil)
	req = httptest.NewRequest("GET", "/api/rooms/log?endpoint="+ep+"&room=empty_room", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty log status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "[]\n" {
		t.Fatalf("empty log body = %s, want []\n", rec.Body.String())
	}
}

func TestAdminRoomsRequireAuthAndReturnData(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/adm"), "")
	s.store.joinRoom("ops", "https://push/adm", nil)
	s.store.logNotification("ops", "", "Deploy", "done", "", 1, 0)

	// Unauthenticated -> 401.
	req := httptest.NewRequest("GET", "/api/admin/rooms", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d, want 401", rec.Code)
	}

	// Authenticated list.
	req = httptest.NewRequest("GET", "/api/admin/rooms", nil)
	req.Header.Set("Authorization", "Bearer "+s.InitialToken())
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"room":"ops"`) ||
		!strings.Contains(rec.Body.String(), `"subscribers":1`) {
		t.Fatalf("admin list = %d %s", rec.Code, rec.Body.String())
	}

	// Authenticated log.
	req = httptest.NewRequest("GET", "/api/admin/rooms/log?room=ops", nil)
	req.Header.Set("Authorization", "Bearer "+s.InitialToken())
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"Deploy"`) {
		t.Fatalf("admin log = %d %s", rec.Code, rec.Body.String())
	}
}
