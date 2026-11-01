package gallery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Library folders and the mount points below them are remembered in
// roots.json in the cache: the identity of the directory while it held
// files (device and inode, on Windows volume and file index), and, while
// it is offline, the files that were kept for it. An empty or changed
// directory is a mount point with nothing mounted (also a bind mount on the
// same disk), not a folder emptied on purpose, unless most of the kept
// files are back (a remount). See Scan.

type dirRecord struct {
	ID   string   `json:"id"`
	Kept []string `json:"kept,omitempty"` // files kept since it went offline (relative to the library folder)
}

type rootDevices struct {
	mu  sync.Mutex
	dev map[string]dirRecord // folder path -> record
}

// deviceOf reports the identity of a directory (false where unknown).
// A variable, so tests can pretend a mount.
var deviceOf = dirIdentity

func (l *Library) devices() map[string]dirRecord {
	d := &l.rootDev
	if d.dev == nil {
		d.dev = map[string]dirRecord{}
		if b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "roots.json")); err == nil {
			json.Unmarshal(b, &d.dev)
		}
	}
	return d.dev
}

func (l *Library) saveDevicesLocked() {
	if b, err := json.Marshal(l.devices()); err == nil {
		if err := writeFileAtomic(filepath.Join(l.opt.CacheDir, "roots.json"), b); err != nil {
			l.logf("roots.json: %v", err)
		}
	}
}

// dirRecord returns what is remembered about a folder.
func (l *Library) dirRecord(path string) (dirRecord, bool) {
	d := &l.rootDev
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := l.devices()[path]
	return r, ok
}

// rememberDevice notes the identity of a folder that has its files: it is
// online.
func (l *Library) rememberDevice(path string) {
	id, ok := deviceOf(path)
	if !ok {
		return
	}
	d := &l.rootDev
	d.mu.Lock()
	defer d.mu.Unlock()
	m := l.devices()
	if old, had := m[path]; had && old.ID == id && len(old.Kept) == 0 {
		return
	}
	m[path] = dirRecord{ID: id}
	l.saveDevicesLocked()
}

// markOffline notes the files kept for a folder that went offline; while it
// stays offline the first list stays.
func (l *Library) markOffline(path string, kept []string) {
	d := &l.rootDev
	d.mu.Lock()
	defer d.mu.Unlock()
	m := l.devices()
	r := m[path]
	if len(r.Kept) > 0 || len(kept) == 0 {
		return
	}
	r.Kept = kept
	m[path] = r
	l.saveDevicesLocked()
}

// sameDevice reports whether a folder is the directory it was while it had
// files. Unknown counts as no.
func (l *Library) sameDevice(path string) bool {
	id, ok := deviceOf(path)
	if !ok {
		return false
	}
	r, had := l.dirRecord(path)
	return had && r.ID == id
}

// forgetDevice drops a remembered directory that no longer exists.
func (l *Library) forgetDevice(path string) {
	d := &l.rootDev
	d.mu.Lock()
	defer d.mu.Unlock()
	m := l.devices()
	if _, ok := m[path]; !ok {
		return
	}
	delete(m, path)
	l.saveDevicesLocked()
}

// knownDirs lists the remembered directories (library folders and mount
// points below them).
func (l *Library) knownDirs() map[string]bool {
	d := &l.rootDev
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]bool{}
	for p := range l.devices() {
		out[p] = true
	}
	return out
}

// deviceOnly is the device part of an identity ("device:inode").
func deviceOnly(id string) string {
	if i := strings.LastIndexByte(id, ':'); i >= 0 {
		return id[:i]
	}
	return id
}
