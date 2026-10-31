package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io/fs"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/internal/gallery"
	"github.com/dgrieser/schuhkarton/internal/setup"
	"github.com/dgrieser/schuhkarton/internal/version"
)

// Plan is what the wizard or the settings page sends.
type Plan struct {
	Config *config.Config `json:"config"`
	// TokenMode is "keep", "off" or "new".
	TokenMode string `json:"tokenMode"`
	// Import copies another cache into the cache folder first.
	Import string `json:"import"`
}

// Progress reports a running save (cache import, restart).
type Progress struct {
	Running  bool   `json:"running"`
	Phase    string `json:"phase"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Error    string `json:"error,omitempty"`
	Finished bool   `json:"finished"`
	Link     string `json:"link,omitempty"` // where to go afterwards (with ?t= when a token is new)
}

// ProgressBox shares the progress of a save between the wizard and the
// settings page that replaces it once the gallery runs.
type ProgressBox struct {
	mu sync.Mutex
	p  Progress
}

func (b *ProgressBox) get() Progress {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.p
}

func (b *ProgressBox) set(p Progress) {
	b.mu.Lock()
	b.p = p
	b.mu.Unlock()
}

// SetupOptions configure the wizard (first start) or the settings page.
type SetupOptions struct {
	Install bool   // first start: protected by Token instead of the gallery's token
	Token   string // setup token printed to the log
	Config  func() *config.Config
	Listen  func() string // the address the server listens on now
	// AccessLink returns the gallery link with its token (settings page only).
	AccessLink func() string
	Progress   *ProgressBox // shared across restarts
	WebDir     string
	// Apply saves the configuration, imports a cache and (re)starts the
	// gallery. It reports progress through the callback.
	Apply func(ctx context.Context, p Plan, progress func(Progress)) (link string, err error)
}

// Setup serves /setup and /api/setup/*.
type Setup struct {
	o     SetupOptions
	files fs.FS
	prog  *ProgressBox
}

// NewSetup creates the wizard or settings handler.
func NewSetup(o SetupOptions) *Setup {
	if o.Progress == nil {
		o.Progress = &ProgressBox{}
	}
	return &Setup{o: o, files: UIFiles(o.WebDir), prog: o.Progress}
}

const setupCookie = "schuhkarton_setup"

// Handler returns the routes. In install mode everything else redirects to
// the wizard.
func (s *Setup) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /setup", s.page)
	mux.HandleFunc("GET /api/setup/state", s.guard(s.state))
	mux.HandleFunc("GET /api/setup/browse", s.guard(s.browse))
	mux.HandleFunc("POST /api/setup/folder", s.guard(s.folder))
	mux.HandleFunc("POST /api/setup/cache", s.guard(s.cache))
	mux.HandleFunc("POST /api/setup/faces", s.guard(s.faces))
	mux.HandleFunc("POST /api/setup/save", s.guard(s.save))
	mux.HandleFunc("GET /api/setup/progress", s.guard(s.progress))
	if s.o.Install {
		mux.Handle("GET /assets/", http.StripPrefix("/assets/", assetHandler(s.files)))
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("setup\n")) })
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
				http.Redirect(w, r, "/setup", http.StatusFound)
				return
			}
			http.Error(w, "Schuhkarton wird gerade eingerichtet", http.StatusServiceUnavailable)
		})
	}
	return mux
}

// guard checks the setup token in install mode (the gallery's own auth
// protects the settings page).
func (s *Setup) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.o.Install && !s.authorized(r) {
			http.Error(w, "Bitte den Einrichtungslink aus der Ausgabe von schuhkarton öffnen.", http.StatusUnauthorized)
			return
		}
		// same-origin only: the browser sends Origin on POST
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && !strings.HasSuffix(o, "//"+r.Host) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func (s *Setup) authorized(r *http.Request) bool {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		if c, err := r.Cookie(setupCookie); err == nil {
			tok = c.Value
		}
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.o.Token)) == 1
}

func (s *Setup) page(w http.ResponseWriter, r *http.Request) {
	if s.o.Install {
		if !s.authorized(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Schuhkarton einrichten</title>` +
				`<body style="font:16px system-ui;padding:2rem;background:#111;color:#eee;max-width:40rem"><h1>Schuhkarton einrichten</h1>` +
				`<p>Bitte den Einrichtungslink öffnen, den <code>schuhkarton</code> beim Start ausgibt (mit <code>?token=…</code>).</p>`))
			return
		}
		if t := r.URL.Query().Get("token"); t != "" {
			http.SetCookie(w, &http.Cookie{Name: setupCookie, Value: t, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
	}
	b, err := fs.ReadFile(s.files, "setup.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (s *Setup) state(w http.ResponseWriter, r *http.Request) {
	c := s.o.Config()
	view := c.Clone()
	hasToken := view.Server.Token != "" && view.Server.Token != "off" && view.Server.Token != "auto"
	view.Server.Token = ""
	if view.Faces.Token != "" {
		view.Faces.Token = "********"
	}
	view.Worker.Token = ""
	var known []string
	for _, f := range c.Library.Folders {
		known = append(known, f.Name)
	}
	writeJSON(w, r, map[string]any{
		"mode":       map[bool]string{true: "install", false: "settings"}[s.o.Install],
		"config":     view,
		"defaults":   config.Defaults(),
		"locked":     c.LockedKeys(),
		"configPath": c.Path(),
		"hasToken":   hasToken,
		"tokenAuto":  c.Server.Token == "auto",
		"listen":     s.o.Listen(),
		"tools":      setup.Tools(c.Media.ExifTool, c.Media.FFmpeg),
		"caches":     gallery.FindCaches(setup.CacheCandidates(c.Cache.Dir)),
		"cache":      setup.CheckCache(c.Cache.Dir),
		"version":    version.String(),
		"os":         runtime.GOOS + "/" + runtime.GOARCH,
		"lan":        lanAddrs(),
		"accessLink": s.accessLink(),
	})
}

func (s *Setup) accessLink() string {
	if s.o.Install || s.o.AccessLink == nil {
		return ""
	}
	return s.o.AccessLink()
}

func lanAddrs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

func (s *Setup) browse(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, setup.Browse(r.URL.Query().Get("path")))
}

func (s *Setup) folder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, setup.CheckPhotos(config.ExpandHome(strings.TrimSpace(req.Path)), 4*time.Second))
}

