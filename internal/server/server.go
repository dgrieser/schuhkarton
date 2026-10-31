// Package server exposes a gallery.Library over HTTP: the web UI, a JSON API
// and the cached thumbnails, previews and originals.
package server

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/auth"
	"github.com/dgrieser/schuhkarton/internal/backup"
	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/internal/gallery"
	"github.com/dgrieser/schuhkarton/internal/version"
	"github.com/dgrieser/schuhkarton/web"
)

// Options configure the gallery server.
type Options struct {
	Lib    *gallery.Library
	Config *config.Config
	Token  string         // required from clients when set (link ?t=, then a cookie)
	WebDir string         // serve the UI from this folder instead of the embedded copy
	Setup  http.Handler   // settings page and its API (/setup, /api/setup/)
	Backup *backup.Runner // nil without a backup folder
	Users  *auth.Store    // accounts and sessions
}

// Server serves one library.
type Server struct {
	lib    *gallery.Library
	backup *backup.Runner
	cfg    *config.Config
	users  *auth.Store
	token  string
	ctx    context.Context
	files  fs.FS // UI files: embedded, or a folder during development
}

// New creates the HTTP handler.
func New(ctx context.Context, o Options) http.Handler {
	s := &Server{lib: o.Lib, backup: o.Backup, cfg: o.Config, users: o.Users, token: o.Token, ctx: ctx, files: UIFiles(o.WebDir)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", assetHandler(s.files)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/items", s.items)
	mux.HandleFunc("GET /api/item/{id}", s.item)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/suggest", s.suggest)
	mux.HandleFunc("GET /api/facets", s.facets)
	mux.HandleFunc("GET /api/places", s.places)
	mux.HandleFunc("GET /api/albums", s.albums)
	mux.HandleFunc("GET /api/people", s.people)
	mux.HandleFunc("POST /api/people/{id}", s.requireUser(s.namePerson))
	mux.HandleFunc("POST /api/face/{id}/{idx}", s.requireUser(s.labelFace))
	mux.HandleFunc("POST /api/fav", s.requireUser(s.fav))
	mux.HandleFunc("POST /api/rescan", s.requireAdmin(s.rescan))
	mux.HandleFunc("POST /api/zip", s.zip)
	mux.HandleFunc("GET /api/backup", s.backupStatus)
	mux.HandleFunc("POST /api/backup/run", s.requireAdmin(s.backupRun))
	mux.HandleFunc("GET /t/{id}/{variant}", s.thumb)
	mux.HandleFunc("GET /f/{id}/{idx}", s.face)
	mux.HandleFunc("GET /o/{id}", s.original)
	mux.HandleFunc("GET /v/{id}", s.video)
	s.accountRoutes(mux)
	s.adminRoutes(mux, s.requireAdmin)
	if o.Setup != nil {
		mux.Handle("/setup", o.Setup)
		mux.Handle("/api/setup/", o.Setup)
	}
	return s.gate(mux)
}

// UIFiles returns the embedded UI, or dir during development.
func UIFiles(dir string) fs.FS {
	if dir != "" {
		return os.DirFS(dir)
	}
	return web.FS
}

// --- helpers ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if len(b) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		zw.Write(b)
		zw.Close()
		return
	}
	w.Write(b)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func round(f float64, n int) float64 {
	p := math.Pow10(n)
	return math.Round(f*p) / p
}

// --- static -------------------------------------------------------------------

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.files, "index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b = withLang(b, s.cfg.UI.Language)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

// withLang tells the page the configured default language (auto, de, en);
// a viewer's own choice in the browser wins.
func withLang(page []byte, lang string) []byte {
	if lang == "" {
		lang = "auto"
	}
	return bytes.Replace(page, []byte(`<html lang="de"`), []byte(`<html lang="de" data-lang="`+lang+`"`), 1)
}

func assetHandler(files fs.FS) http.Handler {
	fsrv := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fsrv.ServeHTTP(w, r)
	})
}

