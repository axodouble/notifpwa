package server

import (
	"testing"
	"time"
)

func TestSessionCreateValidateExpire(t *testing.T) {
	ss := newSessionStore(time.Hour)
	now := time.Unix(1000, 0)

	id := ss.create(now)
	if id == "" {
		t.Fatal("create returned empty id")
	}
	if !ss.valid(id, now) {
		t.Fatal("fresh session should be valid")
	}
	if ss.valid("nope", now) {
		t.Fatal("unknown id should be invalid")
	}
	if ss.valid(id, now.Add(2*time.Hour)) {
		t.Fatal("expired session should be invalid")
	}
	ss.delete(id)
	if ss.valid(id, now) {
		t.Fatal("deleted session should be invalid")
	}
}
