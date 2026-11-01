package gallery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A library folder on a share that is down must not empty the library.
func TestScanKeepsUnreachableFolder(t *testing.T) {
	l, root := testLib(t, "2019/a.jpg", "2020/b.jpg")
	ctx := context.Background()
	away := root + ".weg"
	if err := os.Rename(root, away); err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(l.Items()) != 2 || len(l.Status().Offline) != 1 {
		t.Fatalf("unreachable folder emptied the library: %v offline %v", rels(l), l.Status().Offline)
	}
	// an empty mount point (another directory than the one the files were
	// in) counts as unreachable too
	os.Mkdir(root, 0o755)
	os.Mkdir(filepath.Join(root, "lost+found"), 0o755) // what a mount point may hold
	real := deviceOf
	deviceOf = func(p string) (string, bool) { // the mount point is another directory
		d, ok := real(p)
		return d + "-mountpoint", ok
	}
	l.Scan(ctx)
	deviceOf = real
	if len(l.Items()) != 2 {
		t.Fatalf("empty mount point emptied the library: %v", rels(l))
	}
	// back: a normal scan, deletions count again
	os.RemoveAll(root)
	os.Rename(away, root)
	os.Remove(filepath.Join(root, "2020/b.jpg"))
	l.Scan(ctx)
	if len(l.Items()) != 1 || len(l.Status().Offline) != 0 {
		t.Fatalf("after the share is back: %v offline %v", rels(l), l.Status().Offline)
	}
	// a folder emptied on purpose (same device) empties the library
	os.RemoveAll(filepath.Join(root, "2019"))
	l.Scan(ctx)
	if len(l.Items()) != 0 {
		t.Fatalf("emptied folder kept its files: %v", rels(l))
	}
}

func TestScanKeepsUnreadableSubfolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	l, root := testLib(t, "2019/a.jpg", "2020/b.jpg")
	sub := filepath.Join(root, "2020")
	os.Chmod(sub, 0)
	t.Cleanup(func() { os.Chmod(sub, 0o755) })
	os.Remove(filepath.Join(root, "2019/a.jpg")) // deleted in a readable folder: goes
	l.Scan(context.Background())
	if byRel(l, "2020/b.jpg") == nil || byRel(l, "2019/a.jpg") != nil {
		t.Fatalf("unreadable folder: %v", rels(l))
	}
	if off := l.Status().Offline; len(off) != 1 || off[0] != "B/2020" {
		t.Fatalf("unreadable subfolder not reported: %v", off)
	}
}

// A share mounted below a library folder is protected the same way.
func TestScanKeepsNestedMount(t *testing.T) {
	l, root := testLib(t, "Mount/a.jpg", "b.jpg")
	mount := filepath.Join(root, "Mount")
	real := deviceOf
	t.Cleanup(func() { deviceOf = real })
	mounted := true
	deviceOf = func(p string) (string, bool) {
		if p == mount && mounted {
			return "99:1", true // another device: a mount point
		}
		return real(p)
	}
	ctx := context.Background()
	l.Scan(ctx) // learns the mount point
	// the share goes away: an empty directory of the parent's device stays
	mounted = false
	os.Remove(filepath.Join(mount, "a.jpg"))
	l.Scan(ctx)
	if byRel(l, "Mount/a.jpg") == nil || byRel(l, "b.jpg") == nil {
		t.Fatalf("unmounted share emptied: %v", rels(l))
	}
	if off := l.Status().Offline; len(off) != 1 || off[0] != "B/Mount" {
		t.Fatalf("offline: %v", off)
	}
	// something written into the empty mount point does not make it the share
	writeTestJPEG(t, filepath.Join(mount, "neu.jpg"), 40, 30)
	l.Scan(ctx)
	if byRel(l, "Mount/a.jpg") == nil || byRel(l, "Mount/neu.jpg") == nil {
		t.Fatalf("a photo in the mount point replaced the share: %v", rels(l))
	}
	// … also not in later scans, when more photos land there and are indexed
	for _, n := range []string{"neu2.jpg", "neu3.jpg"} {
		writeTestJPEG(t, filepath.Join(mount, n), 40, 30)
		l.Scan(ctx)
	}
	l.Scan(ctx)
	if byRel(l, "Mount/a.jpg") == nil {
		t.Fatalf("placeholder photos replaced the share: %v", rels(l))
	}
	os.Remove(filepath.Join(mount, "neu2.jpg"))
	os.Remove(filepath.Join(mount, "neu3.jpg"))
	os.Remove(filepath.Join(mount, "neu.jpg"))
	// the folder deleted for good: its files go
	os.RemoveAll(mount)
	l.Scan(ctx)
	if byRel(l, "Mount/a.jpg") != nil || len(l.Status().Offline) != 0 {
		t.Fatalf("deleted folder kept: %v %v", rels(l), l.Status().Offline)
	}
}

// An existing library without roots.json: the first scan takes what is there.
func TestScanFirstLookAfterUpgrade(t *testing.T) {
	l, root := testLib(t, "a.jpg", "b.jpg", "c.jpg")
	os.Remove(filepath.Join(l.opt.CacheDir, "roots.json"))
	l.rootDev.dev = nil
	os.Remove(filepath.Join(root, "a.jpg"))
	os.Remove(filepath.Join(root, "b.jpg"))
	l.Scan(context.Background())
	if len(l.Items()) != 1 || len(l.Status().Offline) != 0 {
		t.Fatalf("deleted files kept after an upgrade: %v %v", rels(l), l.Status().Offline)
	}
}