// --- API ----------------------------------------------------------------------

// config tells the UI what it needs to know about this installation.
func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	c := s.cfg
	if id := identityOf(r); id.user == nil && !(c.Access.Anonymous && id.keyOK) {
		// the login page only needs the look
		writeJSON(w, r, map[string]any{"title": c.UI.Title, "theme": c.UI.Theme})
		return
	}
	writeJSON(w, r, map[string]any{
		"title": c.UI.Title, "theme": c.UI.Theme, "mapTiles": c.UI.MapTiles, "mapAttribution": c.UI.MapAttribution,
		"faces": c.Faces.Mode, "videoPreviews": c.Media.VideoPreviews, "transcode": c.Media.Transcode,
		"version": version.String(), "folders": len(c.Library.Folders),
		"writeFiles": c.Library.WriteFiles, "trashDays": c.Library.TrashDays,
	})
}

// facets lists what the filter panel offers: years, places, cameras, categories.
func (s *Server) facets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.lib.Facets(r.URL.Query().Get("lang")))
}

func (s *Server) backupStatus(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil {
		writeJSON(w, r, backup.Status{})
		return
	}
	writeJSON(w, r, s.backup.Status())
}

func (s *Server) backupRun(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil {
		http.Error(w, "kein Sicherungsordner eingestellt", http.StatusBadRequest)
		return
	}
	s.backup.Trigger()
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st := s.lib.Status()
	res := map[string]any{"status": st, "scanning": s.lib.Scanning()}
	if s.backup != nil {
		res["backup"] = s.backup.Status()
	}
	writeJSON(w, r, res)
}

type compactItem struct {
	ID     string   `json:"id"`
	K      uint8    `json:"k"`
	T      int64    `json:"t"`
	A      float64  `json:"a"`
	V      string   `json:"v"`
	La     float64  `json:"la,omitempty"`
	Lo     float64  `json:"lo,omitempty"`
	P      string   `json:"p,omitempty"`
	Al     string   `json:"al"`
	C      string   `json:"c,omitempty"`
	D      float64  `json:"d,omitempty"`
	Lv     string   `json:"lv,omitempty"`
	Lk     string   `json:"lk,omitempty"`
	F      bool     `json:"f,omitempty"`
	X      bool     `json:"x,omitempty"`
	Pp     []string `json:"pp,omitempty"`
	Approx bool     `json:"ap,omitempty"`
}

func (s *Server) items(w http.ResponseWriter, r *http.Request) {
	user := userName(r)
	items := s.lib.Items()
	out := make([]compactItem, 0, len(items))
	for _, it := range items {
		if it.LivePhoto != "" {
			continue
		}
		c := compactItem{ID: it.ID, K: uint8(it.Kind), T: it.Taken, A: round(it.Aspect, 3), V: it.Key()[:8],
			P: it.Place, Al: s.lib.AlbumOf(it), C: it.Category, D: round(it.Duration, 1),
			F: s.lib.IsFav(user, it), X: it.Desc != "", Approx: it.TakenSrc == "folder" || it.TakenSrc == "mtime"}
		if it.GPS {
			c.La, c.Lo = round(it.Lat, 5), round(it.Lon, 5)
		}
		if it.LiveVideo != "" {
			if lv := s.lib.Get(it.LiveVideo); lv != nil {
				c.Lv, c.Lk = lv.ID, lv.Key()[:8]
			}
		}
		seen := map[string]bool{}
		for i := range s.lib.ItemFaces(it) {
			if pid := s.lib.PersonOf(it.ID, i); pid != "" && !seen[pid] {
				seen[pid] = true
				c.Pp = append(c.Pp, pid)
			}
		}
		out = append(out, c)
	}
	writeJSON(w, r, map[string]any{"version": s.lib.Version(), "items": out})
}

