package server

import (
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"
)

// Handler builds the HTTP router for the application.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public PWA surface.
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /app.js", s.serveStatic("web/app.js", "text/javascript"))
	mux.HandleFunc("GET /sw.js", s.serveStatic("web/sw.js", "text/javascript"))
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("GET /icon.png", s.handleIcon)
	mux.HandleFunc("GET /favicon.ico", s.handleIcon)
	mux.HandleFunc("POST /api/subscribe", s.rateLimit(s.handleSubscribe))
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Token-protected surface. admin.js is static (it reads window.TOKEN,
	// which the token-gated admin page injects), so it needs no gate itself.
	mux.HandleFunc("GET /admin.js", s.serveStatic("web/admin.js", "text/javascript"))
	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin/logout", s.handleLogout)
	mux.HandleFunc("POST /api/config", s.requireToken(s.handleConfig))
	mux.HandleFunc("POST /api/send", s.requireToken(s.handleSend))
	mux.HandleFunc("POST /api/rotate-token", s.requireToken(s.handleRotateToken))
	mux.HandleFunc("GET /api/devices", s.requireToken(s.handleListDevices))
	mux.HandleFunc("POST /api/devices/label", s.requireToken(s.handleLabelDevice))
	mux.HandleFunc("DELETE /api/devices", s.requireToken(s.handleDeleteDevice))

	return mux
}

// serveStatic returns a handler that serves an embedded file with a fixed type.
func (s *Server) serveStatic(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := webFS.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(b)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(webFS, "web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, map[string]string{
		"AppName":        s.appName(),
		"VapidPublicKey": s.vapidPub,
	})
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	name := s.appName()
	manifest := map[string]any{
		"name":             name,
		"short_name":       name,
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#0f1115",
		"theme_color":      "#0f1115",
		"icons": []map[string]string{
			{"src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/icon.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/icon.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	json.NewEncoder(w).Encode(manifest)
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	data, _ := s.store.getSetting("icon_data")
	mime, _ := s.store.getSettingStr("icon_mime")
	if len(data) == 0 {
		// Fall back to the bundled default.
		b, err := webFS.ReadFile("web/default-icon.png")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data, mime = b, "image/png"
	}
	if mime == "" {
		mime = "image/png"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var sub subscription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&sub); err != nil {
		http.Error(w, "invalid subscription", http.StatusBadRequest)
		return
	}
	if sub.Endpoint == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		http.Error(w, "incomplete subscription", http.StatusBadRequest)
		return
	}
	if err := s.store.upsertSubscription(sub, r.UserAgent()); err != nil {
		http.Error(w, "could not store subscription", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const adminCookie = "notifpwa_admin"

// adminAuthed accepts either a valid session cookie or a valid ?token=.
func (s *Server) adminAuthed(r *http.Request) bool {
	if c, err := r.Cookie(adminCookie); err == nil && s.sessions.valid(c.Value, time.Now()) {
		return true
	}
	return s.tokenOK(r.URL.Query().Get("token"))
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthed(r) {
		http.Error(w, "invalid or missing ?token=", http.StatusUnauthorized)
		return
	}
	// If authed by token (not cookie), mint a session so the token stops
	// appearing in the URL on subsequent navigation.
	if _, err := r.Cookie(adminCookie); err != nil {
		id := s.sessions.create(time.Now())
		http.SetCookie(w, &http.Cookie{
			Name:     adminCookie,
			Value:    id,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	count, _ := s.store.countSubscriptions()
	tmpl, err := template.ParseFS(webFS, "web/admin.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, map[string]any{
		"AppName": s.appName(),
		"Token":   s.getToken(),
		"Count":   count,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(adminCookie); err == nil {
		s.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: adminCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		if err := s.store.setSetting("app_name", []byte(name)); err != nil {
			http.Error(w, "could not save name", http.StatusInternalServerError)
			return
		}
	}
	if file, hdr, err := r.FormFile("icon"); err == nil {
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 8<<20))
		if err != nil {
			http.Error(w, "could not read icon", http.StatusBadRequest)
			return
		}
		mime := hdr.Header.Get("Content-Type")
		if mime == "" {
			mime = "image/png"
		}
		if err := s.store.setSetting("icon_data", data); err != nil {
			http.Error(w, "could not save icon", http.StatusInternalServerError)
			return
		}
		s.store.setSetting("icon_mime", []byte(mime))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var p pushPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(p.Title) == "" && strings.TrimSpace(p.Body) == "" {
		http.Error(w, "title or body required", http.StatusBadRequest)
		return
	}
	if len(p.Actions) > 2 {
		p.Actions = p.Actions[:2]
	}
	res, err := s.broadcast(p)
	if err != nil {
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// requireToken wraps a handler, rejecting requests without a valid Bearer token.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !s.tokenOK(token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// tokenOK compares in constant time to avoid leaking the token via timing.
func (s *Server) tokenOK(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.getToken())) == 1
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ping(); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok"))
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.store.listDevices()
	if err != nil {
		http.Error(w, "could not list devices", http.StatusInternalServerError)
		return
	}
	if devs == nil {
		devs = []device{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(devs)
}

func (s *Server) handleLabelDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
		Label    string `json:"label"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Endpoint == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.store.setDeviceLabel(body.Endpoint, strings.TrimSpace(body.Label)); err != nil {
		http.Error(w, "could not set label", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Endpoint == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.store.deleteSubscription(body.Endpoint); err != nil {
		http.Error(w, "could not delete device", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	token := randomHex(32)
	if err := s.store.setSetting("api_token", []byte(token)); err != nil {
		http.Error(w, "could not rotate token", http.StatusInternalServerError)
		return
	}
	s.setToken(token)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}
