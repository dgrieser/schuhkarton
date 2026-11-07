package nasretry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Some files hang the share: reading them makes a Fritz!Box stop answering
// SMB altogether until the kernel gives up after 180 s and reconnects (a
// bad spot on its disk, most likely). Every try freezes the whole share,
// for the viewer too, so such a file must not be tried again and again.
//
// A try that failed after hanging at least HangTime, while the share
// answers right after, is a strike against the file: no new try in the
// same pass, and none until StrikePause has passed. That alone does not
// tell the file that hangs from one that was read at the same moment and
// hung with it, so the next try of a file with a strike runs alone (see
// DoFile). Strikes strikes put the file aside: it is left out until it
// changes (size or modification time) or an admin asks to try it again
// (Forget). A try that works clears its strikes.

var (
	// HangTime: a failed try that took this long hung on the share (a CIFS
	// mount gives up on a server after 2–3 echo intervals of 60 s).
	HangTime = 90 * time.Second
	// StrikePause: no new try of a file with one strike before this.
	StrikePause = 20 * time.Hour
	// Strikes put a file aside for good.
	Strikes = 2
)

// ErrUnreadable is what an *UnreadableError is.
var ErrUnreadable = errors.New("das Netzlaufwerk hängt beim Lesen der Datei")

// UnreadableError: the file hangs the share and is left out.
type UnreadableError struct {
	Path  string
	Cause string // what the try that hung said
}

func (e *UnreadableError) Error() string {
	return ErrUnreadable.Error() + " (" + e.Cause + "); sie wird ausgelassen"
}

func (e *UnreadableError) Is(target error) bool { return target == ErrUnreadable }

// BadFile is a file with strikes.
type BadFile struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	MTime   int64     `json:"mtime"` // unix seconds
	Strikes int       `json:"strikes"`
	At      time.Time `json:"at"` // the last strike
	Err     string    `json:"err"`
}

// Aside tells whether the file is left out for good (not only for a pause).
func (b BadFile) Aside() bool { return b.Strikes >= Strikes }

// Active tells whether the file is left out now (aside, or in its pause).
func (b BadFile) Active() bool { return b.Aside() || time.Since(b.At) < StrikePause }

var bad = struct {
	sync.Mutex
	file  string // where the list is kept ("": nowhere)
	files map[string]*BadFile
}{files: map[string]*BadFile{}}

// LoadUnreadable reads the list of files with strikes from file and keeps
// it there from now on.
func LoadUnreadable(file string) {
	bad.Lock()
	defer bad.Unlock()
	bad.file, bad.files = file, map[string]*BadFile{}
	b, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var list []BadFile
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, f := range list {
		bad.files[f.Path] = &f
	}
}

func saveLocked() {
	if bad.file == "" {
		return
	}
	if len(bad.files) == 0 {
		os.Remove(bad.file)
		return
	}
	b, err := json.MarshalIndent(listLocked(), "", "  ")
	if err != nil {
		return
	}
	tmp := bad.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		Logf("%s: %v", bad.file, err)
		return
	}
	if err := os.Rename(tmp, bad.file); err != nil {
		Logf("%s: %v", bad.file, err)
	}
}

