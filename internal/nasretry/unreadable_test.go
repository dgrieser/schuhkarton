package nasretry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestDoFileHang(t *testing.T) {
	defer func(h, p time.Duration, s int, d []time.Duration, st time.Duration, l func(string, ...any), pr func(string) (bool, error)) {
		HangTime, StrikePause, Strikes, Delays, Settle, Logf, probe = h, p, s, d, st, l, pr
		LoadUnreadable("")
	}(HangTime, StrikePause, Strikes, Delays, Settle, Logf, probe)
	HangTime, StrikePause, Strikes = 20*time.Millisecond, time.Hour, 2
	Delays, Settle = []time.Duration{time.Millisecond}, time.Millisecond
	Logf = func(string, ...any) {}
	probe = func(string) (bool, error) { return false, nil } // the share answers
	file := filepath.Join(t.TempDir(), "unreadable.json")
	LoadUnreadable(file)
	ctx := context.Background()
	dir := In("/mnt/nas/a", "/mnt/nas")
	const p = "/mnt/nas/a/IMG_1464.jpg"

	tries := 0
	hang := func() error { tries++; time.Sleep(30 * time.Millisecond); return syscall.EBADF }
	// a try that hangs: one strike, no second try
	if err := DoFile(ctx, "a", p, Fixed(100, 7), hang, dir); !errors.Is(err, ErrUnreadable) || tries != 1 {
		t.Fatalf("hang: %v after %d tries", err, tries)
	}
	if Outage(&UnreadableError{Path: p, Cause: "write x: bad file descriptor"}) {
		t.Error("a file left out counts as an outage: it would be tried again")
	}
	// kept: read again after a restart
	LoadUnreadable(file)
	if b := Unreadable(); len(b) != 1 || b[0].Strikes != 1 || b[0].Aside() {
		t.Fatalf("after one hang: %+v", b)
	}
	// left out during the pause
	if err := DoFile(ctx, "a", p, Fixed(100, 7), hang, dir); !errors.Is(err, ErrUnreadable) || tries != 1 {
		t.Fatalf("during the pause: %v after %d tries", err, tries)
	}
	// after the pause: tried again; it hangs again and is put aside
	StrikePause = 0
	if err := DoFile(ctx, "a", p, Fixed(100, 7), hang, dir); !errors.Is(err, ErrUnreadable) || tries != 2 {
		t.Fatalf("second hang: %v after %d tries", err, tries)
	}
	if b := Unreadable(); len(b) != 1 || !b[0].Aside() {
		t.Fatalf("after two hangs: %+v", b)
	}
	if err := Avoided(p, 100, 7); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("put aside, still tried: %v", err)
	}
	// changed: tried again
	if err := Avoided(p, 101, 7); err != nil || len(Unreadable()) != 0 {
		t.Fatalf("changed file: %v, %+v", err, Unreadable())
	}
	// a try that works clears a strike
	DoFile(ctx, "a", p, Fixed(100, 7), hang, dir)
	if err := DoFile(ctx, "a", p, Fixed(100, 7), func() error { return nil }, dir); err != nil || len(Unreadable()) != 0 {
		t.Fatalf("worked: %v, %+v", err, Unreadable())
	}
	// a quick failure is no hang: the usual tries, no strike
	tries = 0
	quick := func() error { tries++; return syscall.EBADF }
	if err := DoFile(ctx, "a", p, Fixed(100, 7), quick, dir); !errors.Is(err, syscall.EBADF) || tries != 3 || len(Unreadable()) != 0 {
		t.Fatalf("quick failure: %v after %d tries, %+v", err, tries, Unreadable())
	}
	// a hang while the share stays away is the share's, not the file's
	down := true
	Logf = func(string, ...any) { down = false } // back after the first wait
	probe = func(string) (bool, error) {
		if down {
			return false, syscall.EHOSTDOWN
		}
		return false, nil
	}
	tries = 0
	if err := DoFile(ctx, "a", p, Fixed(100, 7), func() error {
		if tries++; tries == 2 {
			return nil
		}
		time.Sleep(30 * time.Millisecond)
		return syscall.EIO
	}, dir); err != nil || tries != 2 || len(Unreadable()) != 0 {
		t.Fatalf("share away: %v after %d tries, %+v", err, tries, Unreadable())
	}
	// none left: no list kept
	DoFile(ctx, "a", p, Fixed(100, 7), hang, dir)
	Forget()
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty list kept: %v", err)
	}
	probe = func(string) (bool, error) { return false, nil }
	// the work says who hung: a slow try whose target hung is not the
	// file's fault, a quick one whose read hung is
	tries = 0
	if err := DoFile(ctx, "a", p, Fixed(100, 7), func() error {
		tries++
		time.Sleep(30 * time.Millisecond)
		return Blame(syscall.EBADF, false)
	}, dir); !errors.Is(err, syscall.EBADF) || tries != 3 || len(Unreadable()) != 0 {
		t.Fatalf("target hung: %v after %d tries, %+v", err, tries, Unreadable())
	}
	if err := DoFile(ctx, "a", p, Fixed(100, 7), func() error { return Blame(syscall.EBADF, true) }, dir); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("read hung: %v", err)
	}
}

