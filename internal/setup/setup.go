// Package setup checks what the setup wizard and "schuhkarton doctor" need
// to know: folders, free space, external tools and face models.
package setup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/gallery"
)

// Folder describes a photo folder or the cache folder.
type Folder struct {
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	Readable  bool   `json:"readable"`
	Writable  bool   `json:"writable"`
	FreeBytes uint64 `json:"freeBytes"`
	Photos    int    `json:"photos"`
	Videos    int    `json:"videos"`
	Truncated bool   `json:"truncated"` // counting stopped early
	Problem   string `json:"problem,omitempty"`
}

// CheckPhotos looks at a photo folder and counts its pictures (for at most
// a few seconds).
func CheckPhotos(path string, budget time.Duration) Folder {
	f := Folder{Path: path}
	if !filepath.IsAbs(path) {
		f.Problem = "Bitte einen vollständigen Pfad angeben."
		return f
	}
	st, err := os.Stat(path)
	if err != nil {
		f.Problem = "Ordner nicht gefunden."
		if !errors.Is(err, fs.ErrNotExist) {
			f.Problem = "Ordner nicht lesbar: " + err.Error()
		}
		return f
	}
	if !st.IsDir() {
		f.Problem = "Das ist eine Datei, kein Ordner."
		return f
	}
	f.Exists = true
	if _, err := os.ReadDir(path); err != nil {
		f.Problem = "Ordner nicht lesbar: " + err.Error()
		return f
	}
	f.Readable = true
	f.FreeBytes = freeBytes(path)
	deadline := time.Now().Add(budget)
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			f.Truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if p != path && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		switch gallery.KindOf(d.Name()) {
		case gallery.Photo:
			f.Photos++
		case gallery.Video:
			f.Videos++
		}
		return nil
	})
	if f.Photos+f.Videos == 0 && !f.Truncated {
		f.Problem = "Keine Fotos oder Videos gefunden."
	}
	return f
}

// CheckCache looks at the cache folder: can Schuhkarton write there?
func CheckCache(path string) Folder {
	f := Folder{Path: path}
	if !filepath.IsAbs(path) {
		f.Problem = "Bitte einen vollständigen Pfad angeben."
		return f
	}
	st, err := os.Stat(path)
	switch {
	case err == nil && !st.IsDir():
		f.Problem = "Das ist eine Datei, kein Ordner."
		return f
	case err == nil:
		f.Exists, f.Readable = true, true
	}
	probe := path
	for !dirExists(probe) {
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	f.Writable = writable(probe)
	f.FreeBytes = freeBytes(probe)
	if !f.Writable {
		f.Problem = "Kein Schreibrecht in " + probe
	} else if f.FreeBytes > 0 && f.FreeBytes < 2<<30 {
		f.Problem = "Weniger als 2 GB frei: Vorschaubilder brauchen etwa 5 % der Fotogröße."
	}
	return f
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".schuhkarton-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Entry is one sub folder in the folder picker.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Listing is the folder picker's view of one folder.
type Listing struct {
	Path   string  `json:"path"`
	Parent string  `json:"parent,omitempty"`
	Dirs   []Entry `json:"dirs"`
	Roots  []Entry `json:"roots"` // shortcuts: home, pictures, mounts, drives
	Error  string  `json:"error,omitempty"`
}

// Browse lists the sub folders of path (the home folder when empty).
func Browse(path string) Listing {
	home, _ := os.UserHomeDir()
	if path == "" {
		path = home
	}
	l := Listing{Path: filepath.Clean(path), Roots: shortcuts(home)}
	if parent := filepath.Dir(l.Path); parent != l.Path {
		l.Parent = parent
	}
	ents, err := os.ReadDir(l.Path)
	if err != nil {
		l.Error = err.Error()
		return l
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(l.Path, e.Name())
		if e.IsDir() || (e.Type()&fs.ModeSymlink != 0 && dirExists(full)) {
			l.Dirs = append(l.Dirs, Entry{e.Name(), full})
		}
	}
	sort.Slice(l.Dirs, func(a, b int) bool { return strings.ToLower(l.Dirs[a].Name) < strings.ToLower(l.Dirs[b].Name) })
	return l
}

func shortcuts(home string) []Entry {
	var out []Entry
	add := func(name, p string) {
		if p != "" && dirExists(p) {
			out = append(out, Entry{name, p})
		}
	}
	add("Persönlicher Ordner", home)
	for _, n := range []string{"Bilder", "Pictures", "Fotos", "Photos", "Videos", "Downloads/Bilder"} {
		add(n, filepath.Join(home, n))
	}
	switch runtime.GOOS {
	case "windows":
		for c := 'C'; c <= 'Z'; c++ {
			add(string(c)+":", string(c)+`:\`)
		}
	case "darwin":
		if ents, err := os.ReadDir("/Volumes"); err == nil {
			for _, e := range ents {
				add(e.Name(), filepath.Join("/Volumes", e.Name()))
			}
		}
	default:
		for _, base := range []string{"/mnt", "/media", "/srv", "/run/media/" + filepath.Base(home)} {
			if ents, err := os.ReadDir(base); err == nil {
				for _, e := range ents {
					add(e.Name(), filepath.Join(base, e.Name()))
				}
			}
		}
	}
	return out
}

// Tool is an external program.
type Tool struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	Needed  string `json:"needed"` // what it is for
}

// Tools reports exiftool and ffmpeg.
func Tools(exiftool, ffmpeg string) []Tool {
	et := Tool{Name: "exiftool", Path: gallery.FindExifTool(exiftool), Needed: "Aufnahmedatum, Ort, Kamera und Bildtexte"}
	if et.Path != "" {
		et.Version = firstLine(run(et.Path, "-ver"))
	}
	ff := Tool{Name: "ffmpeg", Path: gallery.FindFFmpeg(ffmpeg), Needed: "Video-Vorschauen und Abspielen von HEVC-Videos"}
	if ff.Path != "" {
		v := firstLine(run(ff.Path, "-hide_banner", "-version"))
		v = strings.TrimPrefix(v, "ffmpeg version ")
		if i := strings.IndexByte(v, ' '); i > 0 {
			v = v[:i]
		}
		ff.Version = v
	}
	tools := []Tool{et, ff}
	if gallery.SlowHEIF() { // AVIF: minutes per photo without it
		tools = append(tools, Tool{Name: "heif-dec", Path: gallery.FindHEIFTool(), Needed: "AVIF-Fotos schnell öffnen (auf diesem Prozessor ist der eingebaute Weg sehr langsam); HEIC liest Schuhkarton selbst"})
	}
	return tools
}

func run(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if strings.Contains(name, "exiftool") {
		if lib := filepath.Join(filepath.Dir(name), "../share/perl5"); dirExists(lib) {
			cmd.Env = append(os.Environ(), "PERL5LIB="+lib)
		}
	}
	out, _ := cmd.Output()
	return string(out)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// CacheCandidates are places where earlier caches may be.
func CacheCandidates(current string) []string {
	out := []string{current}
	if dir, err := os.UserCacheDir(); err == nil {
		out = append(out, filepath.Join(dir, "schuhkarton"), filepath.Join(dir, "galerie"))
	}
	return out
}
