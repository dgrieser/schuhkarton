//go:build !linux

package gallery

// mountPoints: elsewhere mount points are found by the change of device.
func mountPoints() []string { return nil }
