// Package server exposes a gallery.Library over HTTP: the web UI, a JSON API
// and the cached thumbnails, previews and originals.
package server

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net/http"
	"os"
	"path"
	"runtime"
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
	mux.Handle("GET /favicon.ico", faviconHandler(s.files, func() config.UI { return s.cfg.UI }))
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
	mux.HandleFunc("GET /api/people/hidden", s.requireUser(s.hiddenPeople))
	mux.HandleFunc("POST /api/people/hidden/{id}/show", s.requireUser(s.showHidden))
	mux.HandleFunc("POST /api/face/{id}/{idx}", s.requireUser(s.labelFace))
	mux.HandleFunc("POST /api/faces/labels", s.requireUser(s.restoreFaceLabels))
	mux.HandleFunc("GET /api/faces/quiz", s.requireUser(s.quizFaces))
	mux.HandleFunc("POST /api/faces/hints", s.requireUser(s.restoreHints))
	mux.HandleFunc("POST /api/items/people", s.itemsPeople)
	mux.HandleFunc("POST /api/items/people/{id}", s.requireUser(s.labelPersonIn))
	mux.HandleFunc("POST /api/face/{id}/add", s.requireUser(s.addFace))
	mux.HandleFunc("POST /api/faces/{id}/approve", s.requireUser(s.approveFaces))
	mux.HandleFunc("GET /api/faces/rethink", s.requireAdmin(s.rethinkCount))
	mux.HandleFunc("POST /api/faces/rethink", s.requireAdmin(s.rethinkFaces))
	mux.HandleFunc("GET /api/faces/rematch", s.requireAdmin(s.rematchCount))
	mux.HandleFunc("POST /api/faces/rematch", s.requireAdmin(s.rematchNames))
	mux.HandleFunc("POST /api/people/{id}/rematch", s.requireUser(s.rematchPerson))
	mux.HandleFunc("POST /api/face/{id}/suggest", s.requireUser(s.suggestForBox))
	mux.HandleFunc("GET /api/face/{id}/{idx}/suggest", s.requireUser(s.suggestForFace))
	mux.HandleFunc("POST /api/fav", s.requireUser(s.fav))
	mux.HandleFunc("POST /api/rescan", s.requireAdmin(s.rescan))
	mux.HandleFunc("POST /api/zip", s.zip)
	mux.HandleFunc("GET /api/backup", s.backupStatus)
	mux.HandleFunc("POST /api/backup/run", s.requireAdmin(s.backupRun))
	mux.HandleFunc("GET /t/{id}/{variant}", s.thumb)
	mux.HandleFunc("GET /f/{id}/{idx}", s.face)
	mux.HandleFunc("GET /o/{id}", s.original)
	mux.HandleFunc("GET /print/{id}", s.print)
	mux.HandleFunc("GET /v/{id}", s.video)
	s.accountRoutes(mux)
	s.socialRoutes(mux)
	s.trashRoutes(mux)
	s.adminRoutes(mux, s.requireAdmin)
	s.rulesRoutes(mux, s.requireAdmin)
	if o.Setup != nil {
		mux.Handle("/setup", o.Setup)
		mux.Handle("/api/setup/", o.Setup)
	}
	return slowLog(s.gate(mux))
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
	b = withLogo(withLang(b, s.cfg.UI.Language), currentLogo(s.cfg.UI, true))
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
		"faces": c.Faces.Mode, "videoPreviews": c.Media.VideoPreviews, "transcode": c.Media.Transcode, "ai": c.AI.Mode, "ocr": c.AI.OCR, "speech": c.AI.Speech,
		"version": version.String(), "folders": len(c.Library.Folders),
		"writeFiles": c.Library.WriteFiles, "writeMetadata": c.Library.WriteMetadata, "trashDays": c.Library.TrashDays,
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
	admin := isAdmin(r)
	if !admin {
		st.Import, st.Unreadable = nil, nil // folder paths and file errors are for admins
		// that something failed shows to everybody, what exactly (addresses
		// of services, paths) only to admins
		for _, e := range []*string{&st.FaceErr, &st.AIError, &st.OCRError, &st.SpeechError, &st.XMPError, &st.RulesError} {
			if *e != "" {
				*e = "Fehler"
			}
		}
	}
	res := map[string]any{"status": st, "scanning": s.lib.Scanning()}
	if s.backup != nil {
		b := s.backup.Status()
		if !admin {
			b.Dir, b.Error = "", ""
		}
		res["backup"] = b
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
	Po     []string `json:"po,omitempty"` // people with a face not approved here
	Approx bool     `json:"ap,omitempty"`
	Doc    bool     `json:"dc,omitempty"`
	Notes  int      `json:"nt,omitempty"` // number of notes
}

