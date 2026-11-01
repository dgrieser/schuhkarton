//go:build unix

package gallery

import (
	"fmt"
	"os"
	"syscall"
)

// dirIdentity is device and inode: a mount point and what is mounted there
// differ, also on the same disk.
func dirIdentity(path string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d", uint64(st.Dev), uint64(st.Ino)), true
}
