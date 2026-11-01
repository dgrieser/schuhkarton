package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/internal/gallery"
	"github.com/dgrieser/schuhkarton/internal/pathtpl"
)

// Filing rules (admins): read, preview, save and apply.

var rulesSaveMu sync.Mutex

func (s *Server) rulesRoutes(mux *http.ServeMux, admin func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/admin/rules", admin(s.adminRules))
	mux.HandleFunc("POST /api/admin/rules", admin(s.adminSaveRules))
	mux.HandleFunc("POST /api/admin/rules/preview", admin(s.adminPreviewRules))
	mux.HandleFunc("POST /api/admin/rules/apply", admin(s.adminApplyRules))
}

func (s *Server) adminRules(w http.ResponseWriter, r *http.Request) {
	st := s.lib.Status()
	writeJSON(w, r, map[string]any{
		"rules":   s.lib.Rules(),
		"locales": pathtpl.Locales(),
		"fields":  pathtpl.Fields,
		"locked":  s.rulesLocked(),
		"saved":   s.cfg.Path() != "",
		"running": st.RulesRunning, "done": st.RulesDone, "todo": st.RulesTodo,
	})
}

func readRules(r *http.Request) (gallery.RuleSet, error) {
	var rs gallery.RuleSet
	if err := readJSON(r, &rs); err != nil {
		return rs, err
	}
	if rs.Locale == "" {
		rs.Locale = "de"
	}
	if len(rs.List) > 50 {
		return rs, errors.New("höchstens 50 Regeln")
	}
	return rs, nil
}

func (s *Server) adminPreviewRules(w http.ResponseWriter, r *http.Request) {
	rs, err := readRules(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r, s.lib.PreviewRules(rs))
}

// adminSaveRules checks the rules, writes them into the configuration file
// and uses them right away.
func (s *Server) adminSaveRules(w http.ResponseWriter, r *http.Request) {
	rs, err := readRules(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for i, x := range rs.List {
		if strings.TrimSpace(x.Path) == "" {
			writeJSON(w, r, map[string]any{"ok": false, "error": fmt.Sprintf("Regel %d: Ordner fehlt", i+1)})
			return
		}
	}
	if c, errs := gallery.CompileRules(rs); c == nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": errors.Join(errs...).Error()})
		return
	}
	rulesSaveMu.Lock()
	defer rulesSaveMu.Unlock()
	if s.rulesLocked() {
		writeJSON(w, r, map[string]any{"ok": false, "error": "Die Regeln sind über Umgebungsvariablen festgelegt"})
		return
	}
	n := s.cfg.Clone()
	n.Library.Rules = config.Rules{Locale: rs.Locale, Auto: rs.Auto}
	for _, x := range rs.List {
		n.Library.Rules.List = append(n.Library.Rules.List, config.Rule{Name: x.Name, When: x.When, Path: x.Path})
	}
	if n.Path() != "" {
		if err := n.Save(); err != nil {
			writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	if err := s.lib.SetRules(rs); err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.cfg.Library.Rules = n.Library.Rules
	writeJSON(w, r, map[string]any{"ok": true})
}

// rulesLocked: an environment variable or flag fixes part of the rules.
func (s *Server) rulesLocked() bool {
	for _, k := range []string{"library.rules.list", "library.rules.locale", "library.rules.auto"} {
		if s.cfg.Locked(k) != "" {
			return true
		}
	}
	return false
}

func (s *Server) adminApplyRules(w http.ResponseWriter, r *http.Request) {
	n, err := s.lib.ApplyRules(userName(r))
	if err != nil {
		writeJSON(w, r, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, map[string]any{"ok": true, "planned": n})
}
