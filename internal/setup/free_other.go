//go:build !linux && !darwin && !freebsd && !windows

package setup

func freeBytes(string) uint64 { return 0 }