func listLocked() []BadFile {
	out := make([]BadFile, 0, len(bad.files))
	for _, f := range bad.files {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Unreadable lists the files with strikes.
func Unreadable() []BadFile {
	bad.Lock()
	defer bad.Unlock()
	return listLocked()
}

// Forget clears the strikes of the files at paths (of all files when none
// is given): they are tried again.
func Forget(paths ...string) {
	bad.Lock()
	defer bad.Unlock()
	n := len(bad.files)
	if len(paths) == 0 {
		clear(bad.files)
	}
	for _, p := range paths {
		delete(bad.files, p)
	}
	if len(bad.files) != n {
		saveLocked()
	}
}

// Avoided returns an *UnreadableError when the file at path, with this
// size and modification time (unix seconds), has strikes: work outside
// DoFile does not read it (a hang there would count no strike). Only a try
// through DoFile, after the pause, tells whether it reads again (see
// Due). A file that changed since its last strike is read again.
func Avoided(path string, size, mtime int64) error { return avoided(path, size, mtime, false) }

// Due is Avoided for work through DoFile: a file with one strike is tried
// again once its pause is over.
func Due(path string, size, mtime int64) error { return avoided(path, size, mtime, true) }

// avoided: the caller's size and time may be older than the strike (an
// index not scanned since): when they differ, the file itself tells (a
// stat, which a file the share hangs on answers; when even that fails, the
// file stays left out).
func avoided(path string, size, mtime int64, tracked bool) error {
	bad.Lock()
	f := bad.files[path]
	if f == nil {
		bad.Unlock()
		return nil
	}
	b := *f
	bad.Unlock()
	if b.Size != size || b.MTime != mtime {
		st, err := statFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// gone: forgotten only when its folder answers and holds
			// something (an unmounted share leaves an empty mount point,
			// and the file comes back with it)
			if empty, perr := probe(filepath.Dir(path)); perr == nil && !empty {
				forgetIf(path, b)
			}
			return nil
		case err == nil && (st.Size() != b.Size || st.ModTime().Unix() != b.MTime):
			forgetIf(path, b) // changed since it hung
			return nil
		}
	}
	if tracked && !b.Active() {
		return nil
	}
	return &UnreadableError{Path: path, Cause: b.Err}
}

var statFile = os.Stat

// forgetIf clears the file's strikes unless they changed since b was read.
func forgetIf(path string, b BadFile) {
	bad.Lock()
	defer bad.Unlock()
	if f := bad.files[path]; f != nil && *f == b {
		delete(bad.files, path)
		saveLocked()
	}
}

func listed(path string) bool {
	bad.Lock()
	defer bad.Unlock()
	return bad.files[path] != nil
}

// strike counts a hang against the file and returns the error for it.
// Only a try that ran alone puts a file aside: hanging along with another
// try (of this file or another one) is one strike at most.
func strike(path string, size, mtime int64, alone bool, err error) error {
	bad.Lock()
	f := bad.files[path]
	if f == nil || f.Size != size || f.MTime != mtime {
		f = &BadFile{Path: path, Size: size, MTime: mtime}
		bad.files[path] = f
	}
	if alone || f.Strikes == 0 {
		f.Strikes++
	}
	f.At, f.Err = time.Now(), err.Error()
	saveLocked()
	aside := f.Aside()
	bad.Unlock()
	if aside {
		Logf("%s: %v; das Netzlaufwerk hing an der Datei schon wieder, sie bleibt ausgelassen, bis sie sich ändert", path, err)
	} else {
		Logf("%s: %v; das Netzlaufwerk hing an der Datei, neuer Versuch frühestens in %s", path, err, StrikePause)
	}
	return &UnreadableError{Path: path, Cause: err.Error()}
}

// blamed marks an error with whether the file itself hung (see Blame).
type blamed struct {
	error
	file bool
}

func (b blamed) Unwrap() error { return b.error }

// Blame tells DoFile whether err came after the file itself hung (a read
// of it that took HangTime or more) or not. Work that touches other files
// too (a copy writing a target on the share) says so this way; otherwise
// DoFile goes by how long the whole try took.
func Blame(err error, fileHung bool) error {
	if err == nil {
		return nil
	}
	return blamed{err, fileHung}
}

// The try of a file with a strike runs alone: no other work through
// DoFile meanwhile, so that a file hanging the share then is this one.
// (Work outside DoFile, thumbnails say, may still run along.)
var gate = struct {
	sync.Mutex
	readers int  // work under way besides
	alone   bool // a file with a strike is tried
	waiting int  // tries of such files waiting for the others to end
	changed chan struct{}
}{changed: make(chan struct{})}

// enter waits until the work may run: alone, or along with other work
// that is not alone.
func enter(ctx context.Context, alone bool) error {
	gate.Lock()
	if alone {
		gate.waiting++
	}
	for {
		if alone && !gate.alone && gate.readers == 0 {
			gate.waiting--
			gate.alone = true
			gate.Unlock()
			return nil
		}
		if !alone && !gate.alone && gate.waiting == 0 {
			gate.readers++
			gate.Unlock()
			return nil
		}
		ch := gate.changed
		gate.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			if alone {
				gate.Lock()
				gate.waiting--
				signalLocked()
				gate.Unlock()
			}
			return ctx.Err()
		}
		gate.Lock()
	}
}

func leave(alone bool) {
	gate.Lock()
	if alone {
		gate.alone = false
	} else {
		gate.readers--
	}
	signalLocked()
	gate.Unlock()
}

func signalLocked() {
	close(gate.changed)
	gate.changed = make(chan struct{})
}

// AvoidedPath is Avoided for a file whose size and time the caller does
// not know (a sidecar): the file itself is asked when it has strikes.
func AvoidedPath(path string) error { return Avoided(path, -1, -1) }

// Sig returns the size and modification time (unix seconds) of the
// version of a file a try works on.
type Sig func() (size, mtime int64)

// Fixed is the Sig of a file that does not change under the work.
func Fixed(size, mtime int64) Sig { return func() (int64, int64) { return size, mtime } }

// DoFile is Do for work that reads or writes the file at path; sig is asked
// before and after every try (work that takes the file as it is then
// tells which version it read). A file left out (see Avoided) is not
// touched: DoFile returns an *UnreadableError at once. A try that hangs on
// the share (see Blame) counts a strike and is not repeated; a try that
// works clears the file's strikes.
func DoFile(ctx context.Context, what, path string, sig Sig, fn func() error, dirs ...Place) error {
	err := Do(ctx, what, func() error {
		// checked before every try: another one may have put it aside
		size, mtime := sig()
		if err := Due(path, size, mtime); err != nil {
			return err
		}
		// a file that hung before (maybe only along with another one) is
		// tried alone; the waits between tries keep nobody waiting
		alone := listed(path)
		if err := enter(ctx, alone); err != nil {
			return err
		}
		// the gate is held until the try is judged: a waiting try must
		// see the strike this one may give
		defer leave(alone)
		if err := Due(path, size, mtime); err != nil { // put aside while this one waited
			return err
		}
		before, had := entry(path)
		start := time.Now()
		err := fn()
		switch {
		case err == nil:
			if had {
				forgetIf(path, before) // not a strike another try gave meanwhile
			}
			return nil
		case ctx.Err() != nil || !retryable(err):
			return err
		}
		hung := time.Since(start) >= HangTime
		var b blamed
		if errors.As(err, &b) {
			hung = b.file
		}
		if hung && Reachable(dirs...) {
			size, mtime := sig() // the version this try read
			return strike(path, size, mtime, alone, err)
		}
		return err
	}, dirs...)
	return err
}

// entry returns the file's strikes as they are now.
func entry(path string) (BadFile, bool) {
	bad.Lock()
	defer bad.Unlock()
	if f := bad.files[path]; f != nil {
		return *f, true
	}
	return BadFile{}, false
}
