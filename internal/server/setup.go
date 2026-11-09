package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html"
	"io/fs"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dgrieser/schuhkarton/internal/auth"
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
	// Admin is the first admin account (required while there is none).
	Admin *credentials `json:"admin,omitempty"`
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
	Install bool   // first start: protected by Token instead of an admin login
	Token   string // setup token printed to the log (first start, or while there is no admin)
	Users   *auth.Store
	Config  func() *config.Config
	Listen  func() string // the address the server listens on now
	// AccessLink returns the gallery link with its token (settings page only).
	AccessLink func() string
	Progress   *ProgressBox // shared across restarts
	WebDir     string
	// Apply saves the configuration, imports a cache and (re)starts the
	// gallery. It reports progress through the callback.
	Apply func(ctx context.Context, p Plan, progress func(Progress)) (link string, err error)
	// Save writes the configuration file, one writer at a time (nil: Config.Save).
	Save func(*config.Config) error
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
	mux.HandleFunc("POST /api/setup/ai", s.guard(s.aiTest))
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

// guard lets admins in, or whoever has the setup link while there is no
// admin yet (first start, or an update from a version without accounts).
func (s *Setup) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.allowed(r) {
			http.Error(w, "Nur für Admins. Ohne Admin-Konto: den Einrichtungslink aus der Ausgabe von schuhkarton öffnen.", http.StatusUnauthorized)
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

func (s *Setup) needAdmin() bool { return s.o.Users == nil || !s.o.Users.HasAdmin() }

func (s *Setup) allowed(r *http.Request) bool {
	if s.o.Install {
		return s.authorized(r)
	}
	if isAdmin(r) {
		return true
	}
	return s.o.Token != "" && s.needAdmin() && s.authorized(r)
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
	if t := r.URL.Query().Get("token"); t != "" && s.o.Token != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.o.Token)) == 1 {
		http.SetCookie(w, &http.Cookie{Name: setupCookie, Value: t, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if !s.allowed(r) {
		if !s.o.Install {
			http.Redirect(w, r, "/login?next=/setup", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Schuhkarton einrichten</title>` +
			`<body style="font:16px system-ui;padding:2rem;background:#111;color:#eee;max-width:40rem"><h1>Schuhkarton einrichten</h1>` +
			`<p>Bitte den Einrichtungslink öffnen, den <code>schuhkarton</code> beim Start ausgibt (mit <code>?token=…</code>).</p>` +
			`<p lang="en">Please open the setup link that <code>schuhkarton</code> prints when it starts.</p>`))
		return
	}
	b, err := fs.ReadFile(s.files, "setup.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b = withLang(b, s.o.Config().UI.Language)
	if !s.o.Install {
		b = settingsPage(b, homePath(s.o.Config().Server.PublicURL, r))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

// settingsReplacements turn the first-start page into the settings page.
func settingsReplacements(home string) [][2]string {
	return [][2]string{
		{`<title>Schuhkarton einrichten</title>`, `<title>Schuhkarton – Einstellungen</title>`},
		{`<h1 id="title" data-t="Schuhkarton einrichten">Schuhkarton einrichten</h1>`, `<h1 id="title" data-t="Einstellungen">Einstellungen</h1>`},
		{`<p class="muted" id="subtitle" data-t="In wenigen Schritten zur eigenen Fotogalerie.">In wenigen Schritten zur eigenen Fotogalerie.</p>`,
			`<p class="muted" id="subtitle">Schuhkarton ` + html.EscapeString(version.String()) + ` · ` + runtime.GOOS + "/" + runtime.GOARCH + `</p>`},
		{`<a class="btn ghost" id="back" href="/" hidden`, `<a class="btn ghost" id="back" href="` + html.EscapeString(home) + `"`},
		{`<button class="btn" id="prev" data-t="Zurück">`, `<button class="btn" id="prev" hidden data-t="Zurück">`},
		{`<button class="btn primary" id="next" data-t="Weiter">Weiter</button>`, `<button class="btn primary" id="next" disabled data-t="Speichern">Speichern</button>`},
	}
}

// settingsPage makes the page a running instance sends already the settings
// page, so the first-start welcome does not show until the script has
// loaded the state.
func settingsPage(b []byte, home string) []byte {
	for _, r := range settingsReplacements(home) {
		b = bytes.Replace(b, []byte(r[0]), []byte(r[1]), 1)
	}
	return b
}

func (s *Setup) state(w http.ResponseWriter, r *http.Request) {
	c := s.o.Config()
	view := c.Clone()
	hasToken := view.Server.Token != "" && view.Server.Token != "off" && view.Server.Token != "auto"
	view.Server.Token = ""
	if view.Faces.Token != "" {
		view.Faces.Token = "********"
	}
	if view.AI.APIKey != "" {
		view.AI.APIKey = "********"
	}
	if view.Worker.Token != "" {
		view.Worker.Token = "********" // saving keeps it (see save)
	}
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
		"slowHEIF":   gallery.SlowHEIF(),
		"caches":     gallery.FindCaches(setup.CacheCandidates(c.Cache.Dir)),
		"cache":      setup.CheckCache(c.Cache.Dir),
		"version":    version.String(),
		"os":         runtime.GOOS + "/" + runtime.GOARCH,
		"lan":        lanAddrs(),
		"accessLink": s.accessLink(),
		"home":       homePath(c.Server.PublicURL, r),
		"proxied":    forwarded(r), // HTTPS on or off changes the browser's address only without a proxy
		"needAdmin":  s.needAdmin(),
		// whether a key is there (never the key), and well-known variables that hold one
		"aiKey": map[string]any{"set": c.AI.Key() != "", "env": c.AI.APIKeyEnv,
			"mittwaldEnv": os.Getenv("MITTWALD_LLM_API_KEY") != ""},
		"me": userName(r),
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

// aiTest lists the models of an AI service and, with describe, lets the
// chosen model describe a small drawn test picture.
func (s *Setup) aiTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Key      string `json:"key"`
		Model    string `json:"model"`
		Describe bool   `json:"describe"`
		OCR      bool   `json:"ocr"`    // read a drawn picture with text using Model
		Speech   bool   `json:"speech"` // send a second of silence to Model
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cur := s.o.Config().AI
	key := strings.TrimSpace(req.Key)
	switch {
	case key == "********":
		key = cur.Key()
	case strings.HasPrefix(key, "$"):
		key = os.Getenv(strings.Trim(key, "${}"))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	url := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	if url == "" {
		url = config.MittwaldAIURL
	}
	if req.Speech {
		writeJSON(w, r, map[string]any{"speech": gallery.TestSpeech(ctx, gallery.SpeechOptions{URL: url, Key: key, Model: strings.TrimSpace(req.Model)}), "hasKey": key != ""})
		return
	}
	if req.OCR {
		writeJSON(w, r, map[string]any{"ocr": gallery.TestOCR(ctx, gallery.OCROptions{URL: url, Key: key, Model: strings.TrimSpace(req.Model)}), "hasKey": key != ""})
		return
	}
	res := gallery.TestAI(ctx, gallery.AIOptions{URL: url, Key: key, Model: strings.TrimSpace(req.Model),
		Languages: cur.Languages, ImageSize: cur.ImageSize}, req.Describe)
	writeJSON(w, r, map[string]any{"result": res, "hasKey": key != ""})
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
	if p.Config.Worker.Token == "********" {
		next.Worker.Token = cur.Worker.Token
	}
	if p.Config.AI.APIKey == "********" && cur.Locked("ai.api_key") == "" {
		next.AI.APIKey = cur.AI.APIKey
	}
	if err := next.Validate(); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// without a folder the gallery (and with it this page) would not start
	if len(next.Library.Folders) == 0 {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Mindestens einen Fotoordner hinzufügen."})
		return
	}
	if s.prog.get().Running {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Speichern läuft schon"})
		return
	}
	if s.needAdmin() && s.o.Users != nil {
		if p.Admin == nil {
			writeJSON(w, r, map[string]any{"ok": false, "error": "Bitte zuerst ein Admin-Konto anlegen"})
			return
		}
		err := s.o.Users.Create(p.Admin.Name, p.Admin.Password, auth.RoleAdmin, false)
		if errors.Is(err, auth.ErrExists) {
			// registered at /login already: the right password makes it the admin
			if cerr := s.o.Users.ClaimAdmin(p.Admin.Name, p.Admin.Password); cerr != nil {
				err = errors.New("Dieses Konto gibt es schon. Mit seinem Passwort wird es zum Admin; das Passwort stimmt nicht.")
			} else {
				err = nil
			}
		}
		if err != nil {
			writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if tok, err := s.o.Users.StartSession(p.Admin.Name); err == nil {
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
				MaxAge: int(auth.SessionTTL.Seconds()), Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")})
		}
		http.SetCookie(w, &http.Cookie{Name: setupCookie, Value: "", Path: "/", MaxAge: -1})
		p.Admin = nil
	}
	s.prog.set(Progress{Running: true, Phase: "Speichern"})
	p.Config = next
	// direct HTTPS goes off: the browser would keep its HTTPS-only cookies and
	// refuse plain ones of the same name, so signing in over HTTP would fail.
	// The file is written first, so the cookies only go when the switch comes
	// (a failed save is reported here, while the page can still ask).
	if r.TLS != nil && cur.Server.TLSCert != "" && next.Server.TLSCert == "" {
		save := s.o.Save
		if save == nil {
			save = (*config.Config).Save
		}
		if err := save(next); err != nil {
			s.prog.set(Progress{})
			writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		for _, name := range []string{sessionCookie, cookieName} {
			http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		}
	}
	// the answer goes out first: applying may close this connection (a
	// switch between HTTP and HTTPS)
	writeJSON(w, r, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
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
	set("background.from", func() { n.Background.From = strings.TrimSpace(in.Background.From) })
	set("background.to", func() { n.Background.To = strings.TrimSpace(in.Background.To) })
	set("background.windows", func() { n.Background.Windows = in.Background.Windows })
	set("background.once", func() { n.Background.Once = in.Background.Once })
	set("library.folder_meta", func() { n.Library.FolderMeta = in.Library.FolderMeta })
	set("library.write_files", func() { n.Library.WriteFiles = in.Library.WriteFiles })
	set("library.write_metadata", func() { n.Library.WriteMetadata = in.Library.WriteMetadata })
	set("library.trash_days", func() { n.Library.TrashDays = in.Library.TrashDays })
	set("library.import.dir", func() { n.Library.Import.Dir = config.ExpandHome(strings.TrimSpace(in.Library.Import.Dir)) })
	set("library.import.folder", func() { n.Library.Import.Folder = strings.TrimSpace(in.Library.Import.Folder) }) // name or path, see config
	set("library.documents", func() {
		if in.Library.Documents != nil {
			n.Library.Documents = in.Library.Documents
		}
	})
	set("cache.dir", func() { n.Cache.Dir = config.ExpandHome(strings.TrimSpace(in.Cache.Dir)) })
	set("cache.previews", func() { n.Cache.Previews = in.Cache.Previews })
	set("cache.max_size", func() { n.Cache.MaxSize = strings.TrimSpace(in.Cache.MaxSize) })
	set("cache.overflow_dir", func() { n.Cache.OverflowDir = config.ExpandHome(strings.TrimSpace(in.Cache.OverflowDir)) })
	set("media.workers", func() { n.Media.Workers = in.Media.Workers })
	set("media.video_previews", func() { n.Media.VideoPreviews = in.Media.VideoPreviews })
	set("media.transcode", func() { n.Media.Transcode = in.Media.Transcode })
	set("media.slow_decodes", func() { n.Media.SlowDecodes = in.Media.SlowDecodes })
	// numbers: 0 means the form did not send one
	set("media.thumb_size", func() { n.Media.ThumbSize = cmp.Or(in.Media.ThumbSize, n.Media.ThumbSize) })
	set("media.preview_size", func() { n.Media.PreviewSize = cmp.Or(in.Media.PreviewSize, n.Media.PreviewSize) })
	set("media.jpeg_quality", func() { n.Media.JPEGQuality = cmp.Or(in.Media.JPEGQuality, n.Media.JPEGQuality) })
	set("media.video_preview_size", func() { n.Media.VideoPreviewSize = cmp.Or(in.Media.VideoPreviewSize, n.Media.VideoPreviewSize) })
	set("media.exiftool", func() { n.Media.ExifTool = strings.TrimSpace(in.Media.ExifTool) })
	set("media.ffmpeg", func() { n.Media.FFmpeg = strings.TrimSpace(in.Media.FFmpeg) })
	set("faces.mode", func() { n.Faces.Mode = in.Faces.Mode })
	set("faces.url", func() { n.Faces.URL = strings.TrimSpace(in.Faces.URL) })
	set("faces.token", func() { n.Faces.Token = strings.TrimSpace(in.Faces.Token) })
	set("faces.workers", func() { n.Faces.Workers = cmp.Or(in.Faces.Workers, n.Faces.Workers) })
	set("faces.python", func() { n.Faces.Python = strings.TrimSpace(in.Faces.Python) })
	set("faces.models_dir", func() { n.Faces.ModelsDir = config.ExpandHome(strings.TrimSpace(in.Faces.ModelsDir)) })
	set("worker.listen", func() { n.Worker.Listen = cmp.Or(strings.TrimSpace(in.Worker.Listen), n.Worker.Listen) })
	set("worker.token", func() { n.Worker.Token = strings.TrimSpace(in.Worker.Token) })
	set("worker.workers", func() { n.Worker.Workers = cmp.Or(in.Worker.Workers, n.Worker.Workers) })
	// the thresholds (Feinabstimmung); 0 means the form did not send one
	set("faces.min_score", func() { n.Faces.MinScore = cmp.Or(in.Faces.MinScore, n.Faces.MinScore) })
	set("faces.cluster_threshold", func() { n.Faces.ClusterThreshold = cmp.Or(in.Faces.ClusterThreshold, n.Faces.ClusterThreshold) })
	set("faces.named_threshold", func() { n.Faces.NamedThreshold = cmp.Or(in.Faces.NamedThreshold, n.Faces.NamedThreshold) })
	set("faces.same_photo_threshold", func() {
		n.Faces.SamePhotoThreshold = cmp.Or(in.Faces.SamePhotoThreshold, n.Faces.SamePhotoThreshold)
	})
	set("dupes.mode", func() {
		if in.Dupes.Mode != "" {
			n.Dupes.Mode = in.Dupes.Mode
		}
	})
	set("dupes.sensitivity", func() {
		if in.Dupes.Sensitivity != "" {
			n.Dupes.Sensitivity = in.Dupes.Sensitivity
		}
	})
	set("dupes.similar_minutes", func() {
		if in.Dupes.SimilarMinutes > 0 {
			n.Dupes.SimilarMinutes = in.Dupes.SimilarMinutes
		}
	})
	set("ai.mode", func() { n.AI.Mode = in.AI.Mode })
	set("ai.url", func() { n.AI.URL = strings.TrimRight(strings.TrimSpace(in.AI.URL), "/") })
	set("ai.model", func() { n.AI.Model = strings.TrimSpace(in.AI.Model) })
	set("ai.workers", func() { n.AI.Workers = in.AI.Workers })
	set("ai.languages", func() { n.AI.Languages = in.AI.Languages })
	set("ai.image_size", func() { n.AI.ImageSize = cmp.Or(in.AI.ImageSize, n.AI.ImageSize) })
	set("ai.thinking", func() { n.AI.Thinking = in.AI.Thinking })
	set("ai.retry_failed", func() { n.AI.RetryFailed = strings.TrimSpace(in.AI.RetryFailed) })
	set("ai.ocr", func() {
		if in.AI.OCR != "" {
			n.AI.OCR = in.AI.OCR
		}
	})
	set("ai.ocr_model", func() { n.AI.OCRModel = strings.TrimSpace(in.AI.OCRModel) })
	set("ai.ocr_workers", func() { n.AI.OCRWorkers = in.AI.OCRWorkers })
	set("ai.speech", func() {
		if in.AI.Speech != "" {
			n.AI.Speech = in.AI.Speech
		}
	})
	set("ai.speech_model", func() { n.AI.SpeechModel = strings.TrimSpace(in.AI.SpeechModel) })
	set("ai.speech_fallback", func() { n.AI.SpeechFallback = strings.TrimSpace(in.AI.SpeechFallback) })
	set("ai.speech_workers", func() { n.AI.SpeechWorkers = in.AI.SpeechWorkers })
	// the key field takes the key itself or $VARIABLE (the key stays in the environment)
	if cur.Locked("ai.api_key") == "" && cur.Locked("ai.api_key_env") == "" {
		k := strings.TrimSpace(in.AI.APIKey)
		switch {
		case k == "********":
		case strings.HasPrefix(k, "$"):
			n.AI.APIKey, n.AI.APIKeyEnv = "", strings.Trim(k, "${}")
		default:
			n.AI.APIKey = k
			if k != "" || in.AI.APIKeyEnv == "" {
				n.AI.APIKeyEnv = ""
			}
		}
	}
	set("server.listen", func() { n.Server.Listen = strings.TrimSpace(in.Server.Listen) })
	set("server.public_url", func() { n.Server.PublicURL = strings.TrimRight(strings.TrimSpace(in.Server.PublicURL), "/") })
	set("server.tls_cert", func() { n.Server.TLSCert = config.ExpandHome(strings.TrimSpace(in.Server.TLSCert)) })
	set("server.tls_key", func() { n.Server.TLSKey = config.ExpandHome(strings.TrimSpace(in.Server.TLSKey)) })
	set("ui.map_tiles", func() { n.UI.MapTiles = cmp.Or(strings.TrimSpace(in.UI.MapTiles), config.Defaults().UI.MapTiles) }) // empty: the default map
	set("ui.map_attribution", func() { n.UI.MapAttribution = strings.TrimSpace(in.UI.MapAttribution) })
	set("backup.dir", func() { n.Backup.Dir = config.ExpandHome(strings.TrimSpace(in.Backup.Dir)) })
	set("backup.interval", func() { n.Backup.Interval = in.Backup.Interval })
	set("backup.originals", func() { n.Backup.Originals = in.Backup.Originals })
	set("backup.cache", func() { n.Backup.Cache = in.Backup.Cache })
	set("backup.config", func() { n.Backup.Config = in.Backup.Config })
	set("access.anonymous", func() { n.Access.Anonymous = in.Access.Anonymous })
	set("access.register", func() { n.Access.Register = in.Access.Register })
	set("ui.title", func() { n.UI.Title = strings.TrimSpace(in.UI.Title) })
	set("ui.theme", func() { n.UI.Theme = in.UI.Theme })
	set("ui.language", func() {
		if in.UI.Language != "" {
			n.UI.Language = in.UI.Language
		}
	})
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
