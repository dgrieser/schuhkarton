package gallery

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/dgrieser/schuhkarton/internal/nasretry"
)

// Files the share hangs on (see nasretry): reading them freezes the whole
// share for minutes. The background work, the backup and the viewer leave
// them out until they change or an admin asks to try them again.

// UnreadableFile is a file the share hung on, for the admin.
type UnreadableFile struct {
	Path string `json:"path"`         // the full path, as the retry takes it
	Name string `json:"name"`         // the library folder's name and the path below it
	ID   string `json:"id,omitempty"` // the item, when it is in the library
	// Aside: left out until it changes or is tried again by hand; else
	// tried once more after a pause.
	Aside bool   `json:"aside"`
	At    int64  `json:"at"` // unix time of the last hang
	Err   string `json:"err"`
}

// unreadable returns an error when the item's file is left out now.
func (l *Library) unreadable(it *Item) error {
	return nasretry.Avoided(l.Path(it), it.Size, it.MTime)
}

// unreadableSig names the files left out of the work outside DoFile (all
// with strikes, see nasretry.Avoided).
func unreadableSig() string {
	var b strings.Builder
	for _, f := range nasretry.Unreadable() {
		b.WriteString(f.Path)
		b.WriteByte(0)
	}
	return b.String()
}

// sideUnreadable tells whether a sidecar is left out (the share hangs on it).
func sideUnreadable(p string) bool { return nasretry.AvoidedPath(p) != nil }

// isUnreadable tells whether err comes from a file left out.
func isUnreadable(err error) bool { return errors.Is(err, nasretry.ErrUnreadable) }

// Unreadable returns an error when the item's file must not be read now
// (the share hangs on it).
func (l *Library) Unreadable(it *Item) error { return l.unreadable(it) }

func (l *Library) unreadableList() []UnreadableFile {
	bad := nasretry.Unreadable()
	if len(bad) == 0 {
		return nil
	}
	out := make([]UnreadableFile, 0, len(bad))
	for _, b := range bad {
		f := UnreadableFile{Path: b.Path, Name: b.Path, Aside: b.Aside(), At: b.At.Unix(), Err: b.Err}
		for _, r := range l.opt.Roots {
			rel, err := filepath.Rel(r.Path, b.Path)
			if err != nil || !filepath.IsLocal(rel) {
				continue
			}
			f.Name = filepath.ToSlash(filepath.Join(r.Name, rel))
			if it := l.Get(itemID(r.Name, filepath.ToSlash(rel))); it != nil {
				f.ID = it.ID
			}
			break
		}
		out = append(out, f)
	}
	return out
}

// RetryUnreadable clears the hangs of the files at paths (all when none is
// given): the background work and the backup try them again.
func (l *Library) RetryUnreadable(paths []string) {
	if len(paths) == 0 {
		for _, b := range nasretry.Unreadable() {
			paths = append(paths, b.Path)
		}
	}
	nasretry.Forget(paths...)
	// a metadata write that hung waits xmpRetryFailed otherwise
	retry := map[string]bool{}
	for _, p := range paths {
		retry[p] = true
	}
	var keys []string
	for _, it := range l.Items() {
		p := l.Path(it)
		side, _ := xmpTarget(p, it.Ext()) // the metadata may have hung on the sidecar
		if retry[p] || retry[side] {
			keys = append(keys, l.favKey(it))
		}
	}
	l.xmpMu.Lock()
	for _, k := range keys {
		delete(l.xmpFails, k)
	}
	l.xmpMu.Unlock()
	// duplicates: compared again (the fingerprint holds what is left out)
	l.dupe.mu.Lock()
	l.dupe.ver = -1
	l.dupe.mu.Unlock()
	l.kickDupes()
	l.media.kick()
	l.kickAI()
	l.ocrJob.kick()
	l.speechJob.kick()
	l.kickXMP()
}