func (s *Setup) cache(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := config.ExpandHome(strings.TrimSpace(req.Path))
	writeJSON(w, r, map[string]any{"folder": setup.CheckCache(p), "cache": gallery.InspectCache(p)})
}

func (s *Setup) faces(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Token == "********" {
		req.Token = s.o.Config().Faces.Token
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	info, err := gallery.ProbeWorker(ctx, req.URL, req.Token)
	res := map[string]any{"info": info, "ok": err == nil && info.Detection}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, r, res)
}

func (s *Setup) save(w http.ResponseWriter, r *http.Request) {
	var p Plan
	if err := readJSON(r, &p); err != nil || p.Config == nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	cur := s.o.Config()
	next := merge(cur, p.Config)
	switch p.TokenMode {
	case "off":
		if cur.Locked("server.token") == "" {
			next.Server.Token = "off"
		}
	case "new":
		if cur.Locked("server.token") == "" {
			next.Server.Token = NewToken()
		}
	}
	if p.Config.Faces.Token == "********" {
		next.Faces.Token = cur.Faces.Token
	}
	if err := next.Validate(); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if s.prog.get().Running {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Speichern läuft schon"})
		return
	}
	s.prog.set(Progress{Running: true, Phase: "Speichern"})
	p.Config = next
	go func() {
		link, err := s.o.Apply(context.Background(), p, func(pr Progress) {
			pr.Running = true
			s.prog.set(pr)
		})
		done := Progress{Finished: true, Link: link, Phase: "fertig"}
		if err != nil {
			done.Error = err.Error()
		}
		s.prog.set(done)
	}()
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Setup) progress(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.prog.get())
}

// merge copies the editable settings from the form onto the current
// configuration; locked keys keep their value.
func merge(cur, in *config.Config) *config.Config {
	n := cur.Clone()
	set := func(key string, apply func()) {
		if cur.Locked(key) == "" {
			apply()
		}
	}
	set("library.folders", func() { n.Library.Folders = cleanFolders(in.Library.Folders) })
	set("library.exclude", func() { n.Library.Exclude = in.Library.Exclude })
	set("library.rescan", func() { n.Library.Rescan = in.Library.Rescan })
	set("cache.dir", func() { n.Cache.Dir = config.ExpandHome(strings.TrimSpace(in.Cache.Dir)) })
	set("cache.previews", func() { n.Cache.Previews = in.Cache.Previews })
	set("media.workers", func() { n.Media.Workers = in.Media.Workers })
	set("media.video_previews", func() { n.Media.VideoPreviews = in.Media.VideoPreviews })
	set("media.transcode", func() { n.Media.Transcode = in.Media.Transcode })
	set("media.thumb_size", func() { n.Media.ThumbSize = in.Media.ThumbSize })
	set("media.exiftool", func() { n.Media.ExifTool = in.Media.ExifTool })
	set("media.ffmpeg", func() { n.Media.FFmpeg = in.Media.FFmpeg })
	set("faces.mode", func() { n.Faces.Mode = in.Faces.Mode })
	set("faces.url", func() { n.Faces.URL = strings.TrimSpace(in.Faces.URL) })
	set("faces.token", func() { n.Faces.Token = strings.TrimSpace(in.Faces.Token) })
	set("faces.workers", func() { n.Faces.Workers = in.Faces.Workers })
	set("server.listen", func() { n.Server.Listen = strings.TrimSpace(in.Server.Listen) })
	set("server.public_url", func() { n.Server.PublicURL = strings.TrimRight(strings.TrimSpace(in.Server.PublicURL), "/") })
	set("backup.dir", func() { n.Backup.Dir = config.ExpandHome(strings.TrimSpace(in.Backup.Dir)) })
	set("backup.interval", func() { n.Backup.Interval = in.Backup.Interval })
	set("backup.originals", func() { n.Backup.Originals = in.Backup.Originals })
	set("backup.cache", func() { n.Backup.Cache = in.Backup.Cache })
	set("backup.config", func() { n.Backup.Config = in.Backup.Config })
	set("ui.title", func() { n.UI.Title = strings.TrimSpace(in.UI.Title) })
	set("ui.theme", func() { n.UI.Theme = in.UI.Theme })
	return n
}

func cleanFolders(in []config.Folder) []config.Folder {
	var out []config.Folder
	for _, f := range in {
		f.Path = config.ExpandHome(strings.TrimSpace(f.Path))
		f.Name = strings.TrimSpace(strings.ReplaceAll(f.Name, "/", "-"))
		if f.Path != "" {
			out = append(out, f)
		}
	}
	return out
}

// NewToken returns a random access token.
func NewToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