func (s *Server) items(w http.ResponseWriter, r *http.Request) {
	user := userName(r)
	items := s.lib.Items()
	approvalOf := s.lib.FaceApprovals()
	out := make([]compactItem, 0, len(items))
	for _, it := range items {
		if it.LivePhoto != "" {
			continue
		}
		c := compactItem{ID: it.ID, K: uint8(it.Kind), T: it.Taken, A: round(it.Aspect, 3), V: it.Key()[:8],
			P: it.Place, Al: s.lib.AlbumOf(it), C: it.Category, D: round(it.Duration, 1),
			F: s.lib.IsFav(user, it), X: it.Desc != "", Approx: it.TakenSrc == "folder" || it.TakenSrc == "mtime",
			Doc: s.lib.IsDocument(it), Notes: s.lib.NoteCount(it)}
		if it.GPS {
			c.La, c.Lo = round(it.Lat, 5), round(it.Lon, 5)
		}
		if it.LiveVideo != "" {
			if lv := s.lib.Get(it.LiveVideo); lv != nil {
				c.Lv, c.Lk = lv.ID, lv.Key()[:8]
			}
		}
		faces := s.lib.ItemFaces(it)
		var approved []bool
		var whole bool
		if len(faces) > 0 {
			approved, whole = approvalOf(it)
		}
		seen, open := map[string]bool{}, map[string]bool{}
		for i := range faces {
			pid, _ := s.lib.FacePerson(it.ID, i)
			if pid == "" {
				continue
			}
			if !seen[pid] {
				seen[pid] = true
				c.Pp = append(c.Pp, pid)
			}
			if !whole && i < len(approved) && !approved[i] && !open[pid] {
				open[pid] = true
				c.Po = append(c.Po, pid)
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
		Idx        int     `json:"idx"`
		Person     string  `json:"person,omitempty"`
		Name       string  `json:"name,omitempty"`
		Manual     bool    `json:"manual,omitempty"`
		T          float32 `json:"t,omitempty"` // video: the second it was found at
		V          string  `json:"v"`           // version of the crop: changes with the box
		// OK: approved (by hand, by a name or drawn by hand); Mine: approved
		// with "Bestätigen", so it can be taken back
		OK   bool `json:"ok,omitempty"`
		Mine bool `json:"mine,omitempty"`
	}
	var faces []face
	approved, itemOK := s.lib.FaceApproval(it)
	whole, redo := s.lib.FaceFlags(it)
	for i, f := range s.lib.ItemFaces(it) {
		if s.lib.IsNoFace(it.ID, i) {
			continue // marked as no face
		}
		fc := face{X: f.X, Y: f.Y, W: f.W, H: f.H, Idx: i, Manual: f.Manual, T: f.T, Mine: f.OK}
		fc.OK = i < len(approved) && approved[i]
		fc.V = strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(fmt.Sprint(f.X, f.Y, f.W, f.H, f.T)))), 36)
		fc.Person, fc.Name = s.lib.FacePerson(it.ID, i) // a name just given shows before grouping caught up
		faces = append(faces, fc)
	}
	ext := strings.ToLower(path.Ext(it.Rel))
	// where the file is on this machine is for admins; others see it inside its library folder
	where := path.Join(s.lib.RootName(it), it.Rel)
	if isAdmin(r) {
		where = s.lib.Path(it)
	}
	res := map[string]any{
		"id": it.ID, "kind": it.Kind, "name": path.Base(it.Rel), "album": s.lib.AlbumOf(it), "rel": it.Rel,
		"root": s.lib.RootName(it), "path": where, "size": it.Size, "mtime": it.MTime,
		"taken": it.Taken, "takenSrc": it.TakenSrc, "w": it.W, "h": it.H, "aspect": it.Aspect,
		"gps": it.GPS, "lat": it.Lat, "lon": it.Lon, "alt": it.Alt,
		"make": it.Make, "model": it.Model, "lens": it.Lens, "software": it.Software,
		"fnumber": it.FNumber, "exposure": it.Exposure, "iso": it.ISO, "focal": it.Focal,
		"duration": it.Duration, "codec": it.Codec, "ext": ext,
		"desc": it.Desc, "keywords": it.Keywords, "place": it.Place, "category": it.Category,
		"live": it.LiveVideo, "fav": s.lib.IsFav(userName(r), it), "faces": faces, "v": it.Key()[:8],
		"facesOK": itemOK, "facesMine": whole, "faceRedo": redo,
	}
	if ed := s.lib.EditedFields(it); len(ed) > 0 {
		res["edited"] = ed
	}
	res["notes"] = notesOut(s.lib.NotesOf(it))
	if a := s.lib.AIOf(it); a != nil {
		res["ai"] = map[string]any{"de": a.De, "en": a.En, "tagsDe": a.TagsDe, "tagsEn": a.TagsEn, "people": a.People, "model": a.Model}
	}
	if o := s.lib.OCROf(it); o != nil && o.Text != "" {
		res["ocr"] = o.Text
		// translations: the text in the viewer's language
		for k, v := range map[string]string{"ocrLang": o.Lang, "ocrDe": o.De, "ocrEn": o.En} {
			if v != "" {
				res[k] = v
			}
		}
	}
	if s.lib.OCRApplies(it) {
		res["ocrOn"] = true // admins can have it read again, also without text so far
	}
	if t := s.lib.SpeechText(it); t != "" {
		res["speech"] = t
	}
	if s.lib.SpeechApplies(it) {
		res["speechOn"] = true
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
	hits := s.lib.Search(q, userName(r), r.URL.Query().Get("lang"))
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
	if st.FaceErr != "" && !isAdmin(r) {
		st.FaceErr = "Fehler" // the text names the address of the face service
	}
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
	id := r.PathValue("id")
	old, items := s.personBefore(id)
	if strings.TrimSpace(req.Name) == "-" { // no face: a group of misdetections
		prev, err := s.lib.NoFacePerson(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.recordFaces(r, items, map[string]any{"op": "noface", "from": old, "n": len(prev)}, prev)
		writeJSON(w, r, map[string]any{"ok": true, "undo": prev})
		return
	}
	if strings.TrimSpace(req.Name) == "~" {
		hid, err := s.lib.HidePerson(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChHide, Items: items, Info: map[string]any{"from": old}}, map[string]string{"hid": hid})
		writeJSON(w, r, map[string]any{"ok": true, "hidden": hid})
		return
	}
	before := s.lib.PersonLabels(id)
	if err := s.lib.NamePerson(id, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.recordFaces(r, items, map[string]any{"op": faceOp(req.Name), "from": old, "to": strings.TrimSpace(req.Name), "n": len(before)}, before)
	writeJSON(w, r, map[string]any{"ok": true})
}

// personBefore returns a person's name and some of its items, for the history.
func (s *Server) personBefore(id string) (string, []string) {
	p := s.lib.Person(id)
	if p == nil {
		return "", nil
	}
	items := p.Items
	if len(items) > 24 {
		items = items[:24]
	}
	return p.Name, append([]string(nil), items...)
}

// faceOp names what a face label does: a name, not this person, hidden, no face.
func faceOp(name string) string {
	switch strings.TrimSpace(name) {
	case "":
		return "not"
	case "-":
		return "noface"
	case "~":
		return "hide"
	}
	return "name"
}

// recordFaces keeps a change of face names with the labels before.
func (s *Server) recordFaces(r *http.Request, items []string, info map[string]any, before map[string]string) {
	if len(items) == 0 {
		seen := map[string]bool{}
		for k := range before {
			if id, _, ok := strings.Cut(k, ":"); ok && !seen[id] && len(items) < 24 {
				seen[id] = true
				items = append(items, id)
			}
		}
	}
	s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChFaces, Items: items, Info: info}, before)
}

