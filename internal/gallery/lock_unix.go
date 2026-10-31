//go:build !windows

package gallery

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockCache takes an exclusive lock on path for the life of the process.
func lockCache(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
