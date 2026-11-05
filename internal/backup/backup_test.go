package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dgrieser/schuhkarton/internal/schedule"
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

// A cancelled run (Schuhkarton stopped) goes on soon after the next start;
// a complete one waits for the interval.
func TestLoopResumesIncompleteRun(t *testing.T) {
	for _, c := range []struct {
		complete bool
		soon     bool
	}{{false, true}, {true, false}} {
		dir := t.TempDir()
		last := Status{Finished: time.Now().Add(-time.Hour), Error: "context canceled", Complete: c.complete}
		b, _ := json.Marshal(last)
		os.WriteFile(filepath.Join(dir, "schuhkarton-backup.json"), b, 0o600)
		r := New(Options{Dir: dir, Every: 24 * time.Hour})
		ctx, cancel := context.WithCancel(context.Background())
		go r.Loop(ctx)
		var next time.Time
		for range 100 {
			if next = r.Status().Next; !next.IsZero() {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		if soon := time.Until(next) <= 2*time.Minute+time.Second; soon != c.soon {
			t.Errorf("complete=%v: next run in %v", c.complete, time.Until(next).Round(time.Minute))
		}
	}
}

// A file that cannot be copied leaves the run incomplete.
func TestBackupFailedFileIncomplete(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "a.jpg"), []byte("aaa"), 0o644)
	os.WriteFile(filepath.Join(src, "b.jpg"), []byte("bbb"), 0)
	r := New(Options{Dir: dst, Sources: []Source{{"Bilder", src}}, Originals: true})
	r.run(context.Background())
	if st := r.Status(); st.Failed != 1 || st.Complete {
		t.Fatalf("run with a failed file: %+v", st)
	}
	// what is on disk says so too
	if again := New(Options{Dir: dst}); again.Status().Complete {
		t.Fatal("status file says complete")
	}
}

// A share that keeps whole seconds only must not make every file look new.
func TestBackupToleratesCoarseTimes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "a.jpg"), []byte("aaa"), 0o644)
	fine := time.Date(2024, 5, 1, 12, 0, 0, 123456789, time.Local)
	os.Chtimes(filepath.Join(src, "a.jpg"), fine, fine)
	r := New(Options{Dir: dst, Sources: []Source{{"Bilder", src}}, Originals: true})
	r.run(context.Background())
	coarse := fine.Truncate(time.Second) // what the share kept
	os.Chtimes(filepath.Join(dst, "originals/Bilder/a.jpg"), coarse, coarse)
	r.run(context.Background())
	if st := r.Status(); st.Copied != 0 {
		t.Fatalf("copied again because of the time's fraction: %+v", st)
	}
}

// A same-size change within the same second is still copied: the record of
// the last copy keeps the exact time.
func TestBackupSeesQuickSameSizeChange(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	f := filepath.Join(src, "people.json")
	os.WriteFile(f, []byte("aaaa"), 0o644)
	t0 := time.Date(2024, 5, 1, 12, 0, 0, 100_000_000, time.Local)
	os.Chtimes(f, t0, t0)
	r := New(Options{Dir: dst, Sources: []Source{{"Bilder", src}}, Originals: true})
	r.run(context.Background())
	copyPath := filepath.Join(dst, "originals/Bilder/people.json")
	coarse := t0.Truncate(time.Second) // the share keeps whole seconds
	os.Chtimes(copyPath, coarse, coarse)
	os.WriteFile(f, []byte("bbbb"), 0o644) // same size, 0.5 s later
	t1 := t0.Add(500 * time.Millisecond)
	os.Chtimes(f, t1, t1)
	r.run(context.Background())
	if b, _ := os.ReadFile(copyPath); string(b) != "bbbb" {
		t.Fatalf("change not copied: %q (%+v)", b, r.Status())
	}
}

