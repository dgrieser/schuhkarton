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
	"log"
	"os"
	"strings"
	"syscall"
	"time"
)

// Delays are the waits before each new try. Their sum covers a CIFS
// reconnect (180 s until the kernel gives up on the old connection).
var Delays = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

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
	if err == nil || errors.As(err, &f) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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

// Do runs fn. When it fails with an outage error while dirs answer, it
// runs fn once more at once (a handle broken by a reconnect); a second
// failure then is the file's own. While dirs do not answer, it waits
// (Delays) and runs fn again once they do; a failure while they answer is
// again the file's own. It returns fn's last error, or
// ctx's when ctx ends while waiting. what names the file in the log.
func Do(ctx context.Context, what string, fn func() error, dirs ...string) error {
	err := fn()
	if err == nil || ctx.Err() != nil || !Outage(err) {
		return err
	}
	if Reachable(dirs...) && ctx.Err() == nil {
		if err = fn(); err == nil || ctx.Err() != nil || !Outage(err) || Reachable(dirs...) {
			return err
		}
	}
	for i, d := range Delays {
		Logf("%s: %v; Netzlaufwerk antwortet nicht, neuer Versuch %d/%d in %s", what, err, i+1, len(Delays), d)
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		if !Reachable(dirs...) {
			continue // still away: the next wait
		}
		if ctx.Err() != nil { // ended while the probe waited for the share
			return ctx.Err()
		}
		if err = fn(); err == nil || ctx.Err() != nil || !Outage(err) || Reachable(dirs...) {
			return err // done, or the share answers and the file still fails: its own problem
		}
	}
	return err
}

// Reachable tells whether every dir answers a listing (a real round trip,
// unlike a cached stat).
func Reachable(dirs ...string) bool {
	for _, d := range dirs {
		f, err := os.Open(d)
		if err != nil {
			return false
		}
		_, err = f.Readdirnames(1)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return false
		}
	}
	return true
}