// A file with a strike is tried alone: it waits for other work through
// DoFile to end, and other work waits for it.
func TestDoFileAlone(t *testing.T) {
	defer func(h, p time.Duration, l func(string, ...any), pr func(string) (bool, error)) {
		HangTime, StrikePause, Logf, probe = h, p, l, pr
		LoadUnreadable("")
	}(HangTime, StrikePause, Logf, probe)
	HangTime, StrikePause = time.Hour, 0
	Logf = func(string, ...any) {}
	probe = func(string) (bool, error) { return false, nil }
	LoadUnreadable("")
	ctx := context.Background()
	dir := In("/mnt/nas/a", "/mnt/nas")
	DoFile(ctx, "s", "/mnt/nas/a/s.jpg", Fixed(1, 1), func() error { return Blame(syscall.EIO, true) }, dir) // one strike

	var order []string
	var mu sync.Mutex
	log := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // other work under way
		defer wg.Done()
		DoFile(ctx, "o", "/mnt/nas/a/o.jpg", Fixed(1, 1), func() error { close(started); <-release; log("other"); return nil }, dir)
	}()
	<-started
	go func() { // the file with the strike waits for it
		defer wg.Done()
		DoFile(ctx, "s", "/mnt/nas/a/s.jpg", Fixed(1, 1), func() error { log("alone"); return nil }, dir)
	}()
	for i := 0; i < 100; i++ { // until it waits
		gate.Lock()
		w := gate.waiting
		gate.Unlock()
		if w == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	go func() { // new work waits for the file with the strike
		defer wg.Done()
		DoFile(ctx, "n", "/mnt/nas/a/n.jpg", Fixed(1, 1), func() error { log("new"); return nil }, dir)
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if len(order) != 3 || order[0] != "other" || order[1] != "alone" || order[2] != "new" {
		t.Fatalf("order: %v", order)
	}
	if len(Unreadable()) != 0 {
		t.Fatalf("worked alone, still listed: %+v", Unreadable())
	}
	// two tries of a file without strikes hang together (backup and
	// metadata): one strike, not put aside without a try alone
	both := make(chan struct{})
	var n sync.WaitGroup
	n.Add(2)
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			DoFile(ctx, "t", "/mnt/nas/a/t.jpg", Fixed(1, 1), func() error {
				n.Done()
				<-both
				return Blame(syscall.EIO, true)
			}, dir)
		}()
	}
	n.Wait()
	close(both)
	wg.Wait()
	if b := Unreadable(); len(b) != 1 || b[0].Strikes != 1 || b[0].Aside() {
		t.Fatalf("hung together: %+v", b)
	}
	Forget()
	// cancelled while waiting: nothing stays blocked
	gate.Lock()
	gate.readers++
	gate.Unlock()
	DoFile(ctx, "s", "/mnt/nas/a/s.jpg", Fixed(1, 1), func() error { return Blame(syscall.EIO, true) }, dir)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := DoFile(cctx, "s", "/mnt/nas/a/s.jpg", Fixed(1, 1), func() error { return nil }, dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled wait: %v", err)
	}
	leave(false)
	if err := DoFile(ctx, "n", "/mnt/nas/a/n.jpg", Fixed(1, 1), func() error { return nil }, dir); err != nil {
		t.Fatalf("after a cancelled wait: %v", err)
	}
}

type fakeInfo struct {
	os.FileInfo
	size, mtime int64
}

func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) ModTime() time.Time { return time.Unix(f.mtime, 0) }

// A caller with an older size and time (an index not scanned since) does
// not clear a strike: the file itself tells whether it changed.
func TestAvoidedStaleSignature(t *testing.T) {
	defer func(l func(string, ...any), st func(string) (os.FileInfo, error)) {
		Logf, statFile = l, st
		LoadUnreadable("")
	}(Logf, statFile)
	Logf = func(string, ...any) {}
	LoadUnreadable("")
	const p = "/mnt/nas/a/b.jpg"
	strike(p, 200, 9, true, syscall.EIO)
	strike(p, 200, 9, true, syscall.EIO)
	now := fakeInfo{size: 200, mtime: 9}
	statFile = func(string) (os.FileInfo, error) { return now, nil }
	if err := Avoided(p, 100, 7); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("old signature cleared the strike: %v", err)
	}
	statFile = func(string) (os.FileInfo, error) { return nil, syscall.EHOSTDOWN }
	if err := Avoided(p, 100, 7); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("no answer cleared the strike: %v", err)
	}
	// gone with an unmounted share (an empty mount point): kept
	statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	defer func(p func(string) (bool, error)) { probe = p }(probe)
	probe = func(string) (bool, error) { return true, nil }
	Avoided(p, 100, 7)
	if len(Unreadable()) != 1 {
		t.Fatal("strike forgotten while the share was unmounted")
	}
	now = fakeInfo{size: 300, mtime: 11}
	statFile = func(string) (os.FileInfo, error) { return now, nil }
	if err := Avoided(p, 100, 7); err != nil || len(Unreadable()) != 0 {
		t.Fatalf("changed file still left out: %v, %+v", err, Unreadable())
	}
	// one strike, pause over: tried again only through DoFile
	defer func(d time.Duration) { StrikePause = d }(StrikePause)
	StrikePause = 0
	strike(p, 100, 7, false, syscall.EIO)
	if Avoided(p, 100, 7) == nil || Due(p, 100, 7) != nil {
		t.Fatalf("pause over: avoided %v, due %v", Avoided(p, 100, 7), Due(p, 100, 7))
	}
}