func (s *Server) item(w http.ResponseWriter, r *http.Request) {
	it := s.lib.Get(r.PathValue("id"))
	if it == nil {
		http.NotFound(w, r)
		return
	}
	type face struct {
		X, Y, W, H float32
		Idx        int    `json:"idx"`
		Person     string `json:"person,omitempty"`
		Name       string `json:"name,omitempty"`
	}
	var faces []face
	for i, f := range s.lib.ItemFaces(it) {
		fc := face{X: f.X, Y: f.Y, W: f.W, H: f.H, Idx: i, Person: s.lib.PersonOf(it.ID, i)}
		if p := s.lib.Person(fc.Person); p != nil {
			fc.Name = p.Name
		}
		faces = append(faces, fc)
	}
	ext := strings.ToLower(path.Ext(it.Rel))
	res := map[string]any{
		"id": it.ID, "kind": it.Kind, "name": path.Base(it.Rel), "album": s.lib.AlbumOf(it), "rel": it.Rel,
		"root": s.lib.RootName(it), "path": s.lib.Path(it), "size": it.Size, "mtime": it.MTime,
		"taken": it.Taken, "takenSrc": it.TakenSrc, "w": it.W, "h": it.H, "aspect": it.Aspect,
		"gps": it.GPS, "lat": it.Lat, "lon": it.Lon, "alt": it.Alt,
		"make": it.Make, "model": it.Model, "lens": it.Lens, "software": it.Software,
		"fnumber": it.FNumber, "exposure": it.Exposure, "iso": it.ISO, "focal": it.Focal,
		"duration": it.Duration, "codec": it.Codec, "ext": ext,
		"desc": it.Desc, "keywords": it.Keywords, "place": it.Place, "category": it.Category,
		"live": it.LiveVideo, "fav": s.lib.IsFav(userName(r), it), "faces": faces, "v": it.Key()[:8],
	}
	if ed := s.lib.EditedFields(it); len(ed) > 0 {
		res["edited"] = ed
	}
	if it.LiveVideo != "" {
		if lv := s.lib.Get(it.LiveVideo); lv != nil {
			res["liveV"] = lv.Key()[:8]
			res["liveCodec"] = lv.Codec
		}
	}
	writeJSON(w, r, res)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	t0 := time.Now()
	hits := s.lib.Search(q, userName(r))
	if len(hits) > 20000 {
		hits = hits[:20000]
	}
	writeJSON(w, r, map[string]any{"q": q, "hits": hits, "ms": time.Since(t0).Milliseconds()})
}

func (s *Server) suggest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.lib.Suggest(r.URL.Query().Get("q"), r.URL.Query().Get("lang")))
}

func (s *Server) places(w http.ResponseWriter, r *http.Request) { writeJSON(w, r, s.lib.Places()) }
func (s *Server) albums(w http.ResponseWriter, r *http.Request) { writeJSON(w, r, s.lib.Albums()) }

func (s *Server) people(w http.ResponseWriter, r *http.Request) {
	st := s.lib.Status()
	writeJSON(w, r, map[string]any{"people": s.lib.People(), "mode": st.FaceMode, "detection": st.FaceOK,
		"recognition": st.FaceRec, "error": st.FaceErr, "faces": st.Faces, "faceTodo": st.FaceTodo})
}