// hiddenPeople lists the groups hidden with "Ausblenden".
func (s *Server) hiddenPeople(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, map[string]any{"hidden": s.lib.HiddenPeople()})
}

func (s *Server) showHidden(w http.ResponseWriter, r *http.Request) {
	if err := s.lib.ShowHidden(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChShow}, nil)
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) labelFace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		// Group: the rest of the face's group gets the name as a hint (a quick
		// round: one answer names the group, not approved)
		Group bool `json:"group"`
		// Not: the face is not this person (whoever it is grouped with now)
		Not string `json:"not"`
		// NoneNamed: the face is none of the people named now (with Group: its
		// group's faces too)
		NoneNamed bool `json:"noneNamed"`
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || readJSON(r, &req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if it := s.lib.Get(r.PathValue("id")); it == nil || idx < 0 || idx >= len(s.lib.ItemFaces(it)) {
		http.Error(w, "unknown face", http.StatusBadRequest)
		return
	}
	person := s.lib.PersonOf(r.PathValue("id"), idx)
	old, _ := s.personBefore(person)
	if req.NoneNamed {
		prev, err := s.lib.RejectNamed(r.PathValue("id"), idx, req.Group)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		s.recordFaces(r, nil, map[string]any{"op": "nonenamed", "from": old, "n": len(prev)}, prev)
		writeJSON(w, r, map[string]any{"ok": true, "undo": prev, "n": len(prev)})
		return
	}
	if req.Not != "" {
		prev, err := s.lib.RejectFace(r.PathValue("id"), idx, req.Not)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		key := r.PathValue("id") + ":" + strconv.Itoa(idx)
		s.recordFaces(r, []string{r.PathValue("id")}, map[string]any{"op": faceOp(""), "from": old, "to": "", "n": 1}, map[string]string{key: prev})
		writeJSON(w, r, map[string]any{"ok": true, "undo": map[string]string{key: prev}})
		return
	}
	var hints map[string]string
	if req.Group && person != "" {
		// before the label: the group as it is now
		if h, err := s.lib.HintGroup(person, req.Name, gallery.FaceRef{Item: r.PathValue("id"), Idx: idx}); err == nil {
			hints = h
		}
	}
	prev, err := s.lib.LabelFace(r.PathValue("id"), idx, req.Name)
	if err != nil {
		if hints != nil {
			s.lib.RestoreHints(hints)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := r.PathValue("id") + ":" + strconv.Itoa(idx)
	before := map[string]string{key: prev}
	for k, h := range hints { // undone with it in the history
		before[gallery.HintUndoPrefix+k] = h
	}
	s.recordFaces(r, []string{r.PathValue("id")}, map[string]any{"op": faceOp(req.Name), "from": old, "to": strings.TrimSpace(req.Name), "n": 1 + len(hints)}, before)
	// what the face had, for undo (and the hints of its group before)
	res := map[string]any{"ok": true, "undo": map[string]string{r.PathValue("id") + ":" + strconv.Itoa(idx): prev}}
	if hints != nil {
		res["hints"] = hints
		res["group"] = len(hints)
	}
	writeJSON(w, r, res)
}

// quizFaces chooses the faces of a quick round (mode name or approve).
func (s *Server) quizFaces(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	faces, err := s.lib.QuizFaces(r.URL.Query().Get("mode"), n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if faces == nil {
		faces = []gallery.QuizFace{}
	}
	writeJSON(w, r, map[string]any{"faces": faces})
}

// restoreHints sets hints back, as "hints" of an answer of labelFace said (undo).
func (s *Server) restoreHints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hints map[string]string `json:"hints"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.lib.RestoreHints(req.Hints)
	writeJSON(w, r, map[string]any{"ok": true})
}

// approveFaces approves faces of an item, or takes that back: the faces
// listed, or with all every face and the item as a whole.
func (s *Server) approveFaces(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Faces []int `json:"faces"`
		All   bool  `json:"all"`
		OK    bool  `json:"ok"`
		// Undo: sets what was approved back, as "undo" of an answer said
		Undo *gallery.Approval `json:"undo"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Undo != nil {
		if err := s.lib.SetApproval(r.PathValue("id"), *req.Undo); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, r, map[string]any{"ok": true})
		return
	}
	prev, err := s.lib.ApproveFaces(r.PathValue("id"), req.Faces, req.All, req.OK)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "undo": prev})
}

