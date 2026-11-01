//go:build !unix && !windows

package gallery

func dirIdentity(path string) (string, bool) { return "", false }
