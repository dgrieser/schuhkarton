package config

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultConfigPath is $XDG_CONFIG_HOME/schuhkarton/config.yaml or the OS
// equivalent (Application Support on macOS, %AppData% on Windows).
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "schuhkarton.yaml"
	}
	return filepath.Join(dir, "schuhkarton", "config.yaml")
}

// DefaultCacheDir is $XDG_CACHE_HOME/schuhkarton or the OS equivalent
// (Library/Caches on macOS, %LocalAppData% on Windows).
func DefaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "schuhkarton")
	}
	return filepath.Join(dir, "schuhkarton")
}

// ExpandHome turns a leading ~ into the home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
