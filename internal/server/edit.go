package server

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// adminRoutes registers the admin tools: editing metadata, rotating, moving,
// duplicates and the history of changes (the trash is for everybody: trash.go).
func (s *Server) adminRoutes(mux *http.ServeMux, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/admin/edit", admin(s.adminEdit))
	mux.HandleFunc("POST /api/admin/unreadable/retry", admin(s.adminRetryUnreadable))
	mux.HandleFunc("POST /api/admin/rotate", admin(s.adminRotate))
	mux.HandleFunc("GET /api/admin/folders", admin(s.adminFolders))
	mux.HandleFunc("POST /api/admin/folders", admin(s.adminMkdir))
	mux.HandleFunc("POST /api/admin/move", admin(s.adminMove))
	mux.HandleFunc("POST /api/admin/describe", admin(s.adminDescribe))
	mux.HandleFunc("POST /api/admin/ocr", admin(s.adminOCR))
	mux.HandleFunc("POST /api/admin/speech", admin(s.adminSpeech))
	mux.HandleFunc("GET /api/admin/dupes", admin(s.adminDupes))
	mux.HandleFunc("POST /api/admin/dupes/resolve", admin(s.adminResolveDupes))
	mux.HandleFunc("POST /api/admin/dupes/ignore", admin(s.adminIgnoreDupes))
	mux.HandleFunc("POST /api/admin/dupes/rescan", admin(s.adminRescanDupes))
	mux.HandleFunc("GET /api/admin/history", admin(s.adminHistory))
	mux.HandleFunc("POST /api/admin/history/{id}/undo", admin(s.adminUndo))
}

// adminHistory lists the changes kept, newest first (before: those after the
// change with this id; kind: comma separated kinds; by: an account, "-" for Schuhkarton).
func (s *Server) adminHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, days, more := s.lib.History(q.Get("before"), limit, q.Get("kind"), q.Get("by"))
	if list == nil {
		list = []gallery.ChangeView{}
	}
	writeJSON(w, r, map[string]any{"changes": list, "days": days, "more": more})
}

// adminUndo undoes one change.
func (s *Server) adminUndo(w http.ResponseWriter, r *http.Request) {
	res, err := s.lib.UndoChange(r.PathValue("id"), userName(r))
	s.adminResult(w, r, res, err)
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
	n := s.lib.RecognizeAgain(gallery.ChAI, req.IDs, userName(r))
	writeJSON(w, r, map[string]any{"ok": true, "changed": n})
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
	n := s.lib.RecognizeAgain(gallery.ChOCR, req.IDs, userName(r))
	writeJSON(w, r, map[string]any{"ok": true, "changed": n})
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
	n := s.lib.RecognizeAgain(gallery.ChSpeech, req.IDs, userName(r))
	writeJSON(w, r, map[string]any{"ok": true, "changed": n})
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
	s.queue(w, r, gallery.OpEdit, gallery.EditArgs{IDs: req.IDs, Edit: e, Info: editInfo(req.Taken, req.Shift, e)})
}

// editInfo describes an edit for the history: which fields, and to what.
func editInfo(taken *string, shift int64, e gallery.Edit) map[string]any {
	info := map[string]any{}
	var fields []string
	if e.Taken != nil {
		fields = append(fields, "taken")
		info["taken"] = *taken
	}
	if shift != 0 {
		fields = append(fields, "taken")
		info["shift"] = shift
	}
	if e.Place != nil {
		fields = append(fields, "place")
		info["place"] = *e.Place
	}
	if e.Category != nil {
		fields = append(fields, "category")
		info["category"] = *e.Category
	}
	if e.Desc != nil {
		fields = append(fields, "desc")
		info["desc"] = clip(*e.Desc, 120)
	}
	if e.GPS != nil {
		fields = append(fields, "gps")
		info["gpsClear"] = e.GPS.Clear
	}
	info["fields"] = fields
	return info
}

// clip shortens a text for the history.
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
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
	if d := ((req.Deg % 360) + 360) % 360; d == 0 || d%90 != 0 {
		writeJSON(w, r, map[string]any{"ok": false, "error": "nur 90, 180 oder 270 Grad"})
		return
	}
	s.queue(w, r, gallery.OpRotate, gallery.RotateArgs{IDs: req.IDs, Deg: req.Deg})
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
	s.queue(w, r, gallery.OpMove, gallery.MoveArgs{IDs: req.IDs, Root: req.Root, Dir: req.Dir})
}

// adminRetryUnreadable tries files the share hung on again (all of them
// without paths).
func (s *Server) adminRetryUnreadable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.lib.RetryUnreadable(req.Paths)
	if s.backup != nil {
		s.backup.Trigger() // their copies too
	}
	go func() { // what a scan left out of them is read now
		if err := s.lib.Scan(s.ctx); err != nil {
			log.Printf("rescan: %v", err)
		}
	}()
	writeJSON(w, r, map[string]any{"ok": true})
}
