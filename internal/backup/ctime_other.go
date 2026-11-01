//go:build !linux && !darwin

package backup

import "os"

// changeTime: no inode change time here; size and modification time decide.
func changeTime(os.FileInfo) int64 { return 0 }
