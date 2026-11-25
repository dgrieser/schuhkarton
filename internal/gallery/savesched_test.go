package gallery

import (
	"testing"
	"time"
)

// Background changes are saved at most every saveEvery; changes made by
// hand come sooner (at most every saveGap); changes coming together are saved together.
func TestSaveSchedule(t *testing.T) {
	defer func(d, g, e time.Duration) { saveDelay, saveGap, saveEvery = d, g, e }(saveDelay, saveGap, saveEvery)
	saveDelay, saveGap, saveEvery = 30*time.Millisecond, 30*time.Millisecond, 400*time.Millisecond
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: t.TempDir()}}, CacheDir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.saveIndex()
	t0 := l.lastSave.Load()
	l.markDirty()
	time.Sleep(120 * time.Millisecond)
	if l.lastSave.Load() != t0 {
		t.Fatal("background change saved before saveEvery")
	}
	for range 5 {
		l.saveSoon()
	}
	time.Sleep(120 * time.Millisecond)
	t1 := l.lastSave.Load()
	if t1 == t0 || l.dirty.Load() {
		t.Fatal("change by hand not saved soon")
	}
	time.Sleep(450 * time.Millisecond)
	if l.lastSave.Load() != t1 {
		t.Error("saved again with nothing to save")
	}
	// a direct save takes over the pending one: the next background save
	// counts from it
	l.markDirty()
	l.saveIndex()
	t2 := l.lastSave.Load()
	l.markDirty()
	time.Sleep(200 * time.Millisecond)
	if l.lastSave.Load() != t2 {
		t.Error("pending save ran right after a direct one")
	}
}
