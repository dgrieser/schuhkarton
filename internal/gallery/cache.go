package gallery

import (
	"compress/gzip"
	"encoding/gob"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// CacheInfo describes a cache folder (an existing one to reuse or import).
type CacheInfo struct {
	Path   string         `json:"path"`
	Exists bool           `json:"exists"`
	Valid  bool           `json:"valid"` // has an index
	Items  int            `json:"items"`
	Roots  map[string]int `json:"roots,omitempty"` // folder name -> items
	Files  int            `json:"files"`           // thumbnails, previews, face crops
	Bytes  int64          `json:"bytes"`
	People bool           `json:"people"`
	Favs   bool           `json:"favorites"`
	InUse  bool           `json:"inUse"` // locked by a running process
}

// InspectCache looks at a cache folder without changing it.
func InspectCache(dir string) CacheInfo {
	ci := CacheInfo{Path: dir}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ci
	}
	ci.Exists = true
	if f, err := os.Open(filepath.Join(dir, "index.gob.gz")); err == nil {
		if zr, err := gzip.NewReader(f); err == nil {
			var idx indexFile
			if gob.NewDecoder(zr).Decode(&idx) == nil {
				ci.Valid, ci.Items, ci.Roots = true, len(idx.Items), map[string]int{}
				for _, it := range idx.Items {
					ci.Roots[it.RootName]++
				}
			}
		}
		f.Close()
	}
	for _, sub := range []string{"thumbs", "faces"} {
		filepath.WalkDir(filepath.Join(dir, sub), func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				ci.Files++
				if info, err := d.Info(); err == nil {
					ci.Bytes += info.Size()
				}
			}
			return nil
		})
	}
	_, err := os.Stat(filepath.Join(dir, "people.json"))
	ci.People = err == nil
	_, err = os.Stat(filepath.Join(dir, "favorites.json"))
	ci.Favs = err == nil
	if exists(filepath.Join(dir, "lock")) {
		if lf, err := lockCache(filepath.Join(dir, "lock")); err != nil {
			ci.InUse = true
		} else {
			lf.Close()
		}
	}
	return ci
}

// FindCaches returns existing caches among the candidates, largest first.
func FindCaches(cands []string) []CacheInfo {
	var out []CacheInfo
	seen := map[string]bool{}
	for _, c := range cands {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if ci := InspectCache(c); ci.Valid {
			out = append(out, ci)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Items > out[b].Items })
	return out
}

// ImportCache copies another cache into dst: thumbnails, previews and face
// crops that dst does not have yet, and the index, people and favorites when
// dst has none. Thumbnails only match when the folder names and the files
// are the same as where the cache was made. progress gets copied/total files.
func ImportCache(src, dst string, progress func(done, total int)) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	var files []string
	for _, sub := range []string{"thumbs", "faces"} {
		filepath.WalkDir(filepath.Join(src, sub), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Ext(p) != ".tmp" {
				files = append(files, p)
			}
			return nil
		})
	}
	for _, f := range []string{"index.gob.gz", "people.json", "favorites.json", "edits.json", "trash.json", "ai.json", "marks.json", "notes.json", "dupes.json", "import.json", "roots.json"} {
		if exists(filepath.Join(src, f)) && !exists(filepath.Join(dst, f)) {
			files = append(files, filepath.Join(src, f))
		}
	}
	for i, p := range files {
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if !exists(target) {
			if err := copyFile(p, target); err != nil {
				return err
			}
		}
		if progress != nil && (i%200 == 0 || i == len(files)-1) {
			progress(i+1, len(files))
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := in.Stat(); err == nil {
		os.Chtimes(tmp, st.ModTime(), st.ModTime())
	}
	return os.Rename(tmp, dst)
}
