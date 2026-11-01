package server

import (
	"net/http"

	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// socialRoutes: marks for deletion (each account its own, hidden for it)
// and notes on photos (everybody reads, accounts write).
func (s *Server) socialRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/marks", s.requireUser(s.myMarks))
	mux.HandleFunc("POST /api/marks", s.requireUser(s.setMarks))
	mux.HandleFunc("POST /api/notes/{id}", s.requireUser(s.addNote))
	mux.HandleFunc("POST /api/notes/{id}/{note}", s.requireUser(s.updateNote))
	mux.HandleFunc("PATCH /api/notes/{id}/{note}", s.requireUser(s.updateNote))
	mux.HandleFunc("DELETE /api/notes/{id}/{note}", s.requireUser(s.deleteNote))
}

type noteJSON struct {
	ID     string `json:"id"`
	User   string `json:"user"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
	Edited int64  `json:"edited,omitempty"`
}

func notesOut(ns []gallery.Note) []noteJSON {
	out := make([]noteJSON, 0, len(ns))
	for _, n := range ns {
		out = append(out, noteJSON(n))
	}
	return out
}

func (s *Server) myMarks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, map[string]any{"ids": s.lib.MarkedIDs(userName(r))})
}

func (s *Server) setMarks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
		On  bool     `json:"on"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	n, err := s.lib.SetMarks(userName(r), req.IDs, req.On)
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": n})
}

func (s *Server) adminMarks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, map[string]any{"items": s.lib.AllMarks()})
}

func (s *Server) adminClearMarks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	n, err := s.lib.ClearMarks(req.IDs)
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": n})
}

func (s *Server) noteText(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return "", false
	}
	return req.Text, true
}

func (s *Server) noteResult(w http.ResponseWriter, r *http.Request, n gallery.Note, err error) {
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "note": noteJSON(n)})
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	text, ok := s.noteText(w, r)
	if !ok {
		return
	}
	n, err := s.lib.AddNote(userName(r), r.PathValue("id"), text)
	s.noteResult(w, r, n, err)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) {
	text, ok := s.noteText(w, r)
	if !ok {
		return
	}
	n, err := s.lib.UpdateNote(userName(r), r.PathValue("id"), r.PathValue("note"), text)
	s.noteResult(w, r, n, err)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	if err := s.lib.DeleteNote(userName(r), isAdmin(r), r.PathValue("id"), r.PathValue("note")); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

// --- duplicates (admins) ---------------------------------------------------------

func (s *Server) adminDupes(w http.ResponseWriter, r *http.Request) {
	groups, st := s.lib.Dupes()
	if r.URL.Query().Get("summary") != "" {
		writeJSON(w, r, map[string]any{"status": st})
		return
	}
	var ids []string
	for _, g := range groups {
		ids = append(ids, g.Items...)
	}
	writeJSON(w, r, map[string]any{"status": st, "groups": groups, "members": s.lib.DupeMembers(ids)})
}

func (s *Server) adminResolveDupes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Groups []gallery.DupeResolution `json:"groups"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	res, freed, err := s.lib.ResolveDupes(req.Groups, userName(r))
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error(), "warnings": res.Warnings})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "changed": res.Changed, "entries": res.Entries, "freed": freed,
		"warnings": res.Warnings, "version": s.lib.Version()})
}

func (s *Server) adminIgnoreDupes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "ungültige Anfrage", http.StatusBadRequest)
		return
	}
	if err := s.lib.IgnoreDupes(req.IDs); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true})
}

func (s *Server) adminRescanDupes(w http.ResponseWriter, r *http.Request) {
	s.lib.RescanDupes()
	writeJSON(w, r, map[string]any{"ok": true})
}
