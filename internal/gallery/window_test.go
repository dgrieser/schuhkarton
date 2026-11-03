package gallery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dgrieser/schuhkarton/internal/schedule"
)

// TestWindowHoldsBackgroundWork: outside the time window no thumbnails are
// made in the background, while a viewer still gets one at once.
func TestWindowHoldsBackgroundWork(t *testing.T) {
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "a.jpg"), 40, 30)
	writeTestJPEG(t, filepath.Join(root, "b.jpg"), 40, 30)
	now := time.Now()
	m := now.Hour()*60 + now.Minute()
	w := schedule.Daily((m+120)%1440, (m+180)%1440)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, Window: w})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.Start(ctx)
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	a, b := byRel(l, "a.jpg"), byRel(l, "b.jpg")
	if _, err := os.Stat(l.CachePath(b, VarSmall)); err == nil {
		t.Error("thumbnail made outside the window")
	}
	if _, err := l.Ensure(ctx, a, VarSmall); err != nil {
		t.Errorf("thumbnail for a viewer: %v", err)
	}
	st := l.Status()
	if st.Window == "" || st.WindowNext != w.Next(time.Now()).Unix() {
		t.Errorf("status: %q %d", st.Window, st.WindowNext)
	}
}
