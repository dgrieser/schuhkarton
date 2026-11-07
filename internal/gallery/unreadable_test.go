package gallery

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dgrieser/schuhkarton/internal/nasretry"
)

// A file the share hangs on is left out of the background work and the
// viewer until it is tried again by hand.
func TestUnreadableLeftOut(t *testing.T) {
	l, root := testLib(t, "2021/a.jpg", "2021/b.jpg")
	defer func(h time.Duration) { nasretry.HangTime = h }(nasretry.HangTime)
	nasretry.HangTime = time.Millisecond
	ctx := context.Background()
	it := byRel(l, "2021/a.jpg")
	p := l.Path(it)
	err := nasretry.DoFile(ctx, "test", p, nasretry.Fixed(it.Size, it.MTime), func() error {
		time.Sleep(5 * time.Millisecond)
		return syscall.EIO
	}, nasretry.In(filepath.Dir(p), root))
	if !errors.Is(err, nasretry.ErrUnreadable) {
		t.Fatalf("hang not counted: %v", err)
	}
	if _, err := l.Ensure(ctx, it, VarSmall); !errors.Is(err, nasretry.ErrUnreadable) {
		t.Fatalf("thumbnail made from a file the share hangs on: %v", err)
	}
	thumbs, _, _ := l.mediaTodo()
	if len(thumbs) != 1 || thumbs[0].Rel != "2021/b.jpg" || l.cnt.thumbFail.Load() != 1 {
		t.Fatalf("background work: %d to do, %d failed", len(thumbs), l.cnt.thumbFail.Load())
	}
	// remote jobs: what is done stays done, what is to do counts as failed
	j := &remoteJob{}
	all := func(*Item) bool { return true }
	todo := l.jobTodo(j, jobTask{eligible: all, state: func(it *Item, _ time.Time) (bool, bool) { return it.Rel == "2021/a.jpg", false }})
	if len(todo) != 1 || j.done.Load() != 1 || j.fail.Load() != 0 {
		t.Fatalf("done before: %d to do, %d done, %d failed", len(todo), j.done.Load(), j.fail.Load())
	}
	todo = l.jobTodo(j, jobTask{eligible: all, state: func(*Item, time.Time) (bool, bool) { return false, false }})
	if len(todo) != 1 || todo[0].Rel != "2021/b.jpg" || j.done.Load() != 1 || j.fail.Load() != 1 {
		t.Fatalf("to do: %d to do, %d done, %d failed", len(todo), j.done.Load(), j.fail.Load())
	}
	if _, err := l.PrintImage(ctx, it); !errors.Is(err, nasretry.ErrUnreadable) {
		t.Fatalf("printed: %v", err)
	}
	st := l.Status().Unreadable
	if len(st) != 1 || st[0].Name != "B/2021/a.jpg" || st[0].ID != it.ID || st[0].Path != p || st[0].Aside {
		t.Fatalf("status: %+v", st)
	}
	l.RetryUnreadable(nil)
	if len(l.Status().Unreadable) != 0 || l.Unreadable(it) != nil {
		t.Fatal("tried again by hand: still left out")
	}
	if _, err := l.Ensure(ctx, it, VarSmall); err != nil {
		t.Fatalf("thumbnail after the retry: %v", err)
	}
}
