//go:build unix

package gallery

import "syscall"

// setNice lowers the CPU priority of this process (the convert child of
// background work).
func setNice(n int) { syscall.Setpriority(syscall.PRIO_PROCESS, 0, n) }
