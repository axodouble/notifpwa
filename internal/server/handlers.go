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

	// Token-protected surface. admin.js is static; its calls rely on the
	// same-origin admin session cookie, so it needs no gate itself.
	mux.HandleFunc("GET /admin.js", s.serveStatic("web/admin.js", "text/javascript"))
	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("POST /admin/logout", s.handleLogout)
	mux.HandleFunc("POST /api/config", s.requireAdmin(s.handleConfig))
	mux.HandleFunc("POST /api/send", s.requireSend(s.handleSend))
	mux.HandleFunc("GET /api/devices", s.requireAdmin(s.handleListDevices))
	mux.HandleFunc("POST /api/devices/label", s.requireAdmin(s.handleLabelDevice))
	mux.HandleFunc("DELETE /api/devices", s.requireAdmin(s.handleDeleteDevice))
	mux.HandleFunc("GET /api/tokens", s.requireAdmin(s.handleListTokens))
	mux.HandleFunc("POST /api/tokens", s.requireAdmin(s.handleCreateToken))
	mux.HandleFunc("PATCH /api/tokens/{id}", s.requireAdmin(s.handleUpdateToken))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.requireAdmin(s.handleDeleteToken))

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

const (
	paperLight = "#f1edee"
	paperDark  = "#182027"
)

func prefersDark(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Sec-CH-Prefers-Color-Scheme"), "dark")
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(webFS, "web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Accept-CH", "Sec-CH-Prefers-Color-Scheme")
	w.Header().Set("Critical-CH", "Sec-CH-Prefers-Color-Scheme")
	w.Header().Set("Vary", "Sec-CH-Prefers-Color-Scheme")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, map[string]string{
		"AppName":        s.appName(),
		"VapidPublicKey": s.vapidPub,
	})
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	name := s.appName()
	paper := paperLight
	if prefersDark(r) {
		paper = paperDark
	}
	manifest := map[string]any{
		"name":             name,
		"short_name":       name,
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": paper,
		"theme_color":      paper,
		"icons": []map[string]string{
			{"src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/icon.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/icon.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	}

	w.Header().Set("Accept-CH", "Sec-CH-Prefers-Color-Scheme")
	w.Header().Set("Vary", "Sec-CH-Prefers-Color-Scheme")
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

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

// callerScopes resolves a request's capabilities: a valid admin session cookie
// or the root token grant full access; otherwise a presented secret (Bearer or
// ?token=) grants exactly its table scopes.
func (s *Server) callerScopes(r *http.Request) (admin, send bool) {
	if c, err := r.Cookie(adminCookie); err == nil && s.sessions.valid(c.Value, time.Now()) {
		return true, true
	}
	secret := bearerToken(r)
	if secret == "" {
		secret = r.URL.Query().Get("token")
	}
	if secret == "" {
		return false, false
	}
	if s.rootToken != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.rootToken)) == 1 {
		return true, true
	}
	if rec, err := s.store.lookupToken(secret); err == nil && rec != nil {
		return rec.ScopeAdmin, rec.ScopeSend
	}
	return false, false
}

func (s *Server) adminAuthed(r *http.Request) bool {
	admin, _ := s.callerScopes(r)
	return admin
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if admin, _ := s.callerScopes(r); !admin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) requireSend(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, send := s.callerScopes(r); !send {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthed(r) {
		http.Error(w, "invalid or missing ?token=", http.StatusUnauthorized)
		return
	}
	// If authed by token (not a valid cookie), mint a fresh session so the
	// token stops appearing in the URL on subsequent navigation. This also
	// covers a stale/expired cookie presented alongside a valid ?token=.
	if c, err := r.Cookie(adminCookie); err != nil || !s.sessions.valid(c.Value, time.Now()) {
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

func tokenJSON(t tokenRecord) map[string]any {
	return map[string]any{
		"id": t.ID, "label": t.Label, "prefix": t.Prefix,
		"admin": t.ScopeAdmin, "send": t.ScopeSend,
		"created_at": t.CreatedAt, "last_used_at": t.LastUsedAt,
	}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.store.listTokens()
	if err != nil {
		http.Error(w, "could not list tokens", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(toks))
	for _, t := range toks {
		out = append(out, tokenJSON(t))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
		Admin bool   `json:"admin"`
		Send  bool   `json:"send"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !body.Admin && !body.Send {
		http.Error(w, "token needs at least one scope", http.StatusBadRequest)
		return
	}
	label := strings.TrimSpace(body.Label)
	id, secret, err := s.store.createToken(label, body.Admin, body.Send)
	if err != nil {
		http.Error(w, "could not create token", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id": id, "secret": secret, "prefix": secret[:6],
		"label": label, "admin": body.Admin, "send": body.Send,
	})
}

func (s *Server) handleUpdateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Label *string `json:"label"`
		Admin *bool   `json:"admin"`
		Send  *bool   `json:"send"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Verify token exists.
	rec, err := s.store.tokenByID(id)
	if err != nil {
		http.Error(w, "could not check token", http.StatusInternalServerError)
		return
	}
	if rec == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if body.Admin != nil && !*body.Admin && s.rootToken == "" {
		stranded, err := s.wouldStrandAdmin(id)
		if err != nil {
			http.Error(w, "could not check tokens", http.StatusInternalServerError)
			return
		}
		if stranded {
			http.Error(w, "refusing to remove the last admin token; set API_TOKEN first", http.StatusConflict)
			return
		}
	}
	if err := s.store.updateToken(id, body.Label, body.Admin, body.Send); err != nil {
		http.Error(w, "could not update token", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.rootToken == "" {
		stranded, err := s.wouldStrandAdmin(id)
		if err != nil {
			http.Error(w, "could not check tokens", http.StatusInternalServerError)
			return
		}
		if stranded {
			http.Error(w, "refusing to delete the last admin token; set API_TOKEN first", http.StatusConflict)
			return
		}
	}
	ok, err := s.store.deleteToken(id)
	if err != nil {
		http.Error(w, "could not delete token", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// wouldStrandAdmin reports whether deleting or downgrading token id would leave
// no admin-scoped tokens. Only meaningful when no root token is configured.
func (s *Server) wouldStrandAdmin(id string) (bool, error) {
	count, err := s.store.countAdminTokens()
	if err != nil {
		return false, err
	}
	if count > 1 {
		return false, nil
	}
	rec, err := s.store.tokenByID(id)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, nil
	}
	return rec.ScopeAdmin && count == 1, nil
}
