package gallery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// FolderTree lists the folders of one root, for picking a move target.
type FolderTree struct {
	Root string   `json:"root"`
	Dirs []string `json:"dirs"` // slash separated, relative to the root, sorted
}

// Folders lists every folder of every root (also empty ones), without
// excluded folders and the trash.
func (l *Library) Folders() []FolderTree {
	var out []FolderTree
	for _, r := range l.opt.Roots {
		t := FolderTree{Root: r.Name, Dirs: []string{}}
		filepath.WalkDir(r.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() && p != r.Path {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() || p == r.Path {
				return nil
			}
			if l.excluded(d.Name()) || d.Name() == TrashDir {
				return fs.SkipDir
			}
			rel, _ := filepath.Rel(r.Path, p)
			t.Dirs = append(t.Dirs, filepath.ToSlash(rel))
			return nil
		})
		sort.Strings(t.Dirs)
		out = append(out, t)
	}
	return out
}

// cleanDir checks a folder given by the UI: relative, inside the root, not
// the trash. "" is the root itself.
func (l *Library) cleanDir(root, dir string) (int, string, error) {
	ri := -1
	for i, r := range l.opt.Roots {
		if r.Name == root {
			ri = i
		}
	}
	if ri < 0 {
		return 0, "", fmt.Errorf("unbekannter Ordner %q", root)
	}
	dir = strings.Trim(strings.ReplaceAll(strings.TrimSpace(dir), `\`, "/"), "/")
	if dir == "" {
		return ri, "", nil
	}
	clean := path.Clean(dir)
	for _, part := range strings.Split(clean, "/") {
		if part == ".." || part == "." || part == "" || part == TrashDir || strings.ContainsAny(part, "\x00") {
			return 0, "", fmt.Errorf("ungültiger Ordner %q", dir)
		}
	}
	return ri, clean, nil
}

// MakeFolder creates a folder inside a root.
func (l *Library) MakeFolder(root, dir string) error {
	ri, d, err := l.cleanDir(root, dir)
	if err != nil {
		return err
	}
	if d == "" {
		return errors.New("Name fehlt")
	}
	return os.MkdirAll(filepath.Join(l.opt.Roots[ri].Path, filepath.FromSlash(d)), 0o755)
}

// moveFile renames src to dst, or copies and deletes across file systems.
// It never overwrites dst.
func moveFile(src, dst string) error {
	if exists(dst) {
		return fmt.Errorf("%s gibt es schon", filepath.Base(dst))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		os.Remove(dst)
		return err
	}
	if cst, err := os.Stat(dst); err != nil || cst.Size() != st.Size() {
		os.Remove(dst)
		return errors.New("Kopie unvollständig")
	}
	if err := os.Remove(src); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// sidecars returns files that belong to a photo: IMG_1.xmp, IMG_1.JPG.xmp,
// IMG_1.aae (any case), as names in the same folder.
func sidecars(abs string) []string {
	dir, name := filepath.Split(abs)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || n == name || e.Type()&fs.ModeSymlink != 0 { // a link is never a sidecar
			continue
		}
		ext := strings.ToLower(filepath.Ext(n))
		if ext != ".xmp" && ext != ".aae" {
			continue
		}
		base := strings.TrimSuffix(n, filepath.Ext(n))
		if base == stem || base == name {
			out = append(out, n)
		}
	}
	return out
}

// moveWithSidecars moves a file and its sidecars; the sidecars follow the
// new name of the file. A sidecar that cannot move stays where it is.
func moveWithSidecars(src, dst string) error {
	_, err := moveWithSidecarsErr(src, dst)
	return err
}

// moveWithSidecarsErr is moveWithSidecars that also reports the first
// sidecar that could not move (the file itself moved).
func moveWithSidecarsErr(src, dst string) (sideErr, err error) {
	side := sidecars(src)
	if err := moveFile(src, dst); err != nil {
		return nil, err
	}
	sdir, sname := filepath.Split(src)
	sstem := strings.TrimSuffix(sname, filepath.Ext(sname))
	ddir, dname := filepath.Split(dst)
	dstem := strings.TrimSuffix(dname, filepath.Ext(dname))
	for _, n := range side {
		base := strings.TrimSuffix(n, filepath.Ext(n))
		to := dstem + filepath.Ext(n)
		if base == sname {
			to = dname + filepath.Ext(n)
		} else if base != sstem {
			continue
		}
		if err := moveFile(filepath.Join(sdir, n), filepath.Join(ddir, to)); err != nil && sideErr == nil {
			sideErr = fmt.Errorf("Begleitdatei %s: %w", n, err)
		}
	}
	return sideErr, nil
}

// freeStem returns stem, or "stem (2)", "stem (3)" …, so that stem+ext is
// free in dir for every ext (a Live Photo pair keeps one name).
func freeStem(dir, stem string, exts []string) string {
	cand := stem
	for n := 2; ; n++ {
		free := true
		for _, ext := range exts {
			if exists(filepath.Join(dir, cand+ext)) {
				free = false
				break
			}
		}
		if free {
			return cand
		}
		cand = fmt.Sprintf("%s (%d)", stem, n)
	}
}

// group returns the requested items with their Live Photo videos, one
// group per photo (videos asked for on their own form their own group).
func (l *Library) groups(ids []string) [][]*Item {
	seen := map[string]bool{}
	var out [][]*Item
	for _, id := range ids {
		it := l.Get(id)
		if it == nil || seen[it.ID] {
			continue
		}
		if it.LivePhoto != "" {
			if p := l.Get(it.LivePhoto); p != nil {
				it = p // move the pair through its photo
				if seen[it.ID] {
					continue
				}
			}
		}
		seen[it.ID] = true
		g := []*Item{it}
		if it.LiveVideo != "" {
			if lv := l.Get(it.LiveVideo); lv != nil && !seen[lv.ID] {
				seen[lv.ID] = true
				g = append(g, lv)
			}
		}
		out = append(out, g)
	}
	return out
}

// relocate gives a moved item its new place and carries along everything
// keyed by its path or id: cached files, edits, favorites, face names.
func (l *Library) relocate(it *Item, ri int, rel string, favs, ids map[string]string) {
	oldFav, oldID, oldKey := l.favKey(it), it.ID, it.key
	name := l.opt.Roots[ri].Name
	l.mu.Lock()
	it.Root, it.RootName, it.Rel = ri, name, rel
	it.ID = itemID(name, rel)
	l.mu.Unlock()
	favs[oldFav] = l.favKey(it)
	if oldID != it.ID {
		ids[oldID] = it.ID
		l.importJob.followImported(oldID, it.ID)
	}
	l.rekey(it, oldKey, 0)
}

// Move moves items (Live Photo videos and sidecar files along) into a
// folder of a root. Names that are taken get " (2)". The folder may be new;
// Schuhkarton imposes no layout.
func (l *Library) Move(ids []string, root, dir string) (EditResult, error) {
	return l.MoveAs(ids, root, dir, nil)
}

// MoveAs moves like Move; with rec the move is kept in the history (rec gives
// who and what for), together with where the files were, in the same locked
// step, so a move right after it cannot slip in between.
func (l *Library) MoveAs(ids []string, root, dir string, rec *Change) (EditResult, error) {
	var res EditResult
	ri, d, err := l.cleanDir(root, dir)
	if err != nil {
		return res, err
	}
	l.opMu.Lock()
	defer l.opMu.Unlock()
	return l.moveLocked(ids, ri, d, rec)
}

// moveLocked moves like MoveAs into folder d of root ri (cleaned); the caller
// holds opMu.
func (l *Library) moveLocked(ids []string, ri int, d string, rec *Change) (EditResult, error) {
	var res EditResult
	var froms []MoveFrom
	if rec != nil {
		froms = l.MoveFroms(ids)
	}
	favs, idMap := map[string]string{}, map[string]string{}
	var changed []*Item
	for _, g := range l.groups(ids) {
		moved, err := l.moveGroup(g, ri, d, favs, idMap, &res)
		if err != nil {
			return res, err
		}
		changed = append(changed, moved...)
	}
	l.finishMoves(favs, idMap, changed)
	res.IDs = idMap
	if back, moved := MovedTo(froms, idMap); rec != nil && len(moved) > 0 {
		c := *rec
		c.Kind, c.Items, c.Names = ChMove, moved, l.Names(moved)
		if c.Info == nil {
			c.Info = map[string]any{}
		}
		c.Info["n"] = len(moved)
		l.Record(c, back)
	}
	return res, nil
}

// MoveBack moves items back to where they were (undo of a move), under
// their names then when those are free.
func (l *Library) MoveBack(froms []MoveFrom) (EditResult, error) {
	var res EditResult
	l.opMu.Lock()
	defer l.opMu.Unlock()
	favs, idMap := map[string]string{}, map[string]string{}
	var changed []*Item
	for _, f := range froms {
		ri, d, err := l.cleanDir(f.Root, f.Dir)
		if err != nil {
			res.warn("%s: %v", f.Name, err)
			continue
		}
		gs := l.groups([]string{f.ID})
		if len(gs) == 0 { // in the trash meanwhile: the undo stays open for it
			res.warn("%s: nicht da (im Papierkorb?)", f.Name)
			continue
		}
		for _, g := range gs {
			moved, err := l.moveGroupAs(g, ri, d, f.Name, favs, idMap, &res)
			if err != nil {
				res.warn("%s: %v", f.Name, err)
				continue
			}
			changed = append(changed, moved...)
		}
	}
	if len(changed) > 0 {
		l.finishMoves(favs, idMap, changed)
	}
	res.IDs = idMap
	return res, nil
}

// moveGroup moves one photo with its Live Photo video (or a single file)
// into folder d of root ri. The caller holds opMu and calls finishMoves.
func (l *Library) moveGroup(g []*Item, ri int, d string, favs, idMap map[string]string, res *EditResult) ([]*Item, error) {
	return l.moveGroupAs(g, ri, d, "", favs, idMap, res)
}

// moveGroupAs moves like moveGroup; name (without extension) is the name the
// files get when it is free ("": the one they have).
func (l *Library) moveGroupAs(g []*Item, ri int, d, name string, favs, idMap map[string]string, res *EditResult) ([]*Item, error) {
	lead := g[0]
	cur := path.Dir(lead.Rel)
	if cur == "." {
		cur = ""
	}
	base := path.Base(lead.Rel)
	if lead.Root == ri && cur == d && (name == "" || strings.TrimSuffix(base, path.Ext(base)) == name) {
		return nil, nil // already there
	}
	target := filepath.Join(l.opt.Roots[ri].Path, filepath.FromSlash(d))
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, err
	}
	stem := strings.TrimSuffix(base, path.Ext(base))
	if name != "" {
		stem = name
	}
	var exts []string
	for _, it := range g {
		exts = append(exts, path.Ext(it.Rel))
	}
	stem = freeStem(target, stem, exts)
	if name != "" && stem != name && lead.Root == ri && cur == d {
		return nil, nil // in its folder, and its old name is taken: it stays as it is
	}
	var moved []*Item
	for i, it := range g {
		if i > 0 && strings.TrimSuffix(path.Base(it.Rel), path.Ext(it.Rel)) != strings.TrimSuffix(base, path.Ext(base)) {
			// a companion with another name keeps its own
			stem = freeStem(target, strings.TrimSuffix(path.Base(it.Rel), path.Ext(it.Rel)), []string{path.Ext(it.Rel)})
		}
		file := stem + path.Ext(it.Rel)
		rel := file
		if d != "" {
			rel = d + "/" + file
		}
		if err := moveWithSidecars(l.Path(it), filepath.Join(target, file)); err != nil {
			res.warn("%s: %v", path.Base(it.Rel), err)
			if i == 0 {
				break // keep the pair together
			}
			continue
		}
		l.relocate(it, ri, rel, favs, idMap)
		moved = append(moved, it)
		res.Changed++
	}
	return moved, nil
}

// finishMoves carries edits, favorites, notes and face names along to the
// new paths and republishes the index.
func (l *Library) finishMoves(favs, idMap map[string]string, changed []*Item) {
	l.renameEdits(favs)
	l.saveEdits()
	l.RenameKeys(favs)
	l.renamePins(favs)
	l.people.renameLabelItems(idMap)
	l.hist.rename(idMap) // the changes kept follow the items
	l.noteMoves(idMap)   // so does work in the queue
	l.moveUrgent(idMap)
	l.refresh(changed)
	l.recluster()
	l.bump()
}
