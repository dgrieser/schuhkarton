package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupCopiesOnlyChanges(t *testing.T) {
	src, cache, dst := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "2020", "_Duplikate"), 0o755)
	os.WriteFile(filepath.Join(src, "2020", "a.jpg"), []byte("aaa"), 0o644)
	os.WriteFile(filepath.Join(src, "2020", "_Duplikate", "x.jpg"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(cache, "thumbs", "ab"), 0o755)
	os.WriteFile(filepath.Join(cache, "thumbs", "ab", "k-s.jpg"), []byte("t"), 0o644)
	os.WriteFile(filepath.Join(cache, "thumbs", "ab", "k-h.mp4"), []byte("big"), 0o644)
	os.WriteFile(filepath.Join(cache, "index.gob.gz"), []byte("i"), 0o644)
	r := New(Options{Dir: dst, Sources: []Source{{"Bilder", src}}, Exclude: []string{"_Duplikate"},
		CacheDir: cache, Originals: true, Cache: true})
	r.run(context.Background())
	st := r.Status()
	if st.Copied != 3 || st.Failed != 0 {
		t.Fatalf("first run: %+v", st)
	}
	for _, p := range []string{"originals/Bilder/2020/a.jpg", "cache/thumbs/ab/k-s.jpg", "cache/index.gob.gz"} {
		if _, err := os.Stat(filepath.Join(dst, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	for _, p := range []string{"originals/Bilder/2020/_Duplikate/x.jpg", "cache/thumbs/ab/k-h.mp4"} {
		if _, err := os.Stat(filepath.Join(dst, p)); err == nil {
			t.Errorf("should skip %s", p)
		}
	}
	r.run(context.Background())
	if st := r.Status(); st.Copied != 0 {
		t.Errorf("second run copied %d", st.Copied)
	}
	later := time.Now().Add(time.Hour)
	os.WriteFile(filepath.Join(src, "2020", "a.jpg"), []byte("changed"), 0o644)
	os.Chtimes(filepath.Join(src, "2020", "a.jpg"), later, later)
	os.Remove(filepath.Join(src, "2020", "a.jpg.gone"))
	r.run(context.Background())
	if st := r.Status(); st.Copied != 1 {
		t.Errorf("changed file: copied %d", st.Copied)
	}
}
