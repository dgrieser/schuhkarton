//go:build linux || darwin || freebsd

package setup

import "golang.org/x/sys/unix"

// freeBytes is the space left for unprivileged users, 0 when unknown.
func freeBytes(path string) uint64 {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize) //nolint:gosec,unconvert // positive; types differ by OS
}
