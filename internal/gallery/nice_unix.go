//go:build unix

package gallery

import (
	"os/exec"
	"syscall"
)

// setNice lowers the CPU priority of this process (the convert child of
// background work).
func setNice(n int) { syscall.Setpriority(syscall.PRIO_PROCESS, 0, n) }

// niceStarted lowers the CPU priority of a started helper (ffmpeg,
// exiftool, heif-convert), so that answering the page comes first on a
// small machine. Threads it starts later take it over.
func niceStarted(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, helperNice)
	}
}
