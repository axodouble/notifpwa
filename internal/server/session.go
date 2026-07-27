package server

import (
	"sync"
	"time"
)

// sessionStore holds admin browser sessions in memory. Sessions are decoupled
// from the API token, so rotating the token does not log the admin out.
// Everything resets on restart — fine for a single self-hosted binary.
type sessionStore struct {
	mu  sync.Mutex
	ids map[string]time.Time // session id -> creation time
	ttl time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{ids: make(map[string]time.Time), ttl: ttl}
}

func (ss *sessionStore) create(now time.Time) string {
	id := randomHex(32)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.ids[id] = now
	return id
}

// valid reports whether id is a live session at now, evicting it if expired.
func (ss *sessionStore) valid(id string, now time.Time) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	created, ok := ss.ids[id]
	if !ok {
		return false
	}
	if now.Sub(created) > ss.ttl {
		delete(ss.ids, id)
		return false
	}
	return true
}

func (ss *sessionStore) delete(id string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.ids, id)
}
