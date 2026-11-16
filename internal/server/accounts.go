package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

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
	case "/healthz", "/favicon.ico", "/login", "/api/me", "/api/config", "/setup":
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
			// the path alone, never "//host/…" (another site to a browser)
			u := url.URL{Path: "/" + strings.TrimLeft(r.URL.Path, `/\`), RawQuery: q.Encode()}
			http.Redirect(w, r, u.String(), http.StatusFound)
			return
		}
		var id identity
		if c, err := r.Cookie(sessionCookie); err == nil && s.users != nil {
			id.user = s.users.Session(c.Value)
		}
		if s.token == "" {
			// No key is needed, but without an account only under a name
			// that is surely this machine's (knownHost): a page of another
			// site that lets its own name point here (DNS rebinding) must
			// not read the gallery through the browser of somebody in the
			// home network.
			id.keyOK = id.user != nil || s.knownHost(r)
			if !id.keyOK && s.cfg.Access.Anonymous {
				s.warnHost(r.Host)
			}
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
		// back to this page afterwards, as the browser sees it (behind a proxy under its path)
		http.Redirect(w, r, "/login?next="+url.QueryEscape(s.home(r)+strings.TrimPrefix(r.URL.RequestURI(), "/")), http.StatusFound)
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

// localSuffixes end names that only resolve inside a home or company
// network: no site on the internet can make them point anywhere.
var localSuffixes = []string{".local", ".localhost", ".lan", ".home", ".home.arpa", ".internal", ".intranet", ".localdomain", ".fritz.box"}

// knownHost tells whether the request names this machine in a way no other
// site could: by address, by a name without a dot or with a suffix of home
// networks, by the machine's own name or by the host of server.public_url.
func (s *Server) knownHost(r *http.Request) bool {
	return hostKnown(r.Host, s.cfg.Server.PublicURL)
}

func hostKnown(hostport, publicURL string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" || host == "localhost" || !strings.Contains(host, ".") {
		return true // HTTP/1.0 without a name, or a name without a dot
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // an IPv6 zone
		host = host[:i]
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, suf := range localSuffixes {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	if u, err := url.Parse(publicURL); err == nil && publicURL != "" && strings.EqualFold(u.Hostname(), host) {
		return true
	}
	if own, err := os.Hostname(); err == nil && strings.EqualFold(strings.TrimSuffix(own, "."), host) {
		return true
	}
	return false
}

var hostWarned struct {
	sync.Mutex
	m map[string]bool
}

// warnHost logs once per name why viewing without an account was refused.
func (s *Server) warnHost(host string) {
	hostWarned.Lock()
	defer hostWarned.Unlock()
	if hostWarned.m[host] || len(hostWarned.m) >= 20 {
		return
	}
	if hostWarned.m == nil {
		hostWarned.m = map[string]bool{}
	}
	hostWarned.m[host] = true
	log.Printf("Ansehen ohne Konto unter dem Namen %q abgelehnt: der Name ist nicht als Adresse dieser Galerie bekannt. "+
		"Wenn er stimmt, server.public_url darauf setzen (Einstellungen → Zugang).", host)
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
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next"), s.home(r)), http.StatusFound)
		return
	}
	b, err := fs.ReadFile(s.files, "login.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b = withLogo(withLang(b, s.cfg.UI.Language), s.cfg.UI.Logo)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

// safeNext keeps redirects on this site; anything else goes to home.
func safeNext(n, home string) string {
	if !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.HasPrefix(n, "/login") {
		return home
	}
	// browsers read a backslash as "/" and drop tabs and line breaks:
	// "/<backslash>host" and "/<tab>/host" would lead to another site
	for _, c := range n {
		if c == '\\' || c < 0x20 || c == 0x7f {
			return home
		}
	}
	return n
}

// homePath is the path of the gallery's start page as the browser sees it.
// Behind a reverse proxy that serves the gallery under a path of the public
// address (public_url https://example.org/fotos), that path ("/fotos/");
// reached directly (in the home network) or without such a path, "/". A
// request came through the proxy when it was for the public host or carries
// the proxy's forwarding headers.
func homePath(publicURL string, r *http.Request) string {
	u, err := url.Parse(publicURL)
	if publicURL == "" || err != nil || u.Host == "" || !viaProxy(publicURL, r) {
		return "/"
	}
	return strings.TrimRight(u.Path, "/") + "/"
}

// viaProxy tells whether a request came by the public address: through a
// reverse proxy, or for the public host.
func viaProxy(publicURL string, r *http.Request) bool {
	if forwarded(r) {
		return true
	}
	u, err := url.Parse(publicURL)
	if publicURL == "" || err != nil || u.Host == "" {
		return false
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return strings.EqualFold(host, u.Host)
}

// forwarded tells whether a reverse proxy passed the request on (its
// forwarding headers); a direct request for the public host is not.
func forwarded(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

func (s *Server) home(r *http.Request) string { return homePath(s.cfg.Server.PublicURL, r) }

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	res := map[string]any{
		"role": role(r), "anonymous": s.cfg.Access.Anonymous, "register": s.cfg.Access.Register,
		"keyOK": id.keyOK, "title": s.cfg.UI.Title, "home": s.home(r),
	}
	if id.user != nil {
		res["name"] = id.user.Name
	}
	if id.user.IsAdmin() && s.users != nil {
		res["pendingUsers"] = s.users.Pending()
	}
	if s.users != nil && !s.users.HasAdmin() {
		// the login page explains how the first admin is made
		res["needAdmin"] = true
	}
	writeJSON(w, r, res)
}

// clientIP is the address the login limit counts by: the one that
// connected, or, when that is this machine itself (a reverse proxy running
// here), the client the proxy names last in X-Forwarded-For. A proxy on
// another machine is not told apart from a client that makes the header up,
// so its clients share one limit.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		xff := r.Header.Values("X-Forwarded-For")
		if len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if fwd := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); fwd != nil {
				return fwd.String()
			}
		}
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
	if err := s.users.AllowRegister(clientIP(r)); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
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
