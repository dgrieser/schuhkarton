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
		if e.IsDir() || n == name {
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
// new name of the file.
func moveWithSidecars(src, dst string) error {
	side := sidecars(src)
	if err := moveFile(src, dst); err != nil {
		return err
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
		moveFile(filepath.Join(sdir, n), filepath.Join(ddir, to))
	}
	return nil
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
	}
	l.rekey(it, oldKey, 0)
}

// Move moves items (Live Photo videos and sidecar files along) into a
// folder of a root. Names that are taken get " (2)". The folder may be new;
// Schuhkarton imposes no layout.
func (l *Library) Move(ids []string, root, dir string) (EditResult, error) {
	var res EditResult
	ri, d, err := l.cleanDir(root, dir)
	if err != nil {
		return res, err
	}
	l.opMu.Lock()
	defer l.opMu.Unlock()
	target := filepath.Join(l.opt.Roots[ri].Path, filepath.FromSlash(d))
	if err := os.MkdirAll(target, 0o755); err != nil {
		return res, err
	}
	favs, idMap := map[string]string{}, map[string]string{}
	var changed []*Item
	for _, g := range l.groups(ids) {
		lead := g[0]
		cur := path.Dir(lead.Rel)
		if cur == "." {
			cur = ""
		}
		if lead.Root == ri && cur == d {
			continue // already there
		}
		base := path.Base(lead.Rel)
		stem := strings.TrimSuffix(base, path.Ext(base))
		var exts []string
		for _, it := range g {
			exts = append(exts, path.Ext(it.Rel))
		}
		stem = freeStem(target, stem, exts)
		for i, it := range g {
			if i > 0 && strings.TrimSuffix(path.Base(it.Rel), path.Ext(it.Rel)) != strings.TrimSuffix(base, path.Ext(base)) {
				// a companion with another name keeps its own
				stem = freeStem(target, strings.TrimSuffix(path.Base(it.Rel), path.Ext(it.Rel)), []string{path.Ext(it.Rel)})
			}
			name := stem + path.Ext(it.Rel)
			rel := name
			if d != "" {
				rel = d + "/" + name
			}
			if err := moveWithSidecars(l.Path(it), filepath.Join(target, name)); err != nil {
				res.warn("%s: %v", path.Base(it.Rel), err)
				if i == 0 {
					break // keep the pair together
				}
				continue
			}
			l.relocate(it, ri, rel, favs, idMap)
			changed = append(changed, it)
			res.Changed++
		}
	}
	l.renameEdits(favs)
	l.saveEdits()
	l.RenameKeys(favs)
	l.people.renameLabelItems(idMap)
	l.refresh(changed)
	l.recluster()
	l.bump()
	res.IDs = idMap
	return res, nil
}