// A rewrite that keeps size and modification time (metadata of the same
// length, written with exiftool -P) is copied too, where the inode change
// time is known.
func TestBackupSeesRewriteWithKeptTime(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	f := filepath.Join(src, "a.jpg")
	os.WriteFile(f, []byte("aaaa"), 0o644)
	t0 := time.Date(2024, 5, 1, 12, 0, 0, 0, time.Local)
	os.Chtimes(f, t0, t0)
	st, _ := os.Stat(f)
	if changeTime(st) == 0 {
		t.Skip("no inode change time on this system")
	}
	r := New(Options{Dir: dst, Sources: []Source{{"Bilder", src}}, Originals: true})
	r.run(context.Background())
	time.Sleep(20 * time.Millisecond) // a later change time
	tmp := f + ".tmp"                 // like exiftool: a new file in its place, the old time kept
	os.WriteFile(tmp, []byte("bbbb"), 0o644)
	os.Chtimes(tmp, t0, t0)
	os.Rename(tmp, f)
	r.run(context.Background())
	if b, _ := os.ReadFile(filepath.Join(dst, "originals/Bilder/a.jpg")); string(b) != "bbbb" {
		t.Fatalf("rewrite not copied: %q (%+v)", b, r.Status())
	}
	r.run(context.Background())
	if st := r.Status(); st.Copied != 0 {
		t.Errorf("copied again without a change: %+v", st)
	}
}

// A copy changed in the backup folder is replaced again.
func TestBackupRepairsTouchedCopy(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "a.jpg"), []byte("aaaa"), 0o644)
	r := New(Options{Dir: dst, Sources: []Source{{"Bilder", src}}, Originals: true})
	r.run(context.Background())
	cp := filepath.Join(dst, "originals/Bilder/a.jpg")
	os.WriteFile(cp, []byte("xxxx"), 0o644) // same size, touched later
	later := time.Now().Add(time.Hour)
	os.Chtimes(cp, later, later)
	r.run(context.Background())
	if b, _ := os.ReadFile(cp); string(b) != "aaaa" {
		t.Fatalf("touched copy kept: %q", b)
	}
}

// On a precise file system a copy changed right after the backup is noticed.
func TestBackupRepairsQuicklyTouchedCopy(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "a.jpg"), []byte("aaaa"), 0o644)
	r := New(Options{Dir: dst, Sources: []Source{{"Bilder", src}}, Originals: true})
	r.run(context.Background())
	cp := filepath.Join(dst, "originals/Bilder/a.jpg")
	fi, _ := os.Stat(cp)
	os.WriteFile(cp, []byte("xxxx"), 0o644)
	soon := fi.ModTime().Add(300 * time.Millisecond)
	if soon.Nanosecond() == 0 {
		soon = soon.Add(time.Millisecond)
	}
	os.Chtimes(cp, soon, soon)
	r.run(context.Background())
	if b, _ := os.ReadFile(cp); string(b) != "aaaa" {
		t.Fatalf("quickly touched copy kept: %q", b)
	}
}

// Outside the time window a regular backup waits for it; one started by
// hand runs at once.
func TestLoopWaitsForWindow(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "a.jpg"), []byte("a"), 0o644)
	now := time.Now()
	m := now.Hour()*60 + now.Minute()
	w := schedule.Daily((m+120)%1440, (m+180)%1440) // opens in two hours
	r := New(Options{Dir: dir, Sources: []Source{{"Bilder", src}}, Originals: true, Every: 24 * time.Hour, Window: w})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Loop(ctx)
	var next time.Time
	for range 100 {
		if next = r.Status().Next; !next.IsZero() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !next.Equal(w.Next(time.Now())) {
		t.Errorf("next run %v, window opens %v", next, w.Next(time.Now()))
	}
	r.Trigger()
	for range 200 {
		if st := r.Status(); !st.Finished.IsZero() && !st.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := r.Status(); st.Copied != 1 {
		t.Errorf("by hand: %+v", st)
	}
}

// A run cut short by its context is not complete, also when only the
// settings and cache files were left.
func TestCancelledRunIncomplete(t *testing.T) {
	dir, cfgDir := t.TempDir(), t.TempDir()
	cfg := filepath.Join(cfgDir, "config.yaml")
	os.WriteFile(cfg, []byte("x: 1"), 0o600)
	r := New(Options{Dir: dir, ConfigPath: cfg, Config: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.run(ctx)
	if st := r.Status(); st.Complete || st.Copied != 0 {
		t.Errorf("cancelled run: %+v", st)
	}
}

func TestTransientSkipped(t *testing.T) {
	r := &Runner{}
	for _, n := range []string{"079.JPG_exiftool_tmp", "a.MOV.xmp_", "b.jpg.sk-tmp"} {
		if !r.excluded(n) {
			t.Errorf("%s: should be skipped", n)
		}
	}
	for _, n := range []string{"079.JPG", "a.MOV.xmp", "tmp.jpg"} {
		if r.excluded(n) {
			t.Errorf("%s: should be kept", n)
		}
	}
}
