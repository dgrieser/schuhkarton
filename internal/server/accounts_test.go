package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgrieser/schuhkarton/internal/auth"
	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/internal/gallery"
)

type fixture struct {
	t     *testing.T
	h     http.Handler
	cfg   *config.Config
	users *auth.Store
}

func newFixture(t *testing.T, token string, anonymous bool) *fixture {
	t.Helper()
	dir := t.TempDir()
	lib, err := gallery.Open(gallery.Options{Roots: []gallery.Root{{Name: "Bilder", Path: t.TempDir()}}, CacheDir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lib.Close)
	users, err := auth.Open(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(users.Create("Anna", "admin-pass", auth.RoleAdmin, false))
	must(users.Create("Ben", "user-pass", auth.RoleUser, false))
	must(users.Create("Cara", "wait-pass", auth.RoleUser, true))
	cfg := config.Defaults()
	cfg.Access.Anonymous = anonymous
	h := New(context.Background(), Options{Lib: lib, Config: cfg, Token: token, Users: users})
	return &fixture{t: t, h: h, cfg: cfg, users: users}
}

// do sends a request with an optional session cookie and returns the status.
func (f *fixture) do(method, path, body, session string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) login(name, pw string) string {
	f.t.Helper()
	w := f.do("POST", "/api/auth/login", `{"name":"`+name+`","password":"`+pw+`"}`, "")
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c.Value
		}
	}
	f.t.Fatalf("login %s failed: %s", name, w.Body.String())
	return ""
}

func TestPermissions(t *testing.T) {
	f := newFixture(t, "", true)
	ben := f.login("Ben", "user-pass")
	anna := f.login("Anna", "admin-pass")
	cases := []struct {
		method, path, body, session string
		want                        int
	}{
		{"GET", "/api/items", "", "", 200},
		{"POST", "/api/fav", `{"ids":[],"on":true}`, "", 401},
		{"POST", "/api/fav", `{"ids":[],"on":true}`, ben, 200},
		{"POST", "/api/face/x/0", `{"name":"x"}`, "", 401},
		{"POST", "/api/rescan", `{}`, "", 403},
		{"POST", "/api/rescan", `{}`, ben, 403},
		{"GET", "/api/users", "", ben, 403},
		{"GET", "/api/users", "", anna, 200},
		{"GET", "/api/admin/trash", "", ben, 403},
		{"GET", "/api/admin/trash", "", anna, 200},
		{"POST", "/api/users", `{"name":"Dora","password":"dora-pass","role":"user"}`, anna, 200},
	}
	for _, c := range cases {
		if got := f.do(c.method, c.path, c.body, c.session).Code; got != c.want {
			t.Errorf("%s %s (session %v): %d, want %d", c.method, c.path, c.session != "", got, c.want)
		}
	}
	// pending accounts cannot sign in
	w := f.do("POST", "/api/auth/login", `{"name":"Cara","password":"wait-pass"}`, "")
	var res map[string]any
	json.Unmarshal(w.Body.Bytes(), &res)
	if res["ok"] == true || res["pending"] != true {
		t.Errorf("pending login: %s", w.Body.String())
	}
	// /api/me tells the role
	json.Unmarshal(f.do("GET", "/api/me", "", anna).Body.Bytes(), &res)
	if res["role"] != "admin" || res["pendingUsers"] != float64(1) {
		t.Errorf("me: %v", res)
	}
	// state changes from other sites are refused
	if got := f.do("POST", "/api/fav", `{"ids":[],"on":true}`, ben, "Origin", "https://evil.example").Code; got != 403 {
		t.Errorf("cross-origin: %d", got)
	}
}

func TestLoginRequired(t *testing.T) {
	f := newFixture(t, "", false)
	if w := f.do("GET", "/", "", ""); w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Errorf("GET / without login: %d %s", w.Code, w.Header().Get("Location"))
	}
	if got := f.do("GET", "/api/items", "", "").Code; got != 401 {
		t.Errorf("items without login: %d", got)
	}
	for _, p := range []string{"/login", "/api/me", "/assets/app.css"} {
		if got := f.do("GET", p, "", "").Code; got != 200 {
			t.Errorf("%s: %d", p, got)
		}
	}
	ben := f.login("Ben", "user-pass")
	if got := f.do("GET", "/api/items", "", ben).Code; got != 200 {
		t.Errorf("items with login: %d", got)
	}
}

func TestAccessKeyOnlyForAnonymous(t *testing.T) {
	f := newFixture(t, "geheimer-schluessel", true)
	if got := f.do("GET", "/api/items", "", "").Code; got != 401 {
		t.Errorf("without key: %d", got)
	}
	w := f.do("GET", "/?t=geheimer-schluessel", "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("key link: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/items", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Errorf("with key cookie: %d", rec.Code)
	}
	ben := f.login("Ben", "user-pass")
	if got := f.do("GET", "/api/items", "", ben).Code; got != 200 {
		t.Errorf("account without key: %d", got)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/#/fotos": "/#/fotos", "//evil.example": "/", "https://evil": "/", "/login?x": "/", "/\\evil": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetupClaimsRegisteredAccount(t *testing.T) {
	dir := t.TempDir()
	users, _ := auth.Open(filepath.Join(dir, "users.json"))
	users.Create("Eva", "eva-passwort", auth.RoleUser, true) // registered at /login, no admin yet
	cfg := config.Defaults()
	cfg.SetPath(filepath.Join(dir, "config.yaml"))
	cfg.Library.Folders = []config.Folder{{Name: "B", Path: t.TempDir()}}
	applied := make(chan bool, 1)
	h := NewSetup(SetupOptions{Token: "tok", Users: users, Config: func() *config.Config { return cfg }, Listen: func() string { return "127.0.0.1:1" },
		Apply: func(ctx context.Context, p Plan, progress func(Progress)) (string, error) {
			applied <- true
			return "/", nil
		}}).Handler()
	save := func(pw string) map[string]any {
		body, _ := json.Marshal(map[string]any{"config": cfg, "tokenMode": "keep", "admin": map[string]string{"name": "eva", "password": pw}})
		r := httptest.NewRequest("POST", "/api/setup/save", strings.NewReader(string(body)))
		r.AddCookie(&http.Cookie{Name: setupCookie, Value: "tok"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var res map[string]any
		json.Unmarshal(w.Body.Bytes(), &res)
		return res
	}
	if res := save("falsch-falsch"); res["ok"] == true {
		t.Fatalf("wrong password accepted: %v", res)
	}
	if users.HasAdmin() {
		t.Fatal("admin without the right password")
	}
	if res := save("eva-passwort"); res["ok"] != true {
		t.Fatalf("claim: %v", res)
	}
	<-applied
	if u, _ := users.Get("Eva"); !u.IsAdmin() {
		t.Fatalf("Eva is not admin: %+v", u)
	}
}
