package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// adminRoutes registers the admin tools: editing metadata, rotating,
// moving, deleting and the trash.
func (s *Server) adminRoutes(mux *http.ServeMux, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/admin/edit", admin(s.adminEdit))
	mux.HandleFunc("POST /api/admin/rotate", admin(s.adminRotate))
	mux.HandleFunc("POST /api/admin/delete", admin(s.adminDelete))
	mux.HandleFunc("GET /api/admin/trash", admin(s.adminTrash))
	mux.HandleFunc("POST /api/admin/trash/restore", admin(s.adminRestore))
	mux.HandleFunc("POST /api/admin/trash/empty", admin(s.adminEmpty))
	mux.HandleFunc("GET /api/admin/trash/{id}/thumb", admin(s.adminTrashThumb))
	mux.HandleFunc("GET /api/admin/folders", admin(s.adminFolders))
	mux.HandleFunc("POST /api/admin/folders", admin(s.adminMkdir))
	mux.HandleFunc("POST /api/admin/move", admin(s.adminMove))
	mux.HandleFunc("POST /api/admin/describe", admin(s.adminDescribe))
	mux.HandleFunc("POST /api/admin/ocr", admin(s.adminOCR))
	mux.HandleFunc("POST /api/admin/speech", admin(s.adminSpeech))
	mux.HandleFunc("GET /api/admin/marks", admin(s.adminMarks))
	mux.HandleFunc("POST /api/admin/marks/clear", admin(s.adminClearMarks))
	mux.HandleFunc("GET /api/admin/dupes", admin(s.adminDupes))
	mux.HandleFunc("POST /api/admin/dupes/resolve", admin(s.adminResolveDupes))
	mux.HandleFunc("POST /api/admin/dupes/ignore", admin(s.adminIgnoreDupes))
	mux.HandleFunc("POST /api/admin/dupes/rescan", admin(s.adminRescanDupes))
}

// adminDescribe drops the AI descriptions of ids so they are made again.
func (s *Server) adminDescribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.cfg.AI.Mode != "on" {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Bildbeschreibungen sind ausgeschaltet"})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": s.lib.Redescribe(req.IDs)})
}

// adminOCR drops the recognized texts of ids so they are recognized again.
func (s *Server) adminOCR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.cfg.AI.OCR == "off" {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Texterkennung ist ausgeschaltet"})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": s.lib.Rerecognize(req.IDs)})
}

// adminSpeech drops what was heard in ids so it is recognized again.
func (s *Server) adminSpeech(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.cfg.AI.Speech != "on" {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Spracherkennung ist ausgeschaltet"})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": s.lib.Retranscribe(req.IDs)})
}

// adminResult answers with what an operation did and the new library version.
func (s *Server) adminResult(w http.ResponseWriter, r *http.Request, res gallery.EditResult, err error) {
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error(), "warnings": res.Warnings})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": res.Changed, "written": res.Written,
		"warnings": res.Warnings, "ids": res.IDs, "entries": res.Entries, "moved": res.Moved, "queued": res.Queued,
		"version": s.lib.Version()})
}

// parseWallClock reads "2006-01-02T15:04[:05]" (a datetime-local value) as
// the wall-clock time that Item.Taken stores as UTC.
func parseWallClock(v string) (int64, bool) {
	v = strings.TrimSpace(strings.Replace(v, " ", "T", 1))
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

func (s *Server) adminEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs      []string `json:"ids"`
		Taken    *string  `json:"taken"`
		Shift    int64    `json:"shift"` // seconds
		Place    *string  `json:"place"`
		Category *string  `json:"category"`
		Desc     *string  `json:"desc"`
		Lat      *float64 `json:"lat"`
		Lon      *float64 `json:"lon"`
		ClearGPS bool     `json:"clearGps"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e := gallery.Edit{Shift: req.Shift, Place: req.Place, Category: req.Category, Desc: req.Desc, By: userName(r)}
	if req.Taken != nil && *req.Taken != "" {
		t, ok := parseWallClock(*req.Taken)
		if !ok {
			http.Error(w, "ungültiges Datum", http.StatusBadRequest)
			return
		}
		e.Taken = &t
	}
	switch {
	case req.ClearGPS:
		e.GPS = &gallery.GPSEdit{Clear: true}
	case req.Lat != nil && req.Lon != nil:
		e.GPS = &gallery.GPSEdit{Lat: *req.Lat, Lon: *req.Lon}
	}
	res, err := s.lib.EditItems(req.IDs, e)
	s.adminResult(w, r, res, err)
}

func (s *Server) adminRotate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
		Deg int      `json:"deg"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.lib.Rotate(req.IDs, req.Deg, userName(r))
	s.adminResult(w, r, res, err)
}

func (s *Server) adminDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.lib.Trash(req.IDs, userName(r))
	s.adminResult(w, r, res, err)
}

func (s *Server) adminTrash(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID      string  `json:"id"`
		Root    string  `json:"root"`
		Rel     string  `json:"rel"`
		Deleted int64   `json:"deleted"`
		By      string  `json:"by,omitempty"`
		Kind    uint8   `json:"k"`
		Taken   int64   `json:"t"`
		Aspect  float64 `json:"a"`
		Live    bool    `json:"live,omitempty"`
		Thumb   bool    `json:"thumb"`
	}
	list := s.lib.TrashList()
	out := make([]entry, 0, len(list))
	for _, e := range list {
		x := entry{ID: e.ID, Root: e.Root, Rel: e.Rel, Deleted: e.Deleted, By: e.By, Live: e.Live != nil,
			Thumb: s.lib.TrashThumb(e.ID) != ""}
		if it := e.Item; it != nil {
			x.Kind, x.Taken, x.Aspect = uint8(it.Kind), it.Taken, round(it.Aspect, 3)
		}
		out = append(out, x)
	}
	writeJSON(w, r, map[string]any{"days": s.cfg.Library.TrashDays, "entries": out})
}

func (s *Server) adminRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 {
		http.Error(w, "ids fehlen", http.StatusBadRequest)
		return
	}
	res, err := s.lib.Restore(req.IDs)
	s.adminResult(w, r, res, err)
}

func (s *Server) adminEmpty(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"` // explicit: an empty list never empties everything
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !req.All && len(req.IDs) == 0 {
		http.Error(w, "ids fehlen", http.StatusBadRequest)
		return
	}
	ids := req.IDs
	if req.All {
		ids = nil
	}
	res, err := s.lib.EmptyTrash(ids)
	s.adminResult(w, r, res, err)
}

func (s *Server) adminTrashThumb(w http.ResponseWriter, r *http.Request) {
	p := s.lib.TrashThumb(r.PathValue("id"))
	if p == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, p)
}

func (s *Server) adminFolders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.lib.Folders())
}

func (s *Server) adminMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root string `json:"root"`
		Dir  string `json:"dir"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.lib.MakeFolder(req.Root, req.Dir); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) adminMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs  []string `json:"ids"`
		Root string   `json:"root"`
		Dir  string   `json:"dir"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.lib.Move(req.IDs, req.Root, req.Dir)
	s.adminResult(w, r, res, err)
}
