package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// trashRoutes: every account deletes photos into the trash, sees and brings
// back what it deleted (admins: everything), and empties its trash. Emptying
// only hides: files are deleted for good after library.trash_days, for
// everybody alike.
func (s *Server) trashRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/trash/delete", s.requireUser(s.trashDelete))
	mux.HandleFunc("GET /api/trash", s.requireUser(s.trashList))
	mux.HandleFunc("POST /api/trash/restore", s.requireUser(s.trashRestore))
	mux.HandleFunc("POST /api/trash/clear", s.requireUser(s.trashClear))
	mux.HandleFunc("POST /api/trash/unhide", s.requireUser(s.trashUnhide))
	mux.HandleFunc("GET /api/trash/{id}/thumb", s.requireUser(s.trashThumb))
	mux.HandleFunc("GET /api/ops/{id}", s.requireUser(s.opStatus))
}

func (s *Server) trashDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 {
		http.Error(w, "ids fehlen", http.StatusBadRequest)
		return
	}
	s.queue(w, r, gallery.OpDelete, gallery.DeleteArgs{IDs: req.IDs, Names: s.lib.Names(req.IDs)})
}

func (s *Server) trashList(w http.ResponseWriter, r *http.Request) {
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
	list := s.lib.TrashFor(userName(r), isAdmin(r))
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

func (s *Server) trashRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 {
		http.Error(w, "ids fehlen", http.StatusBadRequest)
		return
	}
	ids := s.lib.MayRestore(userName(r), isAdmin(r), req.IDs) // one's own; admins all
	if len(ids) == 0 {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Nicht im Papierkorb"})
		return
	}
	s.queue(w, r, gallery.OpRestore, gallery.RestoreArgs{Entries: ids})
}

// trashClear empties the trash for this account (ids: only these): they
// leave its view; deleted for good they are only after the days kept.
func (s *Server) trashClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user := userName(r)
	hidden, err := s.lib.HideTrash(user, isAdmin(r), req.IDs)
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(hidden) > 0 {
		s.lib.Record(gallery.Change{By: user, Kind: gallery.ChBinClear, Info: map[string]any{"n": len(hidden)}},
			gallery.BinClearUndo{User: user, Entries: hidden})
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": len(hidden), "entries": hidden})
}

// trashUnhide brings entries back into this account's trash (undo of emptying).
func (s *Server) trashUnhide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 {
		http.Error(w, "ids fehlen", http.StatusBadRequest)
		return
	}
	if err := s.lib.UnhideTrash(userName(r), req.IDs); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": len(req.IDs)})
}

func (s *Server) trashThumb(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e := s.lib.TrashEntryByID(id)
	if e == nil || (!isAdmin(r) && e.By != userName(r)) {
		http.NotFound(w, r)
		return
	}
	p := s.lib.TrashThumb(id)
	if p == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, p)
}

// --- the queue: work that takes a while runs in the background ------------------------

// queue puts work in the background queue and answers with its result when
// it is done within a moment, else with its id ("pending": GET /api/ops/{id}
// waits for it). The work goes on also when the page is closed or
// Schuhkarton restarts.
func (s *Server) queue(w http.ResponseWriter, r *http.Request, kind string, args any) {
	op, err := s.lib.Enqueue(kind, userName(r), isAdmin(r), args)
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.opResult(w, r, s.lib.WaitOp(r.Context(), op.ID, 2*time.Second))
}

// opResult answers with an op: its result once done (like the admin tools
// did before), else that it is pending.
func (s *Server) opResult(w http.ResponseWriter, r *http.Request, op *gallery.Op) {
	if op == nil {
		http.NotFound(w, r)
		return
	}
	out := map[string]any{"op": op.ID, "state": op.State, "version": s.lib.Version()}
	switch op.State {
	case "done", "failed":
		res := gallery.EditResult{}
		if op.Res != nil {
			res = *op.Res
		}
		out["ok"] = op.State == "done"
		if op.Err != "" {
			out["error"] = op.Err
		}
		out["changed"], out["written"], out["warnings"], out["ids"], out["entries"] = res.Changed, res.Written, res.Warnings, res.IDs, res.Entries
		out["moved"], out["queued"], out["freed"] = res.Moved, res.Queued, res.Freed
	default:
		out["ok"], out["pending"] = true, true
	}
	writeJSON(w, r, out)
}

// opStatus answers with an op of this account (admins: any), waiting up to
// ?wait= seconds (at most 25) for it to end.
func (s *Server) opStatus(w http.ResponseWriter, r *http.Request) {
	op := s.lib.OpStatus(r.PathValue("id"))
	if op == nil || (!isAdmin(r) && op.By != userName(r)) {
		http.NotFound(w, r)
		return
	}
	if wait, _ := strconv.Atoi(r.URL.Query().Get("wait")); wait > 0 && op.State != "done" && op.State != "failed" {
		op = s.lib.WaitOp(r.Context(), op.ID, time.Duration(min(wait, 25))*time.Second)
	}
	s.opResult(w, r, op)
}
