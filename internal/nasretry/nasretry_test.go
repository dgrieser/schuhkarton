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
	defer func(d []time.Duration, l func(string, ...any)) { Delays, Logf = d, l }(Delays, Logf)
	Delays, Logf = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, func(string, ...any) {}
	ctx := context.Background()
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")

	// a handle broken by a reconnect, share answering: once more at once
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

	// failing twice while the share answers: the file's own problem
	n = 0
	err = Do(ctx, "b", func() error { n++; return syscall.EIO }, dir)
	if !errors.Is(err, syscall.EIO) || n != 2 {
		t.Errorf("file problem: %v after %d tries", err, n)
	}

	// share away for a while: waits until it answers, then goes on
	n = 0
	err = Do(ctx, "c", func() error {
		n++
		if n == 1 {
			return syscall.EIO
		}
		return nil
	}, gone)
	if !errors.Is(err, syscall.EIO) || n != 1 {
		t.Errorf("share away all the time: %v after %d tries", err, n)
	}
	n = 0
	err = Do(ctx, "d", func() error {
		n++
		if n == 1 {
			os.MkdirAll(gone, 0o755) // back after the first wait
			return syscall.EIO
		}
		return nil
	}, gone)
	if err != nil || n != 2 {
		t.Errorf("share back: %v after %d tries", err, n)
	}

	// no outage: no new try
	n = 0
	if err := Do(ctx, "e", func() error { n++; return os.ErrPermission }, dir); !errors.Is(err, os.ErrPermission) || n != 1 {
		t.Errorf("permission: %v after %d tries", err, n)
	}

	// ctx ends while waiting
	Delays = []time.Duration{time.Hour}
	c, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := Do(c, "f", func() error { return syscall.EIO }, filepath.Join(dir, "nope")); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancel: %v", err)
	}
}
