package gallery

import (
	"bytes"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// Writing to the disk of a small machine is slow: an SD card in a Raspberry
// Pi takes about a second per megabyte, and while it writes, everything else
// that needs it (a thumbnail, another save) waits. So a change made on the
// page is not written right away: lazyWrite writes it a while later, with
// the changes that came meanwhile. What is lost when the program is killed
// before is at most that while; stopping it writes everything first.

// pageDelay is the wait before writing what was done on the page (names,
// approvals, duplicates); resultsEvery the least time between two writes
// of what background work found (made again after a crash: descriptions,
// recognized text, hashes, face scans).
var (
	pageDelay    = time.Minute
	resultsEvery = 15 * time.Minute
)

// diskMu lets one write of the cache files run at a time (writeFileAtomic,
// the index): two at once take as long together and keep the disk busy
// for the whole time.
var diskMu sync.Mutex

type lazyWrite struct {
	delay time.Duration // from the first change to the write
	write func() error
	logf  func(string, ...any)
	name  string

	mu      sync.Mutex
	dirty   bool
	timer   *time.Timer
	stopped bool // after flushStop: writes at once (nothing comes to write later)
	running sync.Mutex
}

func newLazyWrite(name string, delay time.Duration, write func() error, logf func(string, ...any)) *lazyWrite {
	return &lazyWrite{name: name, delay: delay, write: write, logf: logf}
}

// mark notes a change: written within delay.
func (w *lazyWrite) mark() {
	w.mu.Lock()
	w.dirty = true
	if w.stopped {
		w.mu.Unlock()
		w.flush()
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(w.delay, w.fire)
	}
	w.mu.Unlock()
}

func (w *lazyWrite) fire() {
	w.mu.Lock()
	w.timer = nil
	w.mu.Unlock()
	if err := w.flush(); err != nil {
		if w.logf != nil {
			w.logf("%s: %v", w.name, err)
		}
		w.mark() // tried again later
	}
}

// flush writes now when something changed since the last write.
func (w *lazyWrite) flush() error {
	w.running.Lock()
	defer w.running.Unlock()
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	d := w.dirty
	w.dirty = false
	w.mu.Unlock()
	if !d {
		return nil
	}
	err := w.write()
	if err != nil {
		w.mu.Lock()
		w.dirty = true
		w.mu.Unlock()
	}
	return err
}

// flushStop writes what is pending; later changes are written at once
// (the program is ending).
func (w *lazyWrite) flushStop() error {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	return w.flush()
}

// helperNice is the CPU priority of the helpers (ffmpeg, exiftool, ...):
// the server itself answers first.
const helperNice = 10

// combinedNice is cmd.CombinedOutput with the helper at helperNice.
func combinedNice(cmd *exec.Cmd) ([]byte, error) {
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	niceStarted(cmd)
	err := cmd.Wait()
	return b.Bytes(), err
}

// bgWorkers is how many goroutines work side by side on background work
// that keeps the CPU busy: one core is left for answering the page.
func bgWorkers() int { return max(1, runtime.NumCPU()-1) }
