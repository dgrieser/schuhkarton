package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Home is the data folder chosen with SCHUHKARTON_HOME (or --home): it
// holds config.yaml, users.json and the cache (cache/), so one folder
// moves everything to another machine. Empty: the usual OS locations.
func Home() string {
	h := ExpandHome(strings.TrimSpace(os.Getenv("SCHUHKARTON_HOME")))
	if h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			return abs
		}
	}
	return h
}

// DefaultConfigPath is <data folder>/config.yaml, or
// $XDG_CONFIG_HOME/schuhkarton/config.yaml or the OS equivalent
// (Application Support on macOS, %AppData% on Windows).
func DefaultConfigPath() string {
	if h := Home(); h != "" {
		return filepath.Join(h, "config.yaml")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "schuhkarton.yaml"
	}
	return filepath.Join(dir, "schuhkarton", "config.yaml")
}

// DefaultCacheDir is <data folder>/cache, or $XDG_CACHE_HOME/schuhkarton or
// the OS equivalent (Library/Caches on macOS, %LocalAppData% on Windows).
func DefaultCacheDir() string {
	if h := Home(); h != "" {
		return filepath.Join(h, "cache")
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "schuhkarton")
	}
	return filepath.Join(dir, "schuhkarton")
}

// besideConfig makes a relative path relative to the folder of the
// configuration file (so a data folder can move as a whole).
func besideConfig(configPath, p string) string {
	if p == "" || filepath.IsAbs(p) || configPath == "" {
		return p
	}
	return filepath.Join(filepath.Dir(configPath), p)
}

// relToConfig writes a path inside the configuration file's folder as a
// relative one, so the folder can move; others stay as they are.
func relToConfig(configPath, p string) string {
	if p == "" || configPath == "" || !filepath.IsAbs(p) {
		return p
	}
	rel, err := filepath.Rel(filepath.Dir(configPath), p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.ToSlash(rel)
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
