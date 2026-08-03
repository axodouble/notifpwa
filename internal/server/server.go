// Package server implements the notifpwa Web Push application: storage, HTTP
// handlers, and push delivery. Construct one with New and mount Handler.
package server

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

//go:embed web
var webFS embed.FS

// Config holds the settings needed to start a Server.
type Config struct {
	DBPath     string // path to the SQLite file (created if missing)
	Subscriber string // "mailto:" or URL used in the VAPID JWT
	Token      string // optional root token granting full admin+send access; if empty, the server bootstraps an initial admin token surfaced via InitialToken()
}

// Server holds shared state for the running application.
type Server struct {
	store        *store
	vapidPub     string
	vapidPriv    string
	rootToken    string
	initialToken string
	subscriber   string
	limiter      *rateLimiter
	postLimiter  *rateLimiter
	sessions     *sessionStore
}

// New opens (or creates) the database and loads/generates the VAPID keypair,
// API token, and app name.
func New(cfg Config) (*Server, error) {
	st, err := openStore(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	s := &Server{store: st, subscriber: cfg.Subscriber, rootToken: cfg.Token}
	s.limiter = newRateLimiter(5, 1)      // burst 5, refill 1/sec per IP
	s.postLimiter = newRateLimiter(20, 5) // burst 20, refill 5/sec per IP for public posts
	s.sessions = newSessionStore(7 * 24 * time.Hour)
	if err := s.initSecrets(); err != nil {
		st.close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Server) Close() error { return s.store.close() }

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

	if err := s.initTokens(); err != nil {
		return err
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

// initTokens migrates a legacy single token into the tokens table (once) and
// guarantees at least one admin token exists when there is no root token.
func (s *Server) initTokens() error {
	count, err := s.store.countTokens()
	if err != nil {
		return err
	}
	if count == 0 {
		legacy, err := s.store.getSettingStr("api_token")
		if err != nil {
			return err
		}
		if legacy != "" {
			if _, err := s.store.addToken("Default", legacy, true, true); err != nil {
				return err
			}
			s.initialToken = legacy // surface once so the operator keeps working access
			count = 1
		}
		// The hash is now authoritative; drop the plaintext secret.
		if err := s.store.deleteSetting("api_token"); err != nil {
			return err
		}
	}
	if count == 0 && s.rootToken == "" {
		_, secret, err := s.store.createToken("Default", true, true)
		if err != nil {
			return err
		}
		s.initialToken = secret
	}
	return nil
}

// InitialToken returns a secret the operator can use to sign in: the root token
// if configured, otherwise a secret generated or migrated on THIS run only.
// Returns "" on later runs (secrets are hashed and unrecoverable).
func (s *Server) InitialToken() string {
	if s.rootToken != "" {
		return s.rootToken
	}
	return s.initialToken
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}
