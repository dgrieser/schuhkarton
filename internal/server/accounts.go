package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/dgrieser/schuhkarton/internal/auth"
)

// Who may do what:
//
//	anonymous  look and download, when access.anonymous is on (and with the
//	           access key on a network address)
//	user       also favorites (their own) and names of people
//	admin      also settings, accounts, rescans, backups and editing photos
//
// Accounts sign in at /login with a session cookie; the access key does not
// matter for them.

const (
	sessionCookie = "schuhkarton_session"
	cookieName    = "schuhkarton" // access key for anonymous viewers
)

type identity struct {
	user  *auth.User // nil: anonymous
	keyOK bool       // the access key is not needed or was given
}

type ctxKey struct{}

func identityOf(r *http.Request) identity {
	id, _ := r.Context().Value(ctxKey{}).(identity)
	return id
}

// userName is the signed-in account ("" anonymous).
func userName(r *http.Request) string {
	if u := identityOf(r).user; u != nil {
		return u.Name
	}
	return ""
}

func isAdmin(r *http.Request) bool { return identityOf(r).user.IsAdmin() }

// role returns admin, user or anon.
func role(r *http.Request) string {
	u := identityOf(r).user
	switch {
	case u.IsAdmin():
		return auth.RoleAdmin
	case u.Active():
		return auth.RoleUser
	}
	return "anon"
}

// publicPath is reachable without an account or access key.
func publicPath(p string) bool {
	switch p {
	case "/healthz", "/login", "/api/me", "/api/config", "/setup":
		return true
	}
	return strings.HasPrefix(p, "/assets/") || strings.HasPrefix(p, "/api/auth/") || strings.HasPrefix(p, "/api/setup/")
}

// gate resolves who is asking and lets them in, or sends them to /login.
func (s *Server) gate(next http.Handler) http.Handler {
	keyOK := func(v string) bool {
		return s.token == "" || subtle.ConstantTimeCompare([]byte(v), []byte(s.token)) == 1
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// link with the access key: remember it, drop it from the address
		if t := r.URL.Query().Get("t"); t != "" && s.token != "" && keyOK(t) {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600, Secure: s.secure(r)})
			q := r.URL.Query()
			q.Del("t")
			u := *r.URL
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.String(), http.StatusFound)
			return
		}
		var id identity
		if c, err := r.Cookie(sessionCookie); err == nil && s.users != nil {
			id.user = s.users.Session(c.Value)
		}
		if s.token == "" {
			id.keyOK = true
		} else if c, err := r.Cookie(cookieName); err == nil {
			id.keyOK = keyOK(c.Value)
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, id))

		// state changes only from our own pages
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if id.user != nil || publicPath(r.URL.Path) || (s.cfg.Access.Anonymous && id.keyOK) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
			http.Error(w, "Bitte anmelden", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" && r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}
	if strings.HasSuffix(o, "//"+r.Host) {
		return true
	}
	if pu := s.cfg.Server.PublicURL; pu != "" {
		if u, err := url.Parse(pu); err == nil && strings.EqualFold(o, u.Scheme+"://"+u.Host) {
			return true
		}
	}
	return false
}

// secure reports whether cookies should be HTTPS only.
func (s *Server) secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
		strings.HasPrefix(s.cfg.Server.PublicURL, "https://")
}

// requireUser lets active accounts through.
func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !identityOf(r).user.Active() {
			http.Error(w, "Dafür bitte anmelden", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// requireAdmin lets only admins through.
func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(r) {
			http.Error(w, "Nur für Admins", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (s *Server) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("POST /api/me/password", s.requireUser(s.myPassword))
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("GET /api/users", s.requireAdmin(s.listUsers))
	mux.HandleFunc("POST /api/users", s.requireAdmin(s.createUser))
	mux.HandleFunc("POST /api/users/{name}", s.requireAdmin(s.updateUser))
	mux.HandleFunc("DELETE /api/users/{name}", s.requireAdmin(s.deleteUser))
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if identityOf(r).user != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	b, err := fs.ReadFile(s.files, "login.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b = withLang(b, s.cfg.UI.Language)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

// safeNext keeps redirects on this site.
func safeNext(n string) string {
	if !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.HasPrefix(n, "/\\") || strings.HasPrefix(n, "/login") {
		return "/"
	}
	return n
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	res := map[string]any{
		"role": role(r), "anonymous": s.cfg.Access.Anonymous, "register": s.cfg.Access.Register,
		"keyOK": id.keyOK, "title": s.cfg.UI.Title,
	}
	if id.user != nil {
		res["name"] = id.user.Name
	}
	if id.user.IsAdmin() && s.users != nil {
		res["pendingUsers"] = s.users.Pending()
	}
	writeJSON(w, r, res)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL.Seconds()), Secure: s.secure(r)})
}

type credentials struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	tok, u, err := s.users.Login(c.Name, c.Password, clientIP(r))
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error(), "pending": errors.Is(err, auth.ErrPending)})
		return
	}
	s.setSession(w, r, tok)
	writeJSON(w, r, map[string]any{"ok": true, "name": u.Name, "role": u.Role})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.users.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, r, map[string]any{"ok": true})
}

// maxPending limits how many registrations may wait at once (spam).
const maxPending = 25

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Access.Register {
		writeJSON(w, r, map[string]any{"ok": false, "error": auth.ErrNoRegister.Error()})
		return
	}
	var c credentials
	if err := readJSON(r, &c); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	if s.users.Pending() >= maxPending {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Zu viele offene Anfragen; bitte später noch einmal"})
		return
	}
	if err := s.users.Create(c.Name, c.Password, auth.RoleUser, true); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "pending": true})
}

func (s *Server) myPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	keep := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		keep = c.Value
	}
	if err := s.users.ChangePassword(userName(r), req.Old, req.New, keep); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, map[string]any{"users": s.users.List(), "me": userName(r), "register": s.cfg.Access.Register})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		credentials
		Role string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	if err := s.users.Create(req.Name, req.Password, req.Role, false); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var up auth.Update
	if err := readJSON(r, &up); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	if err := s.users.Apply(r.PathValue("name"), up); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	u, ok := s.users.Get(name)
	if !ok {
		writeJSON(w, r, map[string]any{"ok": false, "error": auth.ErrNotFound.Error()})
		return
	}
	if err := s.users.Delete(name); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.lib.DropUserFavs(u.Name)
	writeJSON(w, r, map[string]any{"ok": true})
}
