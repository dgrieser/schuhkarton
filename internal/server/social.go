package server

import (
	"net/http"
	"path"

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
	user := userName(r)
	changing := s.lib.Changing(req.IDs, req.On, func(it *gallery.Item) bool { return s.lib.IsMarked(user, it) })
	n, err := s.lib.SetMarks(user, req.IDs, req.On)
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(changing) > 0 {
		s.lib.Record(gallery.Change{By: user, Kind: gallery.ChMark, Items: changing, Names: s.lib.Names(changing), Info: map[string]any{"on": req.On, "n": len(changing)}},
			gallery.ToggleUndo{User: user, On: req.On})
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
	before := s.lib.MarksOf(req.IDs)
	n, err := s.lib.ClearMarks(req.IDs)
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if n > 0 {
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChUnmarkAll, Items: req.IDs, Names: s.lib.Names(req.IDs), Info: map[string]any{"n": n}}, before)
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
	res, freed, err := s.lib.ResolveDupes(req.Groups, userName(r))
	if len(res.Entries) > 0 {
		var names []string
		pick := map[string]bool{}
		for _, e := range res.Entries {
			pick[e] = true
		}
		for _, e := range s.lib.TrashList() {
			if pick[e.ID] {
				names = append(names, path.Base(e.Rel))
			}
		}
		s.lib.Record(gallery.Change{By: userName(r), Kind: gallery.ChDelete, Names: names, Info: map[string]any{"n": len(res.Entries), "dupes": true}}, res.Entries)
	}
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
