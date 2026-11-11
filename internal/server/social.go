package server

import (
	"net/http"

	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// socialRoutes: notes on photos (everybody reads, accounts write).
func (s *Server) socialRoutes(mux *http.ServeMux) {
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
	id := r.PathValue("id")
	n, err := s.lib.AddNote(userName(r), id, text)
	if err == nil {
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChNote, Items: []string{id}, Names: s.lib.Names([]string{id}), Info: map[string]any{"op": "add", "text": clip(n.Text, 120)}},
			gallery.NoteUndo{Op: "add", Item: id, Note: n.ID, User: n.User})
	}
	s.noteResult(w, r, n, err)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) {
	text, ok := s.noteText(w, r)
	if !ok {
		return
	}
	id, nid := r.PathValue("id"), r.PathValue("note")
	n, old, err := s.lib.UpdateNoteWas(userName(r), id, nid, text)
	if err == nil {
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChNote, Items: []string{id}, Names: s.lib.Names([]string{id}), Info: map[string]any{"op": "edit", "text": clip(n.Text, 120)}},
			gallery.NoteUndo{Op: "edit", Item: id, Note: nid, User: old.User, Text: old.Text})
	}
	s.noteResult(w, r, n, err)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	id, nid := r.PathValue("id"), r.PathValue("note")
	old, err := s.lib.DeleteNoteWas(userName(r), isAdmin(r), id, nid)
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChNote, Items: []string{id}, Names: s.lib.Names([]string{id}), Info: map[string]any{"op": "delete", "text": clip(old.Text, 120), "of": old.User}},
		gallery.NoteUndo{Op: "delete", Item: id, User: old.User, Text: old.Text, Was: &old})
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
	s.queue(w, r, gallery.OpDupes, gallery.DupesArgs{Groups: req.Groups})
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
