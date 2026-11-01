package gallery

// Filing rules: where files should live, from their metadata. Without rules
// nothing moves and the folders stay as they are. A rule has a condition in
// the search syntax ("typ:dokument", empty for everything) and a folder
// template ("{jahr}/{datum:MM MMMM} {ort}"); the first rule that matches a
// file decides its folder. Files move when an admin changes their date,
// place or category (rules.Auto) and when the rules are applied by hand.
// More than one file at a time moves in the background.

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgrieser/schuhkarton/internal/pathtpl"
)

// Rule is one filing rule.
type Rule struct {
	Name string `json:"name"`
	When string `json:"when"` // search syntax; empty: every file
	Path string `json:"path"` // folder template, see internal/pathtpl
}

// RuleSet are the filing rules and how they work.
type RuleSet struct {
	Locale string `json:"locale"` // language of month and weekday names
	Auto   bool   `json:"auto"`   // move right after edits by an admin
	List   []Rule `json:"list"`
}

type compiledRules struct {
	RuleSet
	tpls []*pathtpl.Template
}

// CompileRules checks rules; errs has one entry per rule (nil when fine).
func CompileRules(rs RuleSet) (*compiledRules, []error) {
	c := &compiledRules{RuleSet: rs}
	errs := make([]error, len(rs.List))
	bad := false
	for i, r := range rs.List {
		t, err := pathtpl.Parse(r.Path)
		if err != nil {
			errs[i], bad = err, true
		}
		c.tpls = append(c.tpls, t)
	}
	if !pathtpl.Supported(rs.Locale) {
		return nil, append(errs, fmt.Errorf("unbekannte Sprache %q", rs.Locale))
	}
	if bad {
		return nil, errs
	}
	return c, errs
}

// SetRules replaces the filing rules (an empty list switches them off).
func (l *Library) SetRules(rs RuleSet) error {
	c, errs := CompileRules(rs)
	if c == nil {
		return errors.Join(errs...)
	}
	l.rules.Store(c)
	return nil
}

// Rules returns the filing rules in use.
func (l *Library) Rules() RuleSet {
	if c := l.rules.Load(); c != nil {
		return c.RuleSet
	}
	return RuleSet{Locale: "de", Auto: true}
}

// RuleMove is one file that a rule puts into another folder.
type RuleMove struct {
	ID   string `json:"id"`
	Rule int    `json:"rule"`
	From string `json:"from"` // folder/name now
	To   string `json:"to"`   // folder/name by the rule
	root int
	dir  string
	rel  string
}

// RuleExample shows where a file ends up.
type RuleExample struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

// RuleStat says what one rule does.
type RuleStat struct {
	Matches  int           `json:"matches"`  // files this rule decides
	Moves    int           `json:"moves"`    // of them in another folder now
	Folders  int           `json:"folders"`  // folders the rule fills
	Blocked  int           `json:"blocked"`  // stay: they would stop (or start) being documents
	Excluded int           `json:"excluded"` // stay: the folder is one the scan skips (library.exclude)
	Examples []RuleExample `json:"examples"`
	Error    string        `json:"error,omitempty"`
}

// RulePreview is what the rules would do to the library.
type RulePreview struct {
	Rules     []RuleStat `json:"rules"`
	Moves     int        `json:"moves"`
	Unmatched int        `json:"unmatched"` // files no rule decides (they stay)
	Blocked   int        `json:"blocked"`   // files that stay, see RuleStat.Blocked and Excluded
	Total     int        `json:"total"`
}

const ruleExamples = 4

