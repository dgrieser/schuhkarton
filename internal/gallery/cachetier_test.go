package gallery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheTiers(t *testing.T) {
	defer func(g time.Duration) { spillGrace = g }(spillGrace)
	spillGrace = 0
	overflowSameDiskOK = true
	defer func() { overflowSameDiskOK = false }()
	root, over := t.TempDir(), t.TempDir()
	for i := range 4 {
		saveJPEG(t, filepath.Join(root, "2020", "IMG_2020010"+string(rune('1'+i))+"_120000.jpg"), picture(int64(200+i), 1600, 1200, 0), 90)
	}
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 2, Previews: true,
		MaxCache: 0, OverflowDir: over})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	l.GenerateAll(ctx)
	it := l.Items()[0]
	if !exists(l.CachePath(it, VarLarge)) {
		t.Fatal("no large preview in tier 1")
	}

	l.spill(ctx) // cap 0: every large file moves
	if exists(l.CachePath(it, VarLarge)) || !exists(l.overflowFile(it.key, VarLarge)) {
		t.Fatal("large preview not moved to tier 2")
	}
	if !exists(l.CachePath(it, VarSmall)) || exists(l.overflowFile(it.key, VarSmall)) {
		t.Error("thumbnail left tier 1")
	}
	if p, err := l.Ensure(ctx, it, VarLarge); err != nil || !strings.HasPrefix(p, over) {
		t.Errorf("served from %q (%v), want tier 2", p, err)
	}
	if exists(l.CachePath(it, VarLarge)) {
		t.Error("tier 2 file made again in tier 1")
	}
	l.mediaTodo() // counts a moved preview as there
	l.GenerateAll(ctx)
	if exists(l.CachePath(it, VarLarge)) {
		t.Error("background made a moved preview again")
	}

	// a written file: its tier 2 preview moves to the new key along
	old := it.key
	f, _ := os.OpenFile(l.Path(it), os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte{0})
	f.Close()
	os.Chtimes(l.Path(it), time.Now(), time.Now().Add(time.Minute))
	l.afterWrite(it, 0)
	if it.key == old || exists(l.overflowFile(old, VarLarge)) || !exists(l.overflowFile(it.key, VarLarge)) {
		t.Error("tier 2 preview did not follow the new key")
	}
	// written during the grace time: no copy stays here under the new key
	it2 := l.Items()[1]
	// as just after a move: in tier 2, and still here until spillGrace ends
	if err := copyFileAtomic(ctx, l.overflowFile(it2.key, VarLarge), l.CachePath(it2, VarLarge)); err != nil {
		t.Fatal(err)
	}
	old2 := it2.key
	f, _ = os.OpenFile(l.Path(it2), os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte{0})
	f.Close()
	os.Chtimes(l.Path(it2), time.Now(), time.Now().Add(2*time.Minute))
	l.afterWrite(it2, 0)
	if it2.key == old2 || exists(l.CachePath(it2, VarLarge)) || !exists(l.overflowFile(it2.key, VarLarge)) {
		t.Error("copy in tier 1 left under the new key")
	}
	l.removeDerivatives(it.key, 0)
	if exists(l.overflowFile(it.key, VarLarge)) {
		t.Error("tier 2 preview not removed")
	}
}

// Below the cap nothing moves; above it the oldest large files first.
func TestSpillOldestFirst(t *testing.T) {
	defer func(g time.Duration) { spillGrace = g }(spillGrace)
	spillGrace = 0
	overflowSameDiskOK = true
	defer func() { overflowSameDiskOK = false }()
	cache, over := t.TempDir(), t.TempDir()
	l := &Library{opt: Options{CacheDir: cache, OverflowDir: over}}
	mk := func(name string, size int, age time.Duration) string {
		p := filepath.Join(cache, "thumbs", name[:2], name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, size), 0o644)
		at := time.Now().Add(-age)
		os.Chtimes(p, at, at)
		return p
	}
	oldest := mk("aa11-m.jpg", 4000, 3*time.Hour)
	middle := mk("bb22-p.mp4", 4000, 2*time.Hour)
	newest := mk("cc33-h.mp4", 4000, time.Hour)
	thumb := mk("dd44-s.jpg", 4000, 5*time.Hour)
	l.opt.MaxCache = 20000
	l.spill(context.Background())
	if !exists(oldest) {
		t.Fatal("moved below the cap")
	}
	l.opt.MaxCache = 12000 // 16000 now: down to 10800, two files go
	l.spill(context.Background())
	if exists(oldest) || exists(middle) || !exists(newest) || !exists(thumb) {
		t.Errorf("oldest %v middle %v newest %v thumb %v", exists(oldest), exists(middle), exists(newest), exists(thumb))
	}
	if !exists(filepath.Join(over, "thumbs", "aa", "aa11-m.jpg")) || !exists(filepath.Join(over, "thumbs", "bb", "bb22-p.mp4")) {
		t.Error("not in tier 2")
	}
	// with the grace time a request that has the old path still finds it
	spillGrace = 50 * time.Millisecond
	l.opt.MaxCache = 0
	l.spill(context.Background())
	if !exists(newest) {
		t.Error("removed at once")
	}
	time.Sleep(200 * time.Millisecond)
	if exists(newest) || !exists(filepath.Join(over, "thumbs", "cc", "cc33-h.mp4")) {
		t.Error("not removed after the grace time")
	}
	// overflow folder gone (NAS not mounted): nothing is written there
	gone := filepath.Join(t.TempDir(), "nas", "cache")
	l.opt.OverflowDir = gone
	mk("ee55-m.jpg", 4000, time.Hour)
	if l.spill(context.Background()) || exists(gone) {
		t.Error("spilled into a missing overflow folder")
	}
	// on the same disk as the cache (a mount point without its share)
	l.opt.OverflowDir = over
	overflowSameDiskOK = false
	if l.spill(context.Background()) || exists(filepath.Join(over, "thumbs", "ee")) {
		t.Error("spilled onto the same disk")
	}
	overflowSameDiskOK = true
	l.opt.OverflowDir = over
	// a closed time window stops a copy
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.spill(ctx) || !exists(filepath.Join(cache, "thumbs", "ee", "ee55-m.jpg")) {
		t.Error("spilled after the window closed")
	}
	if variantOf("abc-m.jpg") != "m" || variantOf("abc-h.mp4.123.tmp") != "" || variantOf("x.jpg") != "" {
		t.Error("variantOf")
	}
}