func (s *Server) namePerson(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.lib.NamePerson(r.PathValue("id"), req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) labelFace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || readJSON(r, &req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.lib.LabelFace(r.PathValue("id"), idx, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) fav(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
		On  bool     `json:"on"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.lib.SetFav(userName(r), req.IDs, req.On); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) rescan(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := s.lib.Scan(s.ctx); err != nil {
			log.Printf("rescan: %v", err)
		}
	}()
	writeJSON(w, r, map[string]any{"ok": true})
}

// zip streams the selected originals (and Live Photo videos) as one archive.
func (s *Server) zip(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var items []*gallery.Item
	for _, id := range strings.Split(r.FormValue("ids"), ",") {
		if it := s.lib.Get(strings.TrimSpace(id)); it != nil {
			items = append(items, it)
			if it.LiveVideo != "" {
				if lv := s.lib.Get(it.LiveVideo); lv != nil {
					items = append(items, lv)
				}
			}
		}
	}
	if len(items) == 0 {
		http.Error(w, "nothing selected", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="schuhkarton-%s.zip"`, time.Now().Format("2006-01-02-150405")))
	zw := zip.NewWriter(w)
	used := map[string]bool{}
	for _, it := range items {
		name := path.Base(it.Rel)
		for n := 1; used[name]; n++ {
			ext := path.Ext(it.Rel)
			name = fmt.Sprintf("%s_%d%s", strings.TrimSuffix(path.Base(it.Rel), ext), n, ext)
		}
		used[name] = true
		f, err := os.Open(s.lib.Path(it))
		if err != nil {
			continue
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Unix(it.MTime, 0)}
		dst, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(dst, f)
		}
		f.Close()
		if err != nil {
			return
		}
	}
	zw.Close()
}

// --- media --------------------------------------------------------------------

func (s *Server) thumb(w http.ResponseWriter, r *http.Request) {
	it := s.lib.Get(r.PathValue("id"))
	if it == nil {
		http.NotFound(w, r)
		return
	}
	variant := strings.TrimSuffix(strings.TrimSuffix(r.PathValue("variant"), ".jpg"), ".mp4")
	switch variant {
	case gallery.VarSmall, gallery.VarLarge, gallery.VarPreview:
	default:
		http.NotFound(w, r)
		return
	}
	if variant == gallery.VarPreview && it.Kind != gallery.Video {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p, err := s.lib.Ensure(ctx, it, variant)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeFile(w, r, p)
}

func (s *Server) face(w http.ResponseWriter, r *http.Request) {
	it := s.lib.Get(r.PathValue("id"))
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if it == nil || err != nil {
		http.NotFound(w, r)
		return
	}
	p := s.lib.FacePath(it, idx)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, p)
}

func contentType(it *gallery.Item) string {
	ext := strings.ToLower(filepath.Ext(it.Rel))
	switch ext {
	case ".heic", ".heif":
		return "image/heic"
	case ".mov", ".m4v":
		if it.Codec == "" || strings.Contains(it.Codec, "avc") || strings.Contains(it.Codec, "h264") {
			return "video/mp4" // lets Chrome and Firefox play H.264 QuickTime files
		}
		return "video/quicktime"
	case ".3gp":
		return "video/3gpp"
	case ".mts", ".m2ts":
		return "video/mp2t"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func (s *Server) original(w http.ResponseWriter, r *http.Request) {
	it := s.lib.Get(r.PathValue("id"))
	if it == nil {
		http.NotFound(w, r)
		return
	}
	s.serveOriginal(w, r, it)
}

func (s *Server) serveOriginal(w http.ResponseWriter, r *http.Request, it *gallery.Item) {
	f, err := os.Open(s.lib.Path(it))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType(it))
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(it.Rel)}))
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, path.Base(it.Rel), time.Unix(it.MTime, 0), f)
}

// video plays the original, or a cached / live H.264 transcode with ?h264=1.
func (s *Server) video(w http.ResponseWriter, r *http.Request) {
	it := s.lib.Get(r.PathValue("id"))
	if it == nil || it.Kind != gallery.Video {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("h264") == "" {
		s.serveOriginal(w, r, it)
		return
	}
	if !s.cfg.Media.Transcode {
		http.Error(w, "Umwandeln ist ausgeschaltet", http.StatusNotFound)
		return
	}
	cached := s.lib.CachePath(it, gallery.VarH264)
	if _, err := os.Stat(cached); err == nil {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeFile(w, r, cached)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.lib.StreamH264(r.Context(), it, w); err != nil && r.Context().Err() == nil {
		log.Printf("transcode %s: %v", it.Rel, err)
	}
}
