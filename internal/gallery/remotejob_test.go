package gallery

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func jobTestLib(t *testing.T, n int) *Library {
	root := t.TempDir()
	for i := range n {
		writeTestJPEG(t, filepath.Join(root, fmt.Sprintf("p%02d.jpg", i)), 8, 6)
	}
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestJobFirst(t *testing.T) {
	l := jobTestLib(t, 6)
	var order []string
	var mu sync.Mutex
	task := jobTask{
		workers:  1,
		eligible: func(*Item) bool { return true },
		state:    func(*Item, time.Time) (bool, bool) { return false, false },
		one: func(_ context.Context, it *Item) error {
			mu.Lock()
			order = append(order, it.Rel)
			mu.Unlock()
			return nil
		},
		save: func() error { return nil },
		first: func() func(*Item) bool {
			return func(it *Item) bool { return it.Rel == "p01.jpg" || it.Rel == "p04.jpg" }
		},
	}
	l.jobPass(context.Background(), &remoteJob{name: "test"}, task)
	if len(order) != 6 || !(order[0] == "p04.jpg" || order[0] == "p01.jpg") || !(order[1] == "p04.jpg" || order[1] == "p01.jpg") {
		t.Errorf("order %v", order)
	}
}