// rethinkCount tells how many photos "Gesichter neu suchen" would look at.
func (s *Server) rethinkCount(w http.ResponseWriter, r *http.Request) {
	n, complete := s.lib.RethinkCount()
	writeJSON(w, r, map[string]any{"n": n, "complete": complete})
}

// rethinkFaces looks for the faces of all photos not approved again; with
// complete, also of those whose faces are all approved one by one.
func (s *Server) rethinkFaces(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Complete bool `json:"complete"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n, err := s.lib.RethinkFaces(req.Complete)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "n": n})
}

// rematchCount tells how many faces "Namen neu zuordnen" would look at.
func (s *Server) rematchCount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, map[string]any{"n": s.lib.RematchCount()})
}

// rematchNames matches the names of all faces not approved anew.
func (s *Server) rematchNames(w http.ResponseWriter, r *http.Request) {
	n, err := s.lib.RematchNames()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "n": n})
}

// rematchPerson matches the names of a person's faces not approved anew
// (the photos "Offen" on the person's page), right away.
func (s *Server) rematchPerson(w http.ResponseWriter, r *http.Request) {
	looked, changed, err := s.lib.RematchPerson(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "n": looked, "changed": changed})
}

// addFace adds a face drawn by hand (on a video at second t).
func (s *Server) addFace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		X, Y, W, H float32
		T          float64 `json:"t"`
		Name       string  `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	idx, err := s.lib.AddFace(ctx, r.PathValue("id"), gallery.FaceBox{X: req.X, Y: req.Y, W: req.W, H: req.H}, req.T, req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := r.PathValue("id") + ":" + strconv.Itoa(idx)
	s.recordFaces(r, []string{r.PathValue("id")}, map[string]any{"op": "add", "to": strings.TrimSpace(req.Name), "n": 1}, map[string]string{key: "-"})
	writeJSON(w, r, map[string]any{"ok": true, "idx": idx})
}

// suggestForBox suggests names for a box drawn by hand, before it is saved:
// the named people it looks most alike, best first. recognition false: no
// face recognition, so no suggestions.
func (s *Server) suggestForBox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		X, Y, W, H float32
		T          float64 `json:"t"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	sg, err := s.lib.SuggestForBox(ctx, r.PathValue("id"), gallery.FaceBox{X: req.X, Y: req.Y, W: req.W, H: req.H}, req.T)
	writeSuggestions(w, r, sg, err)
}

// suggestForFace suggests names for a face of an item.
func (s *Server) suggestForFace(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sg, err := s.lib.SuggestForFace(r.PathValue("id"), idx)
	writeSuggestions(w, r, sg, err)
}

func writeSuggestions(w http.ResponseWriter, r *http.Request, sg []gallery.Suggestion, err error) {
	switch {
	case errors.Is(err, gallery.ErrNoRecognition):
		writeJSON(w, r, map[string]any{"recognition": false, "suggestions": []gallery.Suggestion{}})
	case err != nil:
		// the face service is busy or away: the list stays as it is
		writeJSON(w, r, map[string]any{"recognition": true, "error": err.Error(), "suggestions": []gallery.Suggestion{}})
	default:
		writeJSON(w, r, map[string]any{"recognition": true, "suggestions": sg})
	}
}

// itemsPeople lists the people in a selection of items, with the number of
// their faces and items in it.
func (s *Server) itemsPeople(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, map[string]any{"people": s.lib.PeopleIn(req.IDs)})
}

// labelPersonIn names the faces of one person in a selection of items at once
// (a name, "" not this person, "-" no face), with the labels before for undo.
func (s *Server) labelPersonIn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs  []string `json:"ids"`
		Name string   `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	old, _ := s.personBefore(r.PathValue("id"))
	undo, err := s.lib.LabelPersonIn(req.IDs, r.PathValue("id"), req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(undo) > 0 {
		s.recordFaces(r, nil, map[string]any{"op": faceOp(req.Name), "from": old, "to": strings.TrimSpace(req.Name), "n": len(undo)}, undo)
	}
	writeJSON(w, r, map[string]any{"ok": true, "n": len(undo), "undo": undo})
}

// restoreFaceLabels sets face labels back (undo of "no face").
func (s *Server) restoreFaceLabels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Labels map[string]string `json:"labels"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	keys := make([]string, 0, len(req.Labels))
	for k := range req.Labels {
		keys = append(keys, k)
	}
	before := s.lib.FaceLabels(keys)
	if err := s.lib.RestoreFaceLabels(req.Labels); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.recordFaces(r, nil, map[string]any{"op": "restore", "n": len(keys)}, before)
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
	user := userName(r)
	changing := s.lib.Changing(req.IDs, req.On, func(it *gallery.Item) bool { return s.lib.IsFav(user, it) })
	if err := s.lib.SetFav(user, req.IDs, req.On); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(changing) > 0 {
		s.lib.Record(gallery.Change{By: user, Kind: gallery.ChFav, Items: changing, Names: s.lib.Names(changing), Info: map[string]any{"on": req.On, "n": len(changing)}},
			gallery.ToggleUndo{User: user, On: req.On})
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
		if s.lib.Unreadable(it) != nil { // reading it would freeze the share
			continue
		}
		f, err := s.lib.OpenOriginal(it)
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
	if errors.Is(err, gallery.ErrTier2Away) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=3600")
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
	// a crop can change at the same address when the faces are found again:
	// kept long only under a version (?v=, the face's box)
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "private, max-age=86400")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=3600")
	}
	http.ServeFile(w, r, p)
}

func contentType(it *gallery.Item) string {
	ext := it.Ext() // by the content when the name says another format
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
	if err := s.lib.Unreadable(it); err != nil { // reading it would freeze the share
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	f, err := s.lib.OpenOriginal(it)
	if err != nil {
		http.Error(w, "nicht gefunden", http.StatusNotFound) // not the error: it names the path
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

// print returns the picture for printing at full size, turned like in the
// viewer: the original itself when the browser shows it as it is, else a
// JPEG (browsers print neither HEIC nor RAW).
func (s *Server) print(w http.ResponseWriter, r *http.Request) {
	it := s.lib.Get(r.PathValue("id"))
	if it == nil {
		http.NotFound(w, r)
		return
	}
	if s.lib.PrintAsIs(it) && s.lib.Unreadable(it) == nil { // else the preview from the cache (PrintImage)
		s.serveOriginal(w, r, it)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	b, err := s.lib.PrintImage(ctx, it)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(b)
}

// transcodeSlots: videos converted for browsers at the same time.
var transcodeSlots = make(chan struct{}, max(2, runtime.NumCPU()))

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
	cached, err := s.lib.CachedPath(it, gallery.VarH264)
	if errors.Is(err, gallery.ErrTier2Away) {
		// it may be converted already: not again while the share hangs
		w.Header().Set("Retry-After", "30")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if cached != "" {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeFile(w, r, cached)
		return
	}
	if err := s.lib.Unreadable(it); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// each one is an ffmpeg encoding at full speed: a few at a time
	select {
	case transcodeSlots <- struct{}{}:
		defer func() { <-transcodeSlots }()
	default:
		w.Header().Set("Retry-After", "20")
		http.Error(w, "Gerade werden schon Videos umgewandelt, bitte gleich noch einmal", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.lib.StreamH264(r.Context(), it, w); err != nil && r.Context().Err() == nil {
		log.Printf("transcode %s: %v", it.Rel, err)
	}
}
