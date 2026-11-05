package nasretry

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOutage(t *testing.T) {
	for _, err := range []error{
		&fs.PathError{Op: "write", Path: "x", Err: syscall.EBADF},
		fmt.Errorf("copy: %w", syscall.EIO),
		errors.New("Error: Error writing output file - /mnt/nas/a.jpg"),
	} {
		if !Outage(err) {
			t.Errorf("%v: should be an outage", err)
		}
	}
	if Outage(Final(fmt.Errorf("write: %w; restoring failed too", syscall.EIO))) {
		t.Error("a final error is no outage")
	}
	for _, err := range []error{nil, os.ErrNotExist, os.ErrPermission, context.Canceled, errors.New("Error: Not a valid JPEG")} {
		if Outage(err) {
			t.Errorf("%v: no outage", err)
		}
	}
}

func TestDo(t *testing.T) {
	defer func(d []time.Duration, m, st time.Duration, l func(string, ...any), p func(string) (bool, error)) {
		Delays, MaxWait, Settle, Logf, probe = d, m, st, l, p
	}(Delays, MaxWait, Settle, Logf, probe)
	Delays, MaxWait, Settle = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, 0, time.Millisecond
	ctx := context.Background()
	dir := In("/mnt/nas/a", "/mnt/nas")

	// a fake share: down until the wait (Logf call) numbered backAt
	var waits, backAt int
	Logf = func(string, ...any) { waits++ }
	probe = func(string) (bool, error) {
		if backAt == 0 || waits < backAt {
			return false, syscall.EHOSTDOWN
		}
		return false, nil
	}
	share := func(back int) { waits, backAt = 0, back }

	// a handle broken by a reconnect, share answering: once more at once
	share(-1)
	n := 0
	err := Do(ctx, "a", func() error {
		if n++; n == 1 {
			return syscall.EBADF
		}
		return nil
	}, dir)
	if err != nil || n != 2 {
		t.Errorf("broken handle: %v after %d tries", err, n)
	}

	// failing at once and after Settle while the share answers: the
	// file's own problem
	n = 0
	err = Do(ctx, "b", func() error { n++; return syscall.EIO }, dir)
	if !errors.Is(err, syscall.EIO) || n != 3 {
		t.Errorf("file problem: %v after %d tries", err, n)
	}

	// share away all the time: gives up after Delays
	share(0)
	n = 0
	err = Do(ctx, "c", func() error { n++; return syscall.EIO }, dir)
	if !errors.Is(err, syscall.EIO) || n != 1 || waits != 3 {
		t.Errorf("share away all the time: %v after %d tries, %d waits", err, n, waits)
	}

	// share back after the first wait
	share(1)
	n = 0
	err = Do(ctx, "d", func() error {
		if n++; n == 1 {
			return syscall.EIO
		}
		return nil
	}, dir)
	if err != nil || n != 2 {
		t.Errorf("share back: %v after %d tries", err, n)
	}

	// share away longer than Delays: keeps waiting until MaxWait
	MaxWait = time.Minute
	share(6)
	n = 0
	err = Do(ctx, "e", func() error {
		if n++; n == 1 {
			return syscall.EIO
		}
		return nil
	}, dir)
	if err != nil || n != 2 || waits != 6 {
		t.Errorf("long outage: %v after %d tries, %d waits", err, n, waits)
	}
	share(0)
	n = 0
	err = Do(ctx, "e2", func() error { n++; return errors.New("Error: File not found - x") }, dir)
	if n != 1 || waits != 3 {
		t.Errorf("suspect error, share away: %d tries, %d waits (want Delays only)", n, waits)
	}
	MaxWait = 5 * time.Millisecond
	share(0)
	start := time.Now()
	Do(ctx, "f", func() error { return syscall.EIO }, dir)
	if el := time.Since(start); el > time.Second {
		t.Errorf("MaxWait: gave up only after %s", el)
	}
	MaxWait = 0

	// exiftool "File not found": an outage while the share is away, the
	// file's own problem (one try) while it answers
	notFound := errors.New("Error: File not found - /mnt/nas/a/x.jpg")
	share(1)
	n = 0
	err = Do(ctx, "g", func() error {
		if n++; n == 1 {
			return notFound
		}
		return nil
	}, dir)
	if err != nil || n != 2 {
		t.Errorf("missing, share back: %v after %d tries", err, n)
	}
	share(-1)
	n = 0
	if err = Do(ctx, "h", func() error { n++; return notFound }, dir); err != notFound || n != 3 {
		t.Errorf("missing while the share answers: %v after %d tries", err, n)
	}

	// a read that broke off while the share answers: fine after Settle
	share(-1)
	n = 0
	err = Do(ctx, "h2", func() error {
		if n++; n < 3 {
			return errors.New("Error: Format error in file - /mnt/nas/a/x.jpg")
		}
		return nil
	}, dir)
	if err != nil || n != 3 {
		t.Errorf("format error: %v after %d tries", err, n)
	}
	if Final(notFound) != nil && retryable(Final(notFound)) {
		t.Error("a final error is not retryable")
	}

	// no outage: no new try
	n = 0
	if err := Do(ctx, "i", func() error { n++; return os.ErrPermission }, dir); !errors.Is(err, os.ErrPermission) || n != 1 {
		t.Errorf("permission: %v after %d tries", err, n)
	}

	// ctx ends while waiting
	share(0)
	Delays = []time.Duration{time.Hour}
	c, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := Do(c, "j", func() error { return syscall.EIO }, dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancel: %v", err)
	}
}

func TestReachable(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a")
	os.MkdirAll(filepath.Join(sub, "b"), 0o755)
	if !Reachable(In(sub, root)) {
		t.Error("an existing dir answers")
	}
	// deleted or renamed meanwhile: the share is there
	if !Reachable(In(filepath.Join(sub, "gone", "deeper"), root)) {
		t.Error("a gone folder below an answering root counts as reachable")
	}
	// an unmounted share: its root is gone
	gone := filepath.Join(root, "nas")
	if Reachable(In(filepath.Join(gone, "x"), gone)) {
		t.Error("a gone root does not answer")
	}
	// the empty mount point of a share unmounted below the root
	mp := filepath.Join(root, "mnt")
	os.Mkdir(mp, 0o755)
	if Reachable(In(filepath.Join(mp, "album"), root)) {
		t.Error("an empty folder above a gone one does not answer for it")
	}
	// a share mounted below the root is down while the root answers
	defer func(p func(string) (bool, error)) { probe = p }(probe)
	probe = func(d string) (bool, error) {
		if d == sub {
			return false, syscall.EHOSTDOWN
		}
		return false, nil
	}
	if Reachable(In(sub, root)) {
		t.Error("a share below the root that is down does not answer")
	}
}
