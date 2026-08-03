package server

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// secretHash returns the stored hash for a per-subscriber secret. An empty
// secret hashes to the empty string, which represents "no secret".
func secretHash(s string) string {
	if s == "" {
		return ""
	}
	return hashToken(s)
}

// deviceRoom is one room a device belongs to, plus whether it set a secret.
type deviceRoom struct {
	Room      string `json:"room"`
	HasSecret bool   `json:"has_secret"`
}

// upsertRoom records a room name (idempotent). Rooms are created implicitly.
func (s *store) upsertRoom(name string) error {
	_, err := s.db.Exec(
		`INSERT INTO rooms (name, created_at) VALUES (?, ?) ON CONFLICT(name) DO NOTHING`,
		name, time.Now().Unix())
	return err
}

func (s *store) endpointExists(endpoint string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM subscriptions WHERE endpoint = ?`, endpoint).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// joinRoom adds endpoint to room (idempotent) and optionally sets its secret.
// secret == nil leaves an existing membership's secret untouched; a non-nil
// secret sets it (empty string clears it). The room is created if new.
func (s *store) joinRoom(room, endpoint string, secret *string) error {
	if err := s.upsertRoom(room); err != nil {
		return err
	}
	now := time.Now().Unix()
	if secret == nil {
		_, err := s.db.Exec(`
			INSERT INTO room_subscriptions (room, endpoint, secret_hash, created_at)
			VALUES (?, ?, '', ?) ON CONFLICT(room, endpoint) DO NOTHING`,
			room, endpoint, now)
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO room_subscriptions (room, endpoint, secret_hash, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(room, endpoint) DO UPDATE SET secret_hash = excluded.secret_hash`,
		room, endpoint, secretHash(*secret), now)
	return err
}

func (s *store) leaveRoom(room, endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM room_subscriptions WHERE room = ? AND endpoint = ?`, room, endpoint)
	return err
}

func (s *store) deviceRooms(endpoint string) ([]deviceRoom, error) {
	rows, err := s.db.Query(
		`SELECT room, secret_hash FROM room_subscriptions WHERE endpoint = ? ORDER BY room`, endpoint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deviceRoom
	for rows.Next() {
		var d deviceRoom
		var sh string
		if err := rows.Scan(&d.Room, &sh); err != nil {
			return nil, err
		}
		d.HasSecret = sh != ""
		out = append(out, d)
	}
	return out, rows.Err()
}

// roomRecipients returns the push subscriptions in room whose secret_hash
// exactly matches the given hash (use secretHash("") for the no-secret set).
func (s *store) roomRecipients(room, secretHash string) ([]subscription, error) {
	rows, err := s.db.Query(`
		SELECT s.endpoint, s.p256dh, s.auth
		FROM room_subscriptions rs
		JOIN subscriptions s ON s.endpoint = rs.endpoint
		WHERE rs.room = ? AND rs.secret_hash = ?`, room, secretHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subs []subscription
	for rows.Next() {
		var sub subscription
		if err := rows.Scan(&sub.Endpoint, &sub.Keys.P256dh, &sub.Keys.Auth); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// roomPost is one logged notification. HadSecret indicates the post carried a
// secret (its hash is never exposed).
type roomPost struct {
	Room      string `json:"room"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	Sent      int    `json:"sent"`
	Failed    int    `json:"failed"`
	HadSecret bool   `json:"had_secret"`
	CreatedAt int64  `json:"created_at"`
}

// roomInfo is a room and its subscriber count (admin view).
type roomInfo struct {
	Room        string `json:"room"`
	Subscribers int    `json:"subscribers"`
}

// logNotification records a post to a room and trims the room's log to the most
// recent 200 rows.
func (s *store) logNotification(room, secretHash, title, body, url string, sent, failed int) error {
	if _, err := s.db.Exec(`
		INSERT INTO notification_log (room, secret_hash, title, body, url, sent, failed, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		room, secretHash, title, body, url, sent, failed, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		DELETE FROM notification_log
		WHERE room = ? AND id NOT IN (
			SELECT id FROM notification_log WHERE room = ? ORDER BY id DESC LIMIT 200)`,
		room, room)
	return err
}

