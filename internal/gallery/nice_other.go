//go:build !unix

package gallery

import "os/exec"

func setNice(int) {}

func niceStarted(*exec.Cmd) {}
