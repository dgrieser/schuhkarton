package gallery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// Marks for deletion belong to an account: a marked photo is hidden for
// that account, and admins decide whether it goes to the trash. marks.json
// maps user names to item keys (root name + "/" + rel) and the time.

type markStore struct {
	users map[string]map[string]int64
}

func (l *Library) loadMarks() {
	l.marks.users = map[string]map[string]int64{}
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "marks.json"))
	if err != nil {
		return
	}
	var f struct {
		Users map[string]map[string]int64 `json:"users"`
	}
	if json.Unmarshal(b, &f) == nil && f.Users != nil {
		l.marks.users = f.Users
	}
}

// saveMarks writes marks.json; the caller holds favMu.
func (l *Library) saveMarks() error {
	f := map[string]any{"version": 1, "users": l.marks.users}
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "marks.json"), b)
}

// IsMarked reports whether user marked an item for deletion.
func (l *Library) IsMarked(user string, it *Item) bool {
	if user == "" {
		return false
	}
	l.favMu.Lock()
	defer l.favMu.Unlock()
	_, ok := l.marks.users[user][l.favKey(it)]
	return ok
}

// MarkedBy lists the accounts that marked an item, oldest mark first.
func (l *Library) MarkedBy(it *Item) []Mark {
	k := l.favKey(it)
	l.favMu.Lock()
	defer l.favMu.Unlock()
	var out []Mark
	for u, set := range l.marks.users {
		if at, ok := set[k]; ok {
			out = append(out, Mark{User: u, At: at})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At < out[b].At })
	return out
}

// Mark is one account's mark on an item.
type Mark struct {
	User string `json:"user"`
	At   int64  `json:"at"`
}

// SetMarks marks or unmarks items for deletion for user.
func (l *Library) SetMarks(user string, ids []string, on bool) (int, error) {
	if user == "" {
		return 0, errors.New("Vormerken braucht ein Konto")
	}
	l.favMu.Lock()
	defer l.favMu.Unlock()
	set := l.marks.users[user]
	if set == nil {
		set = map[string]int64{}
		l.marks.users[user] = set
	}
	n := 0
	now := nowUnix()
	for _, id := range ids {
		it := l.Get(id)
		if it == nil {
			continue
		}
		k := l.favKey(it)
		_, had := set[k]
		switch {
		case on && !had:
			set[k] = now
			n++
		case !on && had:
			delete(set, k)
			n++
		}
	}
	if len(set) == 0 {
		delete(l.marks.users, user)
	}
	l.bump()
	return n, l.saveMarks()
}

// MarkedIDs returns the items user marked, newest mark first.
func (l *Library) MarkedIDs(user string) []string {
	l.favMu.Lock()
	set := l.marks.users[user]
	type km struct {
		k  string
		at int64
	}
	var ks []km
	for k, at := range set {
		ks = append(ks, km{k, at})
	}
	l.favMu.Unlock()
	sort.Slice(ks, func(a, b int) bool { return ks[a].at > ks[b].at })
	byKey := l.itemsByKey()
	var out []string
	for _, x := range ks {
		if it := byKey[x.k]; it != nil {
			out = append(out, it.ID)
		}
	}
	return out
}

// MarkedItem is an item with everybody who marked it.
type MarkedItem struct {
	ID    string `json:"id"`
	Marks []Mark `json:"marks"`
}

// AllMarks lists every marked item: marked by most accounts first, then the
// newest mark.
func (l *Library) AllMarks() []MarkedItem {
	l.favMu.Lock()
	by := map[string][]Mark{}
	for u, set := range l.marks.users {
		for k, at := range set {
			by[k] = append(by[k], Mark{User: u, At: at})
		}
	}
	l.favMu.Unlock()
	byKey := l.itemsByKey()
	var out []MarkedItem
	for k, ms := range by {
		it := byKey[k]
		if it == nil {
			continue
		}
		sort.Slice(ms, func(a, b int) bool { return ms[a].At < ms[b].At })
		out = append(out, MarkedItem{ID: it.ID, Marks: ms})
	}
	last := func(m MarkedItem) int64 { return m.Marks[len(m.Marks)-1].At }
	sort.Slice(out, func(a, b int) bool {
		if len(out[a].Marks) != len(out[b].Marks) {
			return len(out[a].Marks) > len(out[b].Marks)
		}
		if last(out[a]) != last(out[b]) {
			return last(out[a]) > last(out[b])
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// ClearMarks removes the marks of every account from items.
func (l *Library) ClearMarks(ids []string) (int, error) {
	keys := map[string]bool{}
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			keys[l.favKey(it)] = true
		}
	}
	l.favMu.Lock()
	defer l.favMu.Unlock()
	n := 0
	for u, set := range l.marks.users {
		for k := range keys {
			if _, ok := set[k]; ok {
				delete(set, k)
				n++
			}
		}
		if len(set) == 0 {
			delete(l.marks.users, u)
		}
	}
	l.bump()
	return n, l.saveMarks()
}

// renameMarks moves marks to new keys ("" drops them); the caller holds favMu.
func (l *Library) renameMarks(pairs map[string]string) bool {
	changed := false
	for _, set := range l.marks.users {
		for from, to := range pairs {
			if at, ok := set[from]; ok {
				delete(set, from)
				if to != "" {
					set[to] = at
				}
				changed = true
			}
		}
	}
	return changed
}

// itemsByKey maps root name + "/" + rel to the items.
func (l *Library) itemsByKey() map[string]*Item {
	items := l.Items()
	m := make(map[string]*Item, len(items))
	for _, it := range items {
		m[l.favKey(it)] = it
	}
	return m
}
