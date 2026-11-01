// Package backup copies the photo folders, the cache (thumbnails, previews,
// faces, index, people, favorites) and the settings into a backup folder,
// in the background and at an interval. It only adds and updates files;
// nothing is ever deleted from the backup.
//
// Layout of the backup folder:
//
//	originals/<folder name>/...   the photos and videos
//	cache/...                     index.gob.gz, people.json, favorites.json, edits.json, trash.json, ai.json, marks.json, notes.json, dupes.json, import.json, thumbs/, faces/
//	config/config.yaml            the settings (with tokens: keep the backup private)
//	config/users.json             accounts (password hashes)
//	schuhkarton-backup.json       the last run
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Source is one photo folder.
type Source struct {
	Name, Path string
}

// Options configure a backup.
type Options struct {
	Dir        string
	Sources    []Source
	Exclude    []string // glob patterns for names (as in the library)
	CacheDir   string
	ConfigPath string
	Originals  bool
	Cache      bool
	Config     bool
	Every      time.Duration
}

// Status describes the current or last run.
type Status struct {
	Enabled  bool      `json:"enabled"`
	Dir      string    `json:"dir"`
	Running  bool      `json:"running"`
	Phase    string    `json:"phase,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Next     time.Time `json:"next,omitzero"`
	Copied   int       `json:"copied"`
	Bytes    int64     `json:"bytes"`
	Checked  int       `json:"checked"`
	Failed   int       `json:"failed"`
	Error    string    `json:"error,omitempty"`
}

// Runner runs backups.
type Runner struct {
	opt Options
	now chan struct{}
	mu  sync.Mutex
	st  Status
}

// New prepares a runner; Loop starts it.
func New(opt Options) *Runner {
	r := &Runner{opt: opt, now: make(chan struct{}, 1)}
	r.st = Status{Enabled: opt.Dir != "", Dir: opt.Dir}
	if b, err := os.ReadFile(filepath.Join(opt.Dir, "schuhkarton-backup.json")); err == nil {
		var last Status
		if json.Unmarshal(b, &last) == nil {
			r.st.Finished, r.st.Copied, r.st.Bytes, r.st.Checked, r.st.Failed, r.st.Error =
				last.Finished, last.Copied, last.Bytes, last.Checked, last.Failed, last.Error
		}
	}
	return r
}

// Status returns the current state.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

// Trigger starts a backup soon (if none is running).
func (r *Runner) Trigger() {
	select {
	case r.now <- struct{}{}:
	default:
	}
}

// Loop runs a first backup after a short delay, then at the interval or
// when triggered, until ctx ends.
func (r *Runner) Loop(ctx context.Context) {
	if r.opt.Dir == "" {
		return
	}
	next := time.Now().Add(2 * time.Minute)
	if !r.st.Finished.IsZero() && r.opt.Every > 0 {
		next = r.st.Finished.Add(r.opt.Every)
		if time.Until(next) < 2*time.Minute {
			next = time.Now().Add(2 * time.Minute)
		}
	}
	for {
		r.mu.Lock()
		r.st.Next = next
		r.mu.Unlock()
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-r.now:
			t.Stop()
		}
		r.run(ctx)
		if r.opt.Every <= 0 {
			next = time.Now().Add(100 * 365 * 24 * time.Hour) // only on demand
		} else {
			next = time.Now().Add(r.opt.Every)
		}
	}
}

func (r *Runner) set(fn func(*Status)) {
	r.mu.Lock()
	fn(&r.st)
	r.mu.Unlock()
}

func (r *Runner) run(ctx context.Context) {
	r.set(func(s *Status) {
		*s = Status{Enabled: true, Dir: r.opt.Dir, Running: true, Started: time.Now(), Phase: "Vorbereiten"}
	})
	err := r.backup(ctx)
	r.set(func(s *Status) {
		s.Running, s.Phase, s.Finished = false, "", time.Now()
		if err != nil {
			s.Error = err.Error()
		}
	})
	st := r.Status()
	if b, err := json.MarshalIndent(st, "", " "); err == nil {
		os.WriteFile(filepath.Join(r.opt.Dir, "schuhkarton-backup.json"), b, 0o600)
	}
	log.Printf("Sicherung nach %s: %d Dateien kopiert (%d MB), %d geprüft, %d Fehler %s",
		r.opt.Dir, st.Copied, st.Bytes>>20, st.Checked, st.Failed, st.Error)
}

func (r *Runner) backup(ctx context.Context) error {
	if err := os.MkdirAll(r.opt.Dir, 0o700); err != nil {
		return fmt.Errorf("Sicherungsordner: %w", err)
	}
	if r.opt.Config && r.opt.ConfigPath != "" {
		r.set(func(s *Status) { s.Phase = "Einstellungen" })
		if _, err := os.Stat(r.opt.ConfigPath); err == nil {
			r.copyIfChanged(r.opt.ConfigPath, filepath.Join(r.opt.Dir, "config", "config.yaml"))
		}
		// accounts (password hashes) live next to the settings
		if users := filepath.Join(filepath.Dir(r.opt.ConfigPath), "users.json"); fileExists(users) {
			r.copyIfChanged(users, filepath.Join(r.opt.Dir, "config", "users.json"))
		}
	}
	if r.opt.Cache && r.opt.CacheDir != "" {
		r.set(func(s *Status) { s.Phase = "Cache" })
		for _, f := range []string{"index.gob.gz", "people.json", "favorites.json", "edits.json", "trash.json", "ai.json", "marks.json", "notes.json", "dupes.json", "import.json"} {
			if _, err := os.Stat(filepath.Join(r.opt.CacheDir, f)); err == nil {
				r.copyIfChanged(filepath.Join(r.opt.CacheDir, f), filepath.Join(r.opt.Dir, "cache", f))
			}
		}
		for _, sub := range []string{"thumbs", "faces"} {
			err := r.mirror(ctx, filepath.Join(r.opt.CacheDir, sub), filepath.Join(r.opt.Dir, "cache", sub), func(name string) bool {
				// browser transcodes are big and can be made again
				return strings.HasSuffix(name, "-h.mp4") || strings.Contains(name, ".tmp")
			})
			if err != nil {
				return err
			}
		}
	}
	if r.opt.Originals {
		for _, src := range r.opt.Sources {
			r.set(func(s *Status) { s.Phase = "Fotos: " + src.Name })
			if err := r.mirror(ctx, src.Path, filepath.Join(r.opt.Dir, "originals", src.Name), r.excluded); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) excluded(name string) bool {
	for _, pat := range r.opt.Exclude {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// mirror copies new and changed files from src into dst.
func (r *Runner) mirror(ctx context.Context, src, dst string, skip func(string) bool) error {
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			r.set(func(s *Status) { s.Failed++ })
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p != src && skip(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		r.copyIfChanged(p, filepath.Join(dst, rel))
		return nil
	})
}

// copyIfChanged copies when the target is missing or differs in size or
// modification time.
func (r *Runner) copyIfChanged(src, dst string) {
	si, err := os.Stat(src)
	if err != nil {
		r.set(func(s *Status) { s.Failed++ })
		return
	}
	r.set(func(s *Status) { s.Checked++ })
	if di, err := os.Stat(dst); err == nil && di.Size() == si.Size() && di.ModTime().Equal(si.ModTime()) {
		return
	}
	if err := copyFile(src, dst, si); err != nil {
		log.Printf("Sicherung %s: %v", src, err)
		r.set(func(s *Status) { s.Failed++ })
		return
	}
	r.set(func(s *Status) { s.Copied++; s.Bytes += si.Size() })
}

func copyFile(src, dst string, si os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".sk-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chtimes(tmp, si.ModTime(), si.ModTime()); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// Inside reports whether path is inside (or equal to) dir.
func Inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
