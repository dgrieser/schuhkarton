//go:build linux

package backup

import (
	"os"
	"syscall"
)

// changeTime is the inode change time (ctime): it moves with every write,
// also when a program keeps the modification time (exiftool -P).
func changeTime(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec)
	}
	return 0
}
