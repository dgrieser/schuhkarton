package gallery

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Two cache tiers (cache.max_size, cache.overflow_dir): everything is made
// in the cache folder (tier 1, a local disk). When it grows above the cap,
// the large files (lightbox previews, video previews, converted videos)
// move to the overflow folder (tier 2, e.g. on the NAS), oldest first, and
// are read from there; they do not come back. Thumbnails, face crops and
// the index always stay in tier 1: the grid needs them all the time.

// spillable tells which variants may move to tier 2.
func spillable(variant string) bool {
	return variant == VarLarge || variant == VarPreview || variant == VarH264
}

// variantOf returns the variant of a cache file name ("<key>-<v>.<ext>").
func variantOf(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if i := strings.LastIndexByte(base, '-'); i > 0 && i == len(base)-2 {
		return base[i+1:]
	}
	return ""
}

type cacheTiers struct {
	mu      sync.Mutex // one file at a time moves (rekey, spill, removal)
	wake    chan struct{}
	spilled int64 // files moved in this run, for the log
}

// overflowFile is where a variant of a key lives in tier 2 ("" without one).
func (l *Library) overflowFile(key, variant string) string {
	if l.opt.OverflowDir == "" || !spillable(variant) || len(key) < 2 {
		return ""
	}
	return filepath.Join(l.opt.OverflowDir, "thumbs", key[:2], filepath.Base(l.cacheFile(key, variant)))
}

// cached returns the path of a derivative in tier 1 or tier 2 ("" when it
// does not exist yet).
func (l *Library) cached(it *Item, variant string) string {
	if p := l.CachePath(it, variant); exists(p) {
		return p
	}
	if p := l.overflowFile(it.key, variant); p != "" && exists(p) {
		return p
	}
	return ""
}

// CachedPath is cached for the server.
func (l *Library) CachedPath(it *Item, variant string) string { return l.cached(it, variant) }

func (l *Library) hasCached(it *Item, variant string) bool { return l.cached(it, variant) != "" }

// kickSpill asks the spill job to look at the cache size.
func (l *Library) kickSpill() {
	if l.tiers.wake == nil {
		return
	}
	select {
	case l.tiers.wake <- struct{}{}:
	default:
	}
}

// spillLoop keeps tier 1 below the cap: at the start, then a while after
// new files came in (checking costs a walk over the cache folder).
func (l *Library) spillLoop(ctx context.Context) {
	defer l.bg.Done()
	tick := time.NewTicker(spillEvery)
	defer tick.Stop()
	for {
		wctx, cancel, ok := l.opt.Window.Wait(ctx)
		if !ok {
			return
		}
		done := l.spill(wctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if done { // look again only when something was made since
			select {
			case <-ctx.Done():
				return
			case <-l.tiers.wake:
			}
		}
	}
}

// spillEvery is the least time between two looks at the cache size.
var spillEvery = 5 * time.Minute

// spill moves the oldest large files to tier 2 until tier 1 is 10 % below
// the cap. It reports false when it had to stop early (the overflow folder
// is not there, the time window closed): the loop tries again.
func (l *Library) spill(ctx context.Context) bool {
	limit := l.opt.MaxCache
	if l.opt.OverflowDir == "" || limit < 0 {
		return true
	}
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var total int64
	var files []file
	filepath.WalkDir(l.opt.CacheDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		if filepath.Base(filepath.Dir(filepath.Dir(p))) == "thumbs" && spillable(variantOf(d.Name())) {
			files = append(files, file{p, info.Size(), info.ModTime()})
		}
		return nil
	})
	if total <= limit {
		return true
	}
	// the folder must be there already: when the NAS is not mounted, its
	// empty mount point must not fill up the local disk instead
	if st, err := os.Stat(l.opt.OverflowDir); err != nil || !st.IsDir() || l.sameDisk(l.opt.OverflowDir, l.opt.CacheDir) {
		l.logf("Cache: Auslagerungsordner %s nicht da oder auf derselben Platte wie der Cache (Netzlaufwerk nicht eingehängt?), %d MB über der Obergrenze",
			l.opt.OverflowDir, (total-limit)>>20)
		return false
	}
	target := limit - limit/10
	sort.Slice(files, func(a, b int) bool { return files[a].mod.Before(files[b].mod) })
	var moved, bytes int64
	done := true
	for _, f := range files {
		if total <= target {
			break
		}
		if ctx.Err() != nil {
			done = false
			break
		}
		dst := filepath.Join(l.opt.OverflowDir, "thumbs", filepath.Base(filepath.Dir(f.path)), filepath.Base(f.path))
		if err := l.moveToTier2(ctx, f.path, dst); err != nil {
			if ctx.Err() != nil { // the time window closed mid-copy
				done = false
				break
			}
			l.logf("Cache auslagern nach %s: %v", l.opt.OverflowDir, err)
			done = false // the overflow folder is not there (NAS offline): next time
			break
		}
		total -= f.size
		moved++
		bytes += f.size
	}
	if moved > 0 {
		l.logf("Cache: %d große Dateien (%d MB) nach %s ausgelagert, %d MB bleiben", moved, bytes>>20, l.opt.OverflowDir, total>>20)
	}
	return done
}

// moveToTier2 copies a file over (another file system: no rename) and
// removes it here a little later: a request that has just picked the path
// here can still open it.
func (l *Library) moveToTier2(ctx context.Context, src, dst string) error {
	l.tiers.mu.Lock()
	defer l.tiers.mu.Unlock()
	st, err := os.Stat(src)
	if err != nil { // renamed or removed meanwhile
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := copyFileAtomic(ctx, src, dst); err != nil {
		return err
	}
	remove := func() {
		l.tiers.mu.Lock()
		defer l.tiers.mu.Unlock()
		// only the copy that went over (a new key makes a new name)
		if now, err := os.Stat(src); err == nil && now.ModTime().Equal(st.ModTime()) && now.Size() == st.Size() && exists(dst) {
			os.Remove(src)
		}
	}
	if spillGrace <= 0 {
		return os.Remove(src)
	}
	time.AfterFunc(spillGrace, remove)
	return nil
}

// spillGrace is how long a moved file stays here for requests that already
// have its path.
var spillGrace = time.Minute

func copyFileAtomic(ctx context.Context, src, dst string) error {
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
	if _, err := io.Copy(out, ctxReader{ctx, in}); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if st, err := in.Stat(); err == nil {
		os.Chtimes(tmp, st.ModTime(), st.ModTime())
	}
	return os.Rename(tmp, dst)
}

// ctxReader stops a copy when ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// sameDisk tells whether two folders are on one file system: an overflow
// folder there frees nothing, and a mount point without its share mounted
// (or the folder inside an unmounted share) looks just like that.
func (l *Library) sameDisk(a, b string) bool {
	if overflowSameDiskOK {
		return false
	}
	da, ok1 := deviceOf(a)
	db, ok2 := deviceOf(b)
	return ok1 && ok2 && deviceOnly(da) == deviceOnly(db)
}

// overflowSameDiskOK lets tests use two folders on one disk.
var overflowSameDiskOK = false
