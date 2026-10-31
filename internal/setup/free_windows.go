//go:build windows

package setup

import "golang.org/x/sys/windows"

// freeBytes is the space left for the user, 0 when unknown.
func freeBytes(path string) uint64 {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, all uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &total, &all) != nil {
		return 0
	}
	return free
}
