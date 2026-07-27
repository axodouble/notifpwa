// Package server implements the notifpwa Web Push application: storage, HTTP
// handlers, and push delivery. Construct one with New and mount Handler.
package server

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

//go:embed web
var webFS embed.FS

// Config holds the settings needed to start a Server.
type Config struct {
	DBPath     string // path to the SQLite file (created if missing)
	Subscriber string // "mailto:" or URL used in the VAPID JWT
	Token      string // optional API token; if empty, loaded/generated from the DB
}

// Server holds shared state for the running application.
type Server struct {
	store      *store
	vapidPub   string
	vapidPriv  string
	token      string
	tokenMu    sync.RWMutex
	subscriber string
	limiter    *rateLimiter
	sessions   *sessionStore
}

// New opens (or creates) the database and loads/generates the VAPID keypair,
// API token, and app name.
func New(cfg Config) (*Server, error) {
	st, err := openStore(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	s := &Server{store: st, subscriber: cfg.Subscriber, token: cfg.Token}
	s.limiter = newRateLimiter(5, 1) // burst 5, refill 1/sec per IP
	s.sessions = newSessionStore(7 * 24 * time.Hour)
	if err := s.initSecrets(); err != nil {
		st.close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Server) Close() error { return s.store.close() }

// Token returns the API token clients must present to send notifications.
func (s *Server) Token() string { return s.getToken() }

// initSecrets loads the VAPID keypair, API token, and app name from the
// database, generating them on first run.
func (s *Server) initSecrets() error {
	priv, err := s.store.getSettingStr("vapid_private_key")
	if err != nil {
		return err
	}
	pub, err := s.store.getSettingStr("vapid_public_key")
	if err != nil {
		return err
	}
	if priv == "" || pub == "" {
		priv, pub, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			return err
		}
		if err := s.store.setSetting("vapid_private_key", []byte(priv)); err != nil {
			return err
		}
		if err := s.store.setSetting("vapid_public_key", []byte(pub)); err != nil {
			return err
		}
	}
	s.vapidPriv, s.vapidPub = priv, pub

	if s.token == "" {
		token, err := s.store.getSettingStr("api_token")
		if err != nil {
			return err
		}
		if token == "" {
			token = randomHex(32)
			if err := s.store.setSetting("api_token", []byte(token)); err != nil {
				return err
			}
		}
		s.setToken(token)
	}

	name, err := s.store.getSettingStr("app_name")
	if err != nil {
		return err
	}
	if name == "" {
		if err := s.store.setSetting("app_name", []byte("Notify")); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) appName() string {
	name, _ := s.store.getSettingStr("app_name")
	if name == "" {
		return "Notify"
	}
	return name
}

// getToken returns the current API token with read lock.
func (s *Server) getToken() string {
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()
	return s.token
}

// setToken updates the API token with write lock.
func (s *Server) setToken(tok string) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	s.token = tok
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}
