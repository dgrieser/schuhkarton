package gallery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// TrashDir is the folder inside each root that holds deleted files until
// the trash is emptied. Scans never look into it.
const TrashDir = ".schuhkarton-papierkorb"

// TrashEntry is one deleted photo or video (with its Live Photo video).
type TrashEntry struct {
	ID       string      `json:"id"`
	Root     string      `json:"root"`
	Rel      string      `json:"rel"`      // where it was
	TrashRel string      `json:"trashRel"` // where it is, relative to the root
	Deleted  int64       `json:"deleted"`
	By       string      `json:"by,omitempty"`
	Key      string      `json:"key"` // cache key: the thumbnail stays until the trash is emptied
	Item     *Item       `json:"item"`
	Live     *TrashEntry `json:"live,omitempty"`
}

func (l *Library) loadTrash() {
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "trash.json"))
	if err != nil {
		return
	}
	var f struct {
		Entries []*TrashEntry `json:"entries"`
	}
	if json.Unmarshal(b, &f) == nil {
		l.trash = f.Entries
	}
}

// saveTrash writes trash.json; the caller holds trashMu.
func (l *Library) saveTrash() error {
	b, err := json.MarshalIndent(map[string]any{"version": 1, "entries": l.trash}, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "trash.json"), b)
}

func (l *Library) rootIndex(name string) int {
	for i, r := range l.opt.Roots {
		if r.Name == name {
			return i
		}
	}
	return -1
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// trashOne moves one file into the trash folder of its root.
func (l *Library) trashOne(it *Item, by string) (*TrashEntry, error) {
	root := l.opt.Roots[it.Root]
	dir := filepath.Join(root.Path, TrashDir, filepath.FromSlash(path.Dir(it.Rel)))
	base := path.Base(it.Rel)
	ext := path.Ext(base)
	stem := freeStem(dir, strings.TrimSuffix(base, ext), []string{ext})
	dst := filepath.Join(dir, stem+ext)
	if err := moveWithSidecars(l.Path(it), dst); err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(root.Path, dst)
	snap := *it
	return &TrashEntry{ID: randomID(), Root: root.Name, Rel: it.Rel, TrashRel: filepath.ToSlash(rel),
		Deleted: nowUnix(), By: by, Key: it.key, Item: &snap}, nil
}

// Trash moves items into the trash (Live Photo videos and sidecars along).
// Favorites, edits and face names stay and come back with a restore.
func (l *Library) Trash(ids []string, by string) (EditResult, error) {
	var res EditResult
	l.opMu.Lock()
	defer l.opMu.Unlock()
	gone := map[string]bool{}
	var entries []*TrashEntry
	for _, g := range l.groups(ids) {
		e, err := l.trashOne(g[0], by)
		if err != nil {
			res.warn("%s: %v", path.Base(g[0].Rel), err)
			continue
		}
		gone[g[0].ID] = true
		res.Changed++
		for _, it := range g[1:] {
			le, err := l.trashOne(it, by)
			if err != nil {
				res.warn("%s: %v", path.Base(it.Rel), err)
				continue
			}
			e.Live = le
			gone[it.ID] = true
		}
		entries = append(entries, e)
		res.Entries = append(res.Entries, e.ID)
	}
	if len(entries) == 0 {
		if len(res.Warnings) > 0 {
			return res, errors.New(res.Warnings[0])
		}
		return res, errors.New("keine Elemente")
	}
	l.trashMu.Lock()
	l.trash = append(l.trash, entries...)
	err := l.saveTrash()
	l.trashMu.Unlock()
	l.removeItems(gone)
	// the changes kept of these files wait in the trash with them: a new file
	// at the same path (same id) gets changes of its own
	refs := map[string]string{}
	for _, e := range entries {
		for i, m := range e.members() {
			if m.Item != nil {
				refs[m.Item.ID] = trashRef(e.ID, i)
			}
		}
	}
	l.hist.rename(refs)
	return res, err
}

// members are the files of an entry: the photo, then its Live Photo video.
func (e *TrashEntry) members() []*TrashEntry {
	if e.Live != nil {
		return []*TrashEntry{e, e.Live}
	}
	return []*TrashEntry{e}
}

// trashRef names a file in the trash in the changes kept (member i of an entry).
func trashRef(entry string, i int) string {
	if i == 0 {
		return "trash:" + entry
	}
	return "trash:" + entry + "/" + strconv.Itoa(i)
}

// removeItems drops items from the index.
func (l *Library) removeItems(gone map[string]bool) {
	l.mu.Lock()
	keep := make([]*Item, 0, len(l.items))
	for _, it := range l.items {
		if !gone[it.ID] {
			keep = append(keep, it)
		}
	}
	l.mu.Unlock()
	pairLive(keep)
	l.setItems(keep)
	// the index file and the people follow in the background: deleting
	// (one duplicate after the other) does not wait for them
	l.saveSoon()
	l.reclusterSoon()
	l.bump()
}

// TrashList returns the trash, newest first, after removing what is older
// than TrashDays.
func (l *Library) TrashList() []*TrashEntry {
	l.PurgeTrash()
	l.trashMu.Lock()
	defer l.trashMu.Unlock()
	out := append([]*TrashEntry(nil), l.trash...)
	sort.Slice(out, func(a, b int) bool { return out[a].Deleted > out[b].Deleted })
	return out
}

// TrashEntryByID returns one entry.
func (l *Library) TrashEntryByID(id string) *TrashEntry {
	l.trashMu.Lock()
	defer l.trashMu.Unlock()
	for _, e := range l.trash {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// TrashThumb returns the cached thumbnail of a trash entry ("" if none).
func (l *Library) TrashThumb(id string) string {
	e := l.TrashEntryByID(id)
	if e == nil || len(e.Key) < 2 {
		return ""
	}
	p := l.cacheFile(e.Key, VarSmall)
	if !exists(p) {
		return ""
	}
	return p
}

// takeTrash removes entries from the list (all when ids is nil).
func (l *Library) takeTrash(ids []string, filter func(*TrashEntry) bool) []*TrashEntry {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	l.trashMu.Lock()
	defer l.trashMu.Unlock()
	var taken, keep []*TrashEntry
	for _, e := range l.trash {
		if (ids == nil || want[e.ID]) && (filter == nil || filter(e)) {
			taken = append(taken, e)
		} else {
			keep = append(keep, e)
		}
	}
	l.trash = keep
	return taken
}

// Restore moves entries back to where they were (with " (2)" when the name
// is taken meanwhile).
func (l *Library) Restore(ids []string) (EditResult, error) {
	var res EditResult
	l.opMu.Lock()
	defer l.opMu.Unlock()
	entries := l.takeTrash(ids, nil)
	var back []*TrashEntry // failed: stay in the trash
	var restored []*Item
	favs, idMap := map[string]string{}, map[string]string{}
	refs := map[string]string{} // trash ref -> the restored id, for the changes kept
	for _, e := range entries {
		ri := l.rootIndex(e.Root)
		if ri < 0 {
			res.warn("%s: Ordner %q gibt es nicht mehr", path.Base(e.Rel), e.Root)
			back = append(back, e)
			continue
		}
		members := []*TrashEntry{e}
		if e.Live != nil {
			members = append(members, e.Live)
		}
		root := l.opt.Roots[ri]
		dir := path.Dir(e.Rel)
		if dir == "." {
			dir = ""
		}
		abs := filepath.Join(root.Path, filepath.FromSlash(dir))
		base := path.Base(e.Rel)
		var exts []string
		for _, m := range members {
			exts = append(exts, path.Ext(m.Rel))
		}
		stem := freeStem(abs, strings.TrimSuffix(base, path.Ext(base)), exts)
		ok := true
		for i, m := range members {
			s := stem
			if i > 0 && strings.TrimSuffix(path.Base(m.Rel), path.Ext(m.Rel)) != strings.TrimSuffix(base, path.Ext(base)) {
				s = freeStem(abs, strings.TrimSuffix(path.Base(m.Rel), path.Ext(m.Rel)), []string{path.Ext(m.Rel)})
			}
			name := s + path.Ext(m.Rel)
			rel := name
			if dir != "" {
				rel = dir + "/" + name
			}
			from := filepath.Join(root.Path, filepath.FromSlash(m.TrashRel))
			err := moveWithSidecars(from, filepath.Join(abs, name))
			if err == nil {
				pruneTrashDirs(filepath.Dir(from))
			}
			if err != nil {
				res.warn("%s: %v", path.Base(m.Rel), err)
				if i == 0 {
					ok = false
					break
				}
				continue
			}
			it := m.Item
			if it == nil {
				continue
			}
			it.Root, it.RootName = ri, root.Name
			it.key = m.Key
			if rel != m.Rel {
				oldFav := root.Name + "/" + m.Rel
				l.relocate(it, ri, rel, favs, idMap)
				favs[oldFav] = l.favKey(it)
			} else {
				it.ID = itemID(root.Name, rel)
			}
			restored = append(restored, it)
			refs[trashRef(e.ID, i)] = it.ID
			if i == 0 {
				if res.IDs == nil {
					res.IDs = map[string]string{}
				}
				res.IDs[e.ID] = it.ID
			}
		}
		if !ok {
			back = append(back, e)
			continue
		}
		res.Changed++
	}
	l.trashMu.Lock()
	l.trash = append(l.trash, back...)
	l.saveTrash()
	l.trashMu.Unlock()
	l.renameEdits(favs)
	l.saveEdits()
	l.RenameKeys(favs)
	l.renamePins(favs)
	l.people.renameLabelItems(idMap)
	l.hist.rename(refs) // the changes kept come back with the files
	if len(restored) > 0 {
		l.mu.Lock()
		all := append(append([]*Item(nil), l.items...), restored...)
		for _, it := range restored {
			l.finishItem(it)
		}
		l.mu.Unlock()
		pairLive(all)
		l.setItems(all)
		l.saveIndex()
		l.recluster()
		l.bump()
	}
	return res, nil
}

// EmptyTrash deletes trash entries for good (all when ids is nil): the
// files, their cached thumbnails and what was kept for them.
func (l *Library) EmptyTrash(ids []string) (EditResult, error) {
	l.opMu.Lock()
	defer l.opMu.Unlock()
	return l.emptyTrash(l.takeTrash(ids, nil)), nil
}

// PurgeTrash empties entries older than TrashDays.
func (l *Library) PurgeTrash() {
	if l.opt.TrashDays <= 0 {
		return
	}
	limit := nowUnix() - int64(l.opt.TrashDays)*86400
	if !l.opMu.TryLock() {
		return // an admin operation runs: next time
	}
	defer l.opMu.Unlock()
	if old := l.takeTrash(nil, func(e *TrashEntry) bool { return e.Deleted < limit }); len(old) > 0 {
		l.Record(Change{Kind: ChPurge, Names: TrashNames(old), Info: map[string]any{"n": len(old), "days": l.opt.TrashDays}}, nil)
		r := l.emptyTrash(old)
		l.logf("trash: %d entries older than %d days removed", r.Changed, l.opt.TrashDays)
	}
}

func (l *Library) emptyTrash(entries []*TrashEntry) EditResult {
	var res EditResult
	// what still lives at a path keeps its edits, favorites and names
	l.mu.RLock()
	live := map[string]bool{}
	for _, it := range l.items {
		live[it.ID] = true
	}
	l.mu.RUnlock()
	favs, idMap := map[string]string{}, map[string]string{}
	cleanDirs := map[string]bool{}
	for _, e := range entries {
		members := []*TrashEntry{e}
		if e.Live != nil {
			members = append(members, e.Live)
		}
		for _, m := range members {
			ri := l.rootIndex(m.Root)
			if ri >= 0 {
				abs := filepath.Join(l.opt.Roots[ri].Path, filepath.FromSlash(m.TrashRel))
				for _, s := range sidecars(abs) {
					os.Remove(filepath.Join(filepath.Dir(abs), s))
				}
				if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
					res.warn("%s: %v", path.Base(m.Rel), err)
				}
				cleanDirs[filepath.Dir(abs)] = true
			}
			faces := 0
			if m.Item != nil {
				faces = len(m.Item.Faces)
			}
			l.removeDerivatives(m.Key, faces)
			id := itemID(m.Root, m.Rel)
			if !live[id] {
				favs[m.Root+"/"+m.Rel] = ""
				idMap[id] = ""
			}
		}
		res.Changed++
	}
	l.renameEdits(favs)
	l.saveEdits()
	l.RenameKeys(favs)
	l.renamePins(favs)
	l.people.renameLabelItems(idMap)
	gone := map[string]string{}
	for _, e := range entries {
		for i := range e.members() {
			gone[trashRef(e.ID, i)] = "" // gone for good: no new file at that path gets them
		}
	}
	l.hist.rename(gone)
	l.trashMu.Lock()
	l.saveTrash()
	l.trashMu.Unlock()
	for d := range cleanDirs {
		pruneTrashDirs(d)
	}
	return res
}

// pruneTrashDirs removes empty folders from d up to the trash folder itself.
func pruneTrashDirs(d string) {
	for strings.Contains(filepath.ToSlash(d), "/"+TrashDir) {
		if os.Remove(d) != nil || filepath.Base(d) == TrashDir {
			return
		}
		d = filepath.Dir(d)
	}
}

// TrashNames returns the file names of trash entries.
func TrashNames(entries []*TrashEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, path.Base(e.Rel))
	}
	return out
}