// PreviewRules shows what rules (saved or not) would do, without moving.
func (l *Library) PreviewRules(rs RuleSet) RulePreview {
	var p RulePreview
	c, errs := CompileRules(rs)
	p.Rules = make([]RuleStat, len(rs.List))
	for i := range p.Rules {
		p.Rules[i].Examples = []RuleExample{}
		if i < len(errs) && errs[i] != nil {
			p.Rules[i].Error = errs[i].Error()
		}
	}
	if c == nil {
		return p
	}
	folders := make([]map[string]bool, len(rs.List))
	for i := range folders {
		folders[i] = map[string]bool{}
	}
	stay := make([][]RuleExample, len(rs.List)) // examples of files already in place
	l.walkRules(c, nil, func(it *Item, rule int, dir string, blocked string) {
		p.Total++
		if rule < 0 {
			p.Unmatched++
			return
		}
		st := &p.Rules[rule]
		st.Matches++
		switch blocked {
		case blockDoc:
			st.Blocked++
		case blockExcluded:
			st.Excluded++
		}
		if blocked != "" {
			p.Blocked++
			return
		}
		folders[rule][dir] = true
		ex := RuleExample{ID: it.ID, From: it.Rel, To: joinRel(dir, path.Base(it.Rel))}
		if ex.From != ex.To {
			st.Moves++
			p.Moves++
			if len(st.Examples) < ruleExamples {
				st.Examples = append(st.Examples, ex)
			}
		} else if len(stay[rule]) < ruleExamples {
			stay[rule] = append(stay[rule], ex)
		}
	})
	for i := range p.Rules {
		p.Rules[i].Folders = len(folders[i])
		// fill up with files that stay, so the result is visible either way
		for _, ex := range stay[i] {
			if len(p.Rules[i].Examples) >= ruleExamples {
				break
			}
			p.Rules[i].Examples = append(p.Rules[i].Examples, ex)
		}
	}
	return p
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// walkRules decides for every file (only: these ids) which rule applies
// (-1: none) and the folder it gives. Live Photo videos follow their photo
// and are left out.
// A destination that would turn a document into a photo or the other way
// round (documents are told by their folder) is blocked, and so is a folder
// the scan skips (library.exclude): the file stays.
func (l *Library) walkRules(c *compiledRules, only map[string]bool, fn func(it *Item, rule int, dir, blocked string)) {
	match := make([]map[string]bool, len(c.List)) // nil: every file
	for i, r := range c.List {
		if strings.TrimSpace(r.When) == "" || strings.TrimSpace(r.When) == "*" {
			continue
		}
		m := map[string]bool{}
		for _, h := range l.Search(r.When, "", "de") {
			m[h.ID] = true
		}
		match[i] = m
	}
	people := map[string][]string{}
	for _, p := range l.People() {
		if p.Name == "" {
			continue
		}
		for _, id := range p.Items {
			people[id] = append(people[id], p.Name)
		}
	}
	for _, it := range l.Items() {
		if it.LivePhoto != "" || (only != nil && !only[it.ID]) {
			continue
		}
		rule := -1
		for i := range c.List {
			if match[i] == nil || match[i][it.ID] {
				rule = i
				break
			}
		}
		if rule < 0 {
			fn(it, -1, "", "")
			continue
		}
		dir := c.tpls[rule].Render(l.ruleValues(it, c.Locale, people[it.ID]))
		blocked := ""
		switch {
		case l.excludedDir(dir):
			blocked = blockExcluded
		case l.isDocumentAt(it.Root, joinRel(dir, path.Base(it.Rel))) != l.IsDocument(it):
			blocked = blockDoc
		}
		fn(it, rule, dir, blocked)
	}
}

const (
	blockDoc      = "doc"
	blockExcluded = "excluded"
)

// excludedDir reports whether a folder path has a part the scan skips.
func (l *Library) excludedDir(dir string) bool {
	for _, p := range strings.Split(dir, "/") {
		if p != "" && l.excluded(p) {
			return true
		}
	}
	return false
}

// ruleValues are the metadata a template can use.
func (l *Library) ruleValues(it *Item, locale string, names []string) pathtpl.Values {
	v := pathtpl.Values{Locale: locale}
	if it.Taken != 0 {
		v.Date = time.Unix(it.Taken, 0).UTC()
	}
	v.Text = func(field string) string {
		switch field {
		case "ort":
			return it.Place
		case "kategorie":
			return it.Category
		case "kamera":
			mk, mo := strings.TrimSpace(it.Make), strings.TrimSpace(it.Model)
			if mk != "" && strings.HasPrefix(strings.ToLower(mo), strings.ToLower(strings.Fields(mk)[0])) {
				return mo // "Canon" + "Canon EOS 5D"
			}
			return strings.TrimSpace(mk + " " + mo)
		case "typ":
			switch {
			case l.IsDocument(it):
				return pathtpl.KindName(locale, "doc")
			case it.Kind == Video:
				return pathtpl.KindName(locale, "video")
			}
			return pathtpl.KindName(locale, "photo")
		case "schlagwort":
			for _, k := range it.Keywords {
				if k = strings.TrimSpace(k); k != "" {
					return k
				}
			}
		case "person":
			n := append([]string(nil), names...)
			sort.Strings(n)
			return strings.Join(n, ", ")
		}
		return ""
	}
	return v
}

// plan lists the files that are not where the rules want them.
func (l *Library) plan(c *compiledRules, only map[string]bool) []RuleMove {
	var out []RuleMove
	l.walkRules(c, only, func(it *Item, rule int, dir, blocked string) {
		if rule < 0 || blocked != "" {
			return
		}
		to := joinRel(dir, path.Base(it.Rel))
		if to == it.Rel {
			return
		}
		out = append(out, RuleMove{ID: it.ID, Rule: rule, From: it.Rel, To: to, root: it.Root, dir: dir, rel: it.Rel})
	})
	return out
}

// --- moving -----------------------------------------------------------------

type rulesJob struct {
	running    atomic.Bool
	done, todo atomic.Int64
	mu         sync.Mutex
	pending    map[string]bool // ids to file once the running job is through
	by         string
}

const rulesChunk = 50

// ApplyRules moves every file the rules put elsewhere, in the background.
// It returns how many files are planned.
func (l *Library) ApplyRules(by string) (int, error) {
	c := l.rules.Load()
	if c == nil || len(c.List) == 0 {
		return 0, errors.New("keine Regeln")
	}
	j := &l.rulesJob
	j.mu.Lock()
	if l.closing.Load() {
		j.mu.Unlock()
		return 0, errors.New("wird beendet")
	}
	if !j.running.CompareAndSwap(false, true) {
		j.mu.Unlock()
		return 0, errors.New("Einsortieren läuft schon")
	}
	l.bg.Add(1) // Close waits for the run
	j.mu.Unlock()
	gen := l.editGen.Load()
	moves := l.plan(c, nil)
	if len(moves) == 0 {
		j.running.Store(false)
		l.bg.Done()
		return 0, nil
	}
	j.done.Store(0)
	j.todo.Store(int64(len(moves)))
	go l.rulesLoop(c, moves, gen, by)
	return len(moves), nil
}

// queueRules files these items in the background (after edits).
func (l *Library) queueRules(ids []string, by string) {
	j := &l.rulesJob
	j.mu.Lock()
	if j.pending == nil {
		j.pending = map[string]bool{}
	}
	for _, id := range ids {
		j.pending[id] = true
	}
	j.by = by
	start := !l.closing.Load() && j.running.CompareAndSwap(false, true)
	if start {
		l.bg.Add(1) // Close waits for the run
	}
	j.mu.Unlock()
	if start {
		j.done.Store(0)
		j.todo.Store(0)
		go l.rulesLoop(nil, nil, 0, by)
	}
}

func (l *Library) takePending() (map[string]bool, string) {
	j := &l.rulesJob
	j.mu.Lock()
	defer j.mu.Unlock()
	p, by := j.pending, j.by
	j.pending = nil
	return p, by
}

func (l *Library) rulesLoop(c *compiledRules, moves []RuleMove, gen int64, by string) {
	defer l.bg.Done()
	j := &l.rulesJob
	for {
		for start := 0; start < len(moves); {
			if l.closing.Load() {
				// stop between chunks: what moved so far is complete
				j.running.Store(false)
				return
			}
			if l.rulesChunkHook != nil {
				l.rulesChunkHook(start)
			}
			l.opMu.Lock()
			// metadata or rules changed since the plan: plan the rest again
			if cur := l.rules.Load(); cur != c || l.editGen.Load() != gen {
				c, gen = cur, l.editGen.Load()
				rest := map[string]bool{}
				for _, m := range moves[start:] {
					rest[m.ID] = true
				}
				var again []RuleMove
				if c != nil && len(c.List) > 0 {
					again = l.plan(c, rest)
				}
				j.todo.Add(int64(len(again) - (len(moves) - start)))
				// the files done so far stay in front, so start still points
				// at the first file of the new plan
				moves = append(moves[:start:start], again...)
			}
			end := min(start+rulesChunk, len(moves))
			if end > start {
				l.moveByRules(moves[start:end], by)
			}
			l.opMu.Unlock()
			j.done.Add(int64(end - start))
			start = end
		}
		moves = nil
		if p, pby := l.takePending(); len(p) > 0 {
			l.opMu.Lock()
			c, gen = l.rules.Load(), l.editGen.Load()
			if c != nil && len(c.List) > 0 {
				moves = l.plan(c, p)
			}
			l.opMu.Unlock()
			by = pby
			j.todo.Add(int64(len(moves)))
			continue
		}
		// edits that came in just now start another round
		j.mu.Lock()
		j.running.Store(false)
		more := len(j.pending) > 0 && !l.closing.Load() && j.running.CompareAndSwap(false, true)
		j.mu.Unlock()
		if !more {
			return
		}
	}
}

// moveByRules moves planned files (with their Live Photo videos); files
// that moved or changed since the plan are left alone. The caller holds opMu.
func (l *Library) moveByRules(moves []RuleMove, by string) EditResult {
	var res EditResult
	favs, idMap := map[string]string{}, map[string]string{}
	var changed []*Item
	from := map[string]bool{}
	for _, m := range moves {
		it := l.Get(m.ID)
		if it == nil || it.Rel != m.rel || it.Root != m.root {
			continue
		}
		g := l.groups([]string{it.ID})
		if len(g) == 0 {
			continue
		}
		l.keepFolderMeta(g[0], m.dir, by)
		dirs := map[string]bool{}
		for _, x := range g[0] {
			dirs[filepath.Dir(l.Path(x))] = true
		}
		moved, err := l.moveGroup(g[0], m.root, m.dir, favs, idMap, &res)
		if err != nil {
			res.warn("%s: %v", path.Base(m.rel), err)
			continue
		}
		if len(moved) > 0 {
			for d := range dirs {
				from[d] = true
			}
		}
		changed = append(changed, moved...)
	}
	if len(changed) > 0 {
		l.finishMoves(favs, idMap, changed)
	} else {
		l.saveEdits()
	}
	for d := range from {
		l.pruneEmptyDirs(d)
	}
	for _, w := range res.Warnings {
		l.logf("Regeln: %s", w)
	}
	res.IDs = idMap
	return res
}

// keepFolderMeta remembers date, place and category that a file only has
// from its folder's name, before it moves to a folder that says otherwise.
func (l *Library) keepFolderMeta(g []*Item, dir, by string) {
	if l.opt.IgnoreFolders {
		return
	}
	type keep struct {
		key        string
		place, cat *string
		taken      *int64
	}
	var keeps []keep
	for _, it := range g {
		n := *it
		n.Rel = joinRel(dir, path.Base(it.Rel))
		l.mu.RLock()
		l.finishItem(&n)
		l.mu.RUnlock()
		k := keep{key: l.favKey(it)}
		if n.Place != it.Place {
			p := it.Place
			k.place = &p
		}
		if n.Category != it.Category {
			c := it.Category
			k.cat = &c
		}
		if n.Taken != it.Taken {
			t := it.Taken
			k.taken = &t
		}
		if k.place != nil || k.cat != nil || k.taken != nil {
			keeps = append(keeps, k)
		}
	}
	if len(keeps) == 0 {
		return
	}
	now := time.Now().Unix()
	l.editMu.Lock()
	for _, k := range keeps {
		o := l.edits[k.key]
		if o == nil {
			o = &Override{}
			l.edits[k.key] = o
		}
		if k.place != nil {
			o.Place = k.place
		}
		if k.cat != nil {
			o.Category = k.cat
		}
		if k.taken != nil {
			o.Taken = k.taken
		}
		o.At, o.By = now, by
	}
	l.editMu.Unlock()
}

// pruneEmptyDirs removes a folder that moving left empty, and its empty
// parents, up to the library folder.
func (l *Library) pruneEmptyDirs(dir string) {
	for {
		inRoot := false
		for _, r := range l.opt.Roots {
			if rel, err := filepath.Rel(r.Path, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				inRoot = true
			}
		}
		if !inRoot {
			return
		}
		if es, err := os.ReadDir(dir); err != nil || len(es) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// fileByRules files edited items: one photo (with its Live Photo video)
// right away, more in the background. The caller holds opMu.
func (l *Library) fileByRules(ids []string, by string, res *EditResult) {
	c := l.rules.Load()
	if c == nil || !c.Auto || len(c.List) == 0 {
		return
	}
	only := map[string]bool{}
	for _, id := range ids {
		only[id] = true
		if it := l.Get(id); it != nil && it.LivePhoto != "" {
			only[it.LivePhoto] = true
		}
	}
	moves := l.plan(c, only)
	switch {
	case len(moves) == 0:
	case len(moves) == 1:
		r := l.moveByRules(moves, by)
		res.IDs = r.IDs
		res.Warnings = append(res.Warnings, r.Warnings...)
		res.Moved = len(r.IDs)
	default:
		var queued []string
		for _, m := range moves {
			queued = append(queued, m.ID)
		}
		l.queueRules(queued, by)
		res.Queued = len(moves)
	}
}