func scanRoomPosts(rows *sql.Rows) ([]roomPost, error) {
	defer rows.Close()
	var out []roomPost
	for rows.Next() {
		var p roomPost
		var sh string
		if err := rows.Scan(&p.Room, &p.Title, &p.Body, &p.URL, &p.Sent, &p.Failed, &sh, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.HadSecret = sh != ""
		out = append(out, p)
	}
	return out, rows.Err()
}

// deviceRoomLog returns the posts a device received in a room: log rows whose
// secret_hash equals the device's secret_hash for that room. Empty if the
// device is not a member.
func (s *store) deviceRoomLog(endpoint, room string) ([]roomPost, error) {
	var sh string
	err := s.db.QueryRow(
		`SELECT secret_hash FROM room_subscriptions WHERE room = ? AND endpoint = ?`, room, endpoint).Scan(&sh)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT room, title, body, url, sent, failed, secret_hash, created_at
		FROM notification_log WHERE room = ? AND secret_hash = ? ORDER BY id DESC`, room, sh)
	if err != nil {
		return nil, err
	}
	return scanRoomPosts(rows)
}

func (s *store) adminRoomLog(room string) ([]roomPost, error) {
	rows, err := s.db.Query(`
		SELECT room, title, body, url, sent, failed, secret_hash, created_at
		FROM notification_log WHERE room = ? ORDER BY id DESC`, room)
	if err != nil {
		return nil, err
	}
	return scanRoomPosts(rows)
}

func (s *store) listRooms() ([]roomInfo, error) {
	rows, err := s.db.Query(`
		SELECT r.name, COUNT(rs.endpoint)
		FROM rooms r LEFT JOIN room_subscriptions rs ON rs.room = r.name
		GROUP BY r.name ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []roomInfo
	for rows.Next() {
		var ri roomInfo
		if err := rows.Scan(&ri.Room, &ri.Subscribers); err != nil {
			return nil, err
		}
		out = append(out, ri)
	}
	return out, rows.Err()
}

// roomSendResult is a broadcast summary plus how many devices matched.
type roomSendResult struct {
	sendResult
	Recipients int `json:"recipients"`
}

// broadcastRoom delivers p to the room's devices whose secret matches, and
// logs the post. A zero-recipient post succeeds (sent:0) but leaves no trace:
// no room row and no log row, so anonymous callers cannot mint empty rooms or
// evict a known room's legitimate log history.
func (s *Server) broadcastRoom(room, secret string, p pushPayload) (roomSendResult, error) {
	sh := secretHash(secret)
	subs, err := s.store.roomRecipients(room, sh)
	if err != nil {
		return roomSendResult{}, err
	}
	if len(subs) == 0 {
		// No matching recipient: deliver nothing and leave no trace. This
		// prevents anonymous callers from minting empty rooms or writing
		// log rows that evict a known room's legitimate history.
		return roomSendResult{}, nil
	}
	res, err := s.sendToSubs(subs, p)
	if err != nil {
		return roomSendResult{}, err
	}
	if err := s.store.logNotification(room, sh, p.Title, p.Body, p.URL, res.Sent, res.Failed); err != nil {
		return roomSendResult{}, err
	}
	return roomSendResult{sendResult: res, Recipients: len(subs)}, nil
}

func validRoomName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// roomSecret extracts the posted secret: X-Room-Secret header wins, else ?secret=.
func roomSecret(r *http.Request) string {
	if h := r.Header.Get("X-Room-Secret"); h != "" {
		return h
	}
	return r.URL.Query().Get("secret")
}

// rateLimitPost gates public posting routes by per-IP token bucket.
func (s *Server) rateLimitPost(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.postLimiter.allow(clientIP(r), time.Now()) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// handleRoomPost posts a notification to a room. Plaintext bodies become the
// notification body (title defaults to the room name, overridable via ?title=);
// application/json bodies are a full pushPayload.
func (s *Server) handleRoomPost(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	if !validRoomName(room) {
		http.Error(w, "invalid room name", http.StatusBadRequest)
		return
	}
	secret := roomSecret(r)

	var p pushPayload
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	} else {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
		if err != nil {
			http.Error(w, "could not read body", http.StatusBadRequest)
			return
		}
		p.Body = strings.TrimSpace(string(b))
		p.Title = room
		if t := strings.TrimSpace(r.URL.Query().Get("title")); t != "" {
			p.Title = t
		}
		if p.Body == "" {
			http.Error(w, "empty body", http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(p.Title) == "" && strings.TrimSpace(p.Body) == "" {
		http.Error(w, "title or body required", http.StatusBadRequest)
		return
	}
	if len(p.Actions) > 2 {
		p.Actions = p.Actions[:2]
	}

	res, err := s.broadcastRoom(room, secret, p)
	if err != nil {
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (s *Server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		http.Error(w, "endpoint required", http.StatusBadRequest)
		return
	}
	rooms, err := s.store.deviceRooms(endpoint)
	if err != nil {
		http.Error(w, "could not list rooms", http.StatusInternalServerError)
		return
	}
	if rooms == nil {
		rooms = []deviceRoom{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rooms)
}

func (s *Server) handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string  `json:"endpoint"`
		Room     string  `json:"room"`
		Secret   *string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if body.Endpoint == "" || !validRoomName(body.Room) {
		http.Error(w, "endpoint and valid room required", http.StatusBadRequest)
		return
	}
	exists, err := s.store.endpointExists(body.Endpoint)
	if err != nil {
		http.Error(w, "could not check device", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "unknown device; enable notifications first", http.StatusBadRequest)
		return
	}
	if err := s.store.joinRoom(body.Room, body.Endpoint, body.Secret); err != nil {
		http.Error(w, "could not join room", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
		Room     string `json:"room"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Endpoint == "" || !validRoomName(body.Room) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.store.leaveRoom(body.Room, body.Endpoint); err != nil {
		http.Error(w, "could not leave room", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeviceRoomLog(w http.ResponseWriter, r *http.Request) {
	endpoint := r.URL.Query().Get("endpoint")
	room := r.URL.Query().Get("room")
	if endpoint == "" || !validRoomName(room) {
		http.Error(w, "endpoint and valid room required", http.StatusBadRequest)
		return
	}
	posts, err := s.store.deviceRoomLog(endpoint, room)
	if err != nil {
		http.Error(w, "could not load log", http.StatusInternalServerError)
		return
	}
	if posts == nil {
		posts = []roomPost{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(posts)
}

func (s *Server) handleAdminListRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.store.listRooms()
	if err != nil {
		http.Error(w, "could not list rooms", http.StatusInternalServerError)
		return
	}
	if rooms == nil {
		rooms = []roomInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rooms)
}

func (s *Server) handleAdminRoomLog(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if !validRoomName(room) {
		http.Error(w, "valid room required", http.StatusBadRequest)
		return
	}
	posts, err := s.store.adminRoomLog(room)
	if err != nil {
		http.Error(w, "could not load log", http.StatusInternalServerError)
		return
	}
	if posts == nil {
		posts = []roomPost{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(posts)
}
