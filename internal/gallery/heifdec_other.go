//go:build !linux || !(amd64 || arm64 || arm)

package gallery

// No bundled heif-dec for this platform.
var bundledHEIFGz []byte
