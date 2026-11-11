package server

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// adminRoutes registers the admin tools: editing metadata, rotating,
// moving, deleting and the trash.
func (s *Server) adminRoutes(mux *http.ServeMux, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/admin/edit", admin(s.adminEdit))
	mux.HandleFunc("POST /api/admin/unreadable/retry", admin(s.adminRetryUnreadable))
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
	e.Record = editInfo(req.Taken, req.Shift, e) // kept with the values before, for undo
	res, err := s.lib.EditItems(req.IDs, e)
	s.adminResult(w, r, res, err)
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
	res, err := s.lib.RotateAs(req.IDs, req.Deg, userName(r), &gallery.Change{})
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
	names := s.lib.Names(req.IDs)
	res, err := s.lib.Trash(req.IDs, userName(r))
	if len(res.Entries) > 0 {
		// the files are in the trash now: the entry names them, not their old ids
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChDelete, Names: names, Info: map[string]any{"n": len(res.Entries)}}, res.Entries)
	}
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
	if len(res.IDs) > 0 {
		var ids []string
		for _, id := range res.IDs {
			ids = append(ids, id)
		}
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChRestore, Items: ids, Names: s.lib.Names(ids), Info: map[string]any{"n": len(ids)}}, nil)
	}
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
	var gone []*gallery.TrashEntry
	pick := map[string]bool{}
	for _, id := range ids {
		pick[id] = true
	}
	for _, e := range s.lib.TrashList() {
		if req.All || pick[e.ID] {
			gone = append(gone, e)
		}
	}
	res, err := s.lib.EmptyTrash(ids)
	if err == nil && res.Changed > 0 {
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChPurge, Names: gallery.TrashNames(gone), Info: map[string]any{"n": res.Changed}}, nil)
	}
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
	res, err := s.lib.MoveAs(req.IDs, req.Root, req.Dir,
		&gallery.Change{By: userName(r), Info: map[string]any{"to": strings.Trim(req.Root+"/"+req.Dir, "/")}})
	s.adminResult(w, r, res, err)
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
