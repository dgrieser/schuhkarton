// Package nasretry retries file work on a network share after the share
// went away for a while. A Fritz!Box answering SMB stops responding for a
// few minutes under load (the kernel logs "has not responded in 180
// seconds. Reconnecting..."); every read and write in that time fails with
// an I/O error. Counting those as failed puts the files aside for hours;
// waiting for the share and trying again gets them done in the same pass.
package nasretry

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Delays are the waits before each new try. Their sum covers a CIFS
// reconnect (180 s until the kernel gives up on the old connection). After
// the last one Do keeps waiting that long between probes, until MaxWait has
// passed: the share can stay away for hours, and every file given up on then
// would wait for retry_failed.
var Delays = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

// MaxWait bounds how long Do waits for the share in all.
var MaxWait = 6 * time.Hour

// Logf reports the waits.
var Logf = log.Printf

// outageErrnos are what a share that went away answers with.
var outageErrnos = []error{
	syscall.EIO, syscall.EBADF, syscall.ENOTCONN, syscall.ECONNRESET, syscall.ECONNABORTED,
	syscall.ETIMEDOUT, syscall.EHOSTDOWN, syscall.EHOSTUNREACH, syscall.ENETDOWN,
	syscall.ENETUNREACH, syscall.ESTALE,
}

// outageTexts are the same from programs that only report text (exiftool).
var outageTexts = []string{
	"error writing output file", "error renaming temporary file", "input/output error",
	"bad file descriptor", "host is down", "stale file handle", "connection reset",
	// exiftool without the cause: the share is asked before waiting
	"error opening file", "error creating temporary file",
}

// suspectTexts are what exiftool says when a read or write broke off, but
// also for a file that is really bad or gone: on a share that drops for a
// moment a file is "not found" or has a "format error" too. Such errors
// count as an outage only while the share does not answer (see away);
// while it answers, they get the same few tries as an outage error.
var suspectTexts = []string{"file not found", "no such file", "format error in file", "error creating file"}

// suspect tells whether err may come from the share dropping for a moment.
func suspect(err error) bool {
	var f final
	if errors.As(err, &f) || errors.Is(err, ErrUnreadable) {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, t := range suspectTexts {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// retryable tells whether err may go away with the next try.
func retryable(err error) bool { return Outage(err) || suspect(err) }

// away tells whether err comes from the share being away: an outage error,
// or a suspect one while dirs do not answer.
func away(err error, dirs []Place) bool {
	return Outage(err) || suspect(err) && !Reachable(dirs...)
}

// final marks an error that must not be tried again, whatever it says.
type final struct{ error }

func (f final) Unwrap() error { return f.error }

// Final marks err as one that must not be tried again even if it looks like
// an outage: the try left something behind (a damaged file) that a new try
// would build on.
func Final(err error) error {
	if err == nil {
		return nil
	}
	return final{err}
}

// Outage tells whether err looks like the share being away rather than
// something wrong with the file.
func Outage(err error) bool {
	var f final
	if err == nil || errors.As(err, &f) || errors.Is(err, ErrUnreadable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, e := range outageErrnos {
		if errors.Is(err, e) {
			return true
		}
	}
	s := strings.ToLower(err.Error())
	for _, t := range outageTexts {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// Settle is the wait before the last try of a file that failed while the
// share answers: a reconnect breaks handles for a moment.
var Settle = 30 * time.Second

// Do runs fn. When it fails with an outage or suspect error while dirs
// answer, it runs fn once more at once (a handle broken by a reconnect)
// and once more after Settle; a failure after that is the file's own.
// While dirs do not answer (see Reachable), it waits (Delays, then for an
// outage error the last delay again until MaxWait has passed) and runs fn again once they
// do; a failure while they answer is again the file's own. It returns
// fn's last error, or ctx's when ctx ends while waiting. what names the
// file in the log.
func Do(ctx context.Context, what string, fn func() error, dirs ...Place) error {
	err := fn()
	if err == nil || ctx.Err() != nil || !retryable(err) {
		return err
	}
	if Reachable(dirs...) {
		down := false
		for i, d := range []time.Duration{0, Settle} {
			if i > 0 {
				Logf("%s: %v; neuer Versuch in %s", what, err, d)
				if !sleep(ctx, d) {
					return ctx.Err()
				}
				if down = !Reachable(dirs...); down {
					break
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err = fn(); err == nil || ctx.Err() != nil || !retryable(err) {
				return err
			}
			if down = !Reachable(dirs...); down {
				break
			}
		}
		if !down {
			return err // the share answers and the file still fails: its own problem
		}
	}
	start := time.Now()
	for i := 0; ; i++ {
		d := Delays[min(i, len(Delays)-1)]
		if i >= len(Delays) {
			left := MaxWait - time.Since(start)
			if left <= 0 || !Outage(err) {
				break // a suspect error may be the file's own (its folder gone): no longer than Delays
			}
			d = min(d, left)
			Logf("%s: Netzlaufwerk antwortet seit %s nicht, warte weiter (höchstens %s)", what, time.Since(start).Round(time.Minute), MaxWait)
		} else {
			Logf("%s: %v; Netzlaufwerk antwortet nicht, neuer Versuch %d/%d in %s", what, err, i+1, len(Delays), d)
		}
		if !sleep(ctx, d) {
			return ctx.Err()
		}
		if !Reachable(dirs...) {
			continue // still away: the next wait
		}
		if ctx.Err() != nil { // ended while the probe waited for the share
			return ctx.Err()
		}
		if err = fn(); err == nil || ctx.Err() != nil || !away(err, dirs) || Reachable(dirs...) {
			return err // done, or the share answers and the file still fails: its own problem
		}
	}
	return err
}

// sleep waits d, or less when ctx ends (then false).
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Place is where a file lives: Dir, its folder, below Root, the library
// or backup folder it belongs to.
type Place struct{ Dir, Root string }

// In is the Place of dir below root.
func In(dir, root string) Place { return Place{dir, root} }

// Reachable tells whether every place answers a listing (a real round
// trip, unlike a cached stat). A Dir that is gone (deleted or renamed
// meanwhile) counts when the nearest folder above it, up to Root, answers
// and is not empty: the share is there, the folder is not. An empty one
// may be the mount point a share left behind when it got unmounted, and a
// Root that is gone is one. Asking Dir first keeps a share mounted below
// Root (/photos/NAS) apart from Root's own disk.
func Reachable(places ...Place) bool {
	for _, pl := range places {
		root, d := filepath.Clean(pl.Root), filepath.Clean(pl.Dir)
		for {
			empty, err := probe(d)
			if err == nil {
				if empty && d != filepath.Clean(pl.Dir) {
					return false // maybe the mount point of a share that is gone
				}
				break
			}
			up := filepath.Dir(d)
			if !errors.Is(err, fs.ErrNotExist) || d == root || up == d || !within(up, root) {
				return false
			}
			d = up
		}
	}
	return true
}

// within tells whether dir is root or below it.
func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// probe lists one entry of dir and tells whether there is none.
var probe = func(dir string) (empty bool, err error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err = f.Readdirnames(1); errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}
