package gallery

// Import folder: photos and videos put there (subfolders too) move into the
// library on their own. A file moves once it is completely written (same
// size and time on two looks, a few seconds old). It first lands below the
// library folder with its subfolders, then the filing rules (if any) put it
// in place. Live Photo pairs and sidecars (.xmp, .aae) stay together.
// Other files stay in the import folder; emptied subfolders are removed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dgrieser/schuhkarton/internal/schedule"
)

// ImportOptions configure the import folder.
type ImportOptions struct {
	Dir    string        // empty: off
	Root   int           // library folder (index into Roots) to import into
	Every  time.Duration // how often to look (default 15s)
	Settle time.Duration // a file must be at least this old (default 5s)
}

type importJob struct {
	wake    chan struct{}
	done    atomic.Int64 // files imported since start
	waiting atomic.Int64 // files seen but still being written
	other   atomic.Int64 // files in the folder that are no photos or videos
	last    atomic.Int64 // unix time of the last import
	lastErr atomic.Value // string
	seen    map[string]fileSig
	stems   map[string]stemChoice // import folder+name -> name chosen, for the other half of a pair
	dirty   atomic.Bool           // stems changed since import.json was written
	base    string                // the import folder with links resolved (set each pass)
}

type stemChoice struct {
	Stem string    `json:"stem"`
	At   time.Time `json:"at"`
	ID   string    `json:"id"` // the item imported under that name (follows later moves)
}

// followImported keeps the record of an imported file current when it moves
// (filing rules, admins). The caller holds opMu.
func (j *importJob) followImported(oldID, newID string) {
	for k, c := range j.stems {
		if c.ID == oldID {
			c.ID = newID
			j.stems[k] = c
			j.dirty.Store(true)
		}
	}
}

// the names chosen for imported files live in import.json, so the other half
// of a Live Photo finds its partner also after a restart
func (l *Library) loadImportStems() {
	j := &l.importJob
	j.stems = map[string]stemChoice{}
	if b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "import.json")); err == nil {
		json.Unmarshal(b, &j.stems)
	}
}

func (l *Library) saveImportStems() {
	j := &l.importJob
	if !j.dirty.CompareAndSwap(true, false) {
		return
	}
	l.opMu.Lock()
	loaded := j.stems != nil
	l.opMu.Unlock()
	if !loaded { // never read: keep what is on disk
		return
	}
	l.opMu.Lock()
	b, err := json.Marshal(j.stems)
	l.opMu.Unlock()
	if err == nil {
		err = writeFileAtomic(filepath.Join(l.opt.CacheDir, "import.json"), b)
	}
	if err != nil {
		j.dirty.Store(true) // try again with the next pass
		l.logf("import.json: %v", err)
	}
}

// pairExts are the extensions the other half of a Live Photo may have:
// every photo or video format, in lower and upper case.
var photoPairExts, videoPairExts = func() ([]string, []string) {
	both := func(m map[string]bool) []string {
		var out []string
		for e := range m {
			out = append(out, e, strings.ToUpper(e))
		}
		sort.Strings(out)
		return out
	}
	return both(photoExt), both(videoExt)
}()

type fileSig struct {
	size  int64
	mtime int64
}

// ImportStatus is shown in the sidebar and on the filing page.
type ImportStatus struct {
	Dir     string `json:"dir"`
	Done    int64  `json:"done"`
	Waiting int64  `json:"waiting"`
	Other   int64  `json:"other"`
	Last    int64  `json:"last,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ImportInfo reports the import folder (nil when there is none).
func (l *Library) ImportInfo() *ImportStatus {
	if l.opt.Import.Dir == "" {
		return nil
	}
	j := &l.importJob
	st := &ImportStatus{Dir: l.opt.Import.Dir, Done: j.done.Load(), Waiting: j.waiting.Load(), Other: j.other.Load(), Last: j.last.Load()}
	st.Error, _ = j.lastErr.Load().(string)
	return st
}

// KickImport looks into the import folder right away.
func (l *Library) KickImport() {
	select {
	case l.importJob.wake <- struct{}{}:
	default:
	}
}

func (l *Library) importLoop(ctx context.Context) {
	defer l.bg.Done()
	o := l.opt.Import
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		l.importJob.lastErr.Store(err.Error())
		l.logf("import: %v", err)
	}
	l.opMu.Lock()
	l.loadImportStems()
	l.opMu.Unlock()
	defer l.saveImportStems()
	t := time.NewTicker(o.Every)
	defer t.Stop()
	for {
		// only inside the background window; a started import is not cut short
		if l.window(schedule.JobImport).Open(time.Now()) {
			if err := l.importOnce(ctx); err != nil && ctx.Err() == nil {
				l.importJob.lastErr.Store(err.Error())
				l.logf("import: %v", err)
			} else if err == nil {
				l.importJob.lastErr.Store("")
			}
			l.saveImportStems()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.importJob.wake:
		}
	}
}

// importOnce moves what is ready and indexes it.
func (l *Library) importOnce(ctx context.Context) error {
	o := l.opt.Import
	j := &l.importJob
	if l.Scanning() || !l.scannedOnce.Load() {
		return nil // the library is not read yet
	}
	// never import from a folder that is (through a link) part of the library
	// a link as import folder is followed; paths below it are taken from there
	j.base = realPath(o.Dir)
	base := j.base
	rd := base
	for _, r := range l.opt.Roots {
		rr := realPath(r.Path)
		if within(rd, rr) || within(rr, rd) {
			return errors.New("Import-Ordner liegt in der Bibliothek")
		}
	}
	now := time.Now()
	seen := map[string]fileSig{}
	var ready []string
	var waiting, other int64
	busy := map[string]bool{}  // folder+stem of files still being written: their pair waits too
	media := map[string]bool{} // folder+stem of photos and videos here now
	var sides []string         // complete sidecars
	key := func(p string) string {
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if KindOf(base) != 0 { // IMG_1.JPG.xmp belongs to IMG_1.JPG
			base = strings.TrimSuffix(base, filepath.Ext(base))
		}
		return filepath.Dir(p) + "\x00" + strings.ToLower(base)
	}
	var walkErr error
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// a file that just went away is fine; an unreadable folder is reported
			if !errors.Is(err, fs.ErrNotExist) && walkErr == nil {
				walkErr = err
			}
			if d != nil && d.IsDir() && p != base {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := d.Name()
		if p != base && (strings.HasPrefix(name, ".") || l.excluded(name) || partial(name)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			other++ // a link could point anywhere: never import it
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		sidecar := ext == ".xmp" || ext == ".aae"
		if KindOf(name) == 0 && !sidecar {
			other++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sig := fileSig{info.Size(), info.ModTime().UnixNano()}
		seen[p] = sig
		if sidecar {
			// a sidecar still being written holds back its photo
			if prev, ok := j.seen[p]; !ok || prev != sig || now.Sub(info.ModTime()) < o.Settle || sig.size == 0 {
				busy[key(p)] = true
			} else {
				sides = append(sides, p)
			}
			return nil
		}
		media[key(p)] = true
		// still being written: wait until it stays the same for one round
		if prev, ok := j.seen[p]; !ok || prev != sig || now.Sub(info.ModTime()) < o.Settle || sig.size == 0 {
			waiting++
			busy[key(p)] = true
			return nil
		}
		ready = append(ready, p)
		return nil
	})
	ready = slices.DeleteFunc(ready, func(p string) bool {
		if busy[key(p)] {
			waiting++
			return true
		}
		return false
	})
	j.seen = seen
	j.waiting.Store(waiting)
	j.other.Store(other)
	if err == nil {
		err = walkErr
	}
	// sidecars that came after their photo follow it into the library
	var orphans []string
	for _, p := range sides {
		if !media[key(p)] {
			orphans = append(orphans, p)
		}
	}
	if len(orphans) > 0 {
		moved, left, serr := l.importSidecars(orphans)
		j.other.Add(int64(left))
		if serr != nil && err == nil {
			err = serr
		}
		l.pruneImportDirs(moved)
	}
	if len(ready) == 0 {
		return err
	}
	ids, ierr := l.importFiles(ready)
	if ierr != nil {
		err = ierr
	}
	if len(ids) > 0 {
		j.done.Add(int64(len(ids)))
		j.last.Store(time.Now().Unix())
		j.lastErr.Store("")
		l.logf("import: %d files", len(ids))
		l.pruneImportDirs(ready)
		// index them: a scan that runs now may have missed them, and Scan
		// returns at once while one runs, so scan until they are in
		for try := 0; ; try++ {
			for l.Scanning() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}
			// no move between reading the folders and publishing the index:
			// a filing run moving the last import waits for this scan
			l.opMu.Lock()
			err := l.Scan(ctx)
			l.opMu.Unlock()
			if err != nil {
				return err
			}
			missing := 0
			for _, id := range ids {
				if l.Get(id) == nil {
					missing++
				}
			}
			if missing == 0 || try >= 5 {
				break
			}
		}
		// file them by the rules (in the background, like edits)
		if c := l.rules.Load(); c != nil && len(c.List) > 0 {
			l.queueRules(ids, "Import")
		}
	}
	return err
}

// realPath resolves links in p (p itself may not exist).
func realPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// partial reports names of files that are still being downloaded or copied.
func partial(name string) bool {
	n := strings.ToLower(name)
	for _, suf := range []string{".part", ".partial", ".crdownload", ".download", ".tmp", ".temp", ".!sync", ".filepart"} {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return strings.HasPrefix(n, "~$")
}

// importFiles moves files into the library folder, keeping their subfolders
// below the import folder. Files with the same name in one folder (a Live
// Photo pair) keep one name; taken names get " (2)". It returns the ids
// of the imported files.
func (l *Library) importFiles(paths []string) ([]string, error) {
	o := l.opt.Import
	type group struct {
		dir   string // relative to the import folder, slash separated
		stem  string
		files []string
		extra bool // a.jpg beside A.jpg: its own name, no pair record
	}
	groups := map[string]*group{}
	var keys []string
	for _, p := range paths {
		rel, err := filepath.Rel(l.importJob.base, p)
		if err != nil {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		dir = cleanImportDir(dir)
		stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		k := dir + "\x00" + strings.ToLower(stem)
		// files of one group share a name; two with the same extension (a.jpg,
		// A.jpg on a case-sensitive disk) cannot, so the second gets its own group
		for n := 1; ; n++ {
			g := groups[k]
			if g == nil {
				groups[k] = &group{dir: dir, stem: stem, extra: n > 1}
				keys = append(keys, k)
				break
			}
			clash := false
			for _, f := range g.files {
				clash = clash || strings.EqualFold(filepath.Ext(f), filepath.Ext(p))
			}
			if !clash {
				break
			}
			k = fmt.Sprintf("%s\x00%s\x01%d", dir, strings.ToLower(stem), n)
		}
		groups[k].files = append(groups[k].files, p)
	}
	sort.Strings(keys)
	l.opMu.Lock()
	defer l.opMu.Unlock()
	j := &l.importJob
	if j.stems == nil {
		j.stems = map[string]stemChoice{}
	}
	// a record lives as long as its file (a sidecar may come much later),
	// at most a year
	for k, c := range j.stems {
		if time.Since(c.At) > 365*24*time.Hour || (time.Since(c.At) > time.Hour && l.Get(c.ID) == nil) {
			delete(j.stems, k)
			j.dirty.Store(true)
		}
	}
	var out []string
	var firstErr error
	for _, k := range keys {
		g := groups[k]
		sort.Strings(g.files)
		var exts []string
		for _, f := range g.files {
			exts = append(exts, filepath.Ext(f))
			// the sidecars need their names free as well
			fstem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
			for _, n := range sidecars(f) {
				if base := strings.TrimSuffix(n, filepath.Ext(n)); base == fstem {
					exts = append(exts, filepath.Ext(n))
				} else {
					exts = append(exts, filepath.Ext(f)+filepath.Ext(n))
				}
			}
		}
		ri := o.Root // the library folder: the import one, or where an earlier half went
		allFree := func(ri int, dir, stem string) bool {
			for _, x := range exts {
				if exists(filepath.Join(l.opt.Roots[ri].Path, filepath.FromSlash(dir), stem+x)) {
					return false
				}
			}
			return true
		}
		// the other half of a Live Photo imported before joins it, also where
		// the rules have put it since
		k := g.dir + "\x00" + strings.ToLower(g.stem)
		destDir, stem := g.dir, ""
		if prev, ok := j.stems[k]; ok && !g.extra {
			if it := l.Get(prev.ID); it != nil {
				d := path.Dir(it.Rel)
				if d == "." {
					d = ""
				}
				if s := strings.TrimSuffix(path.Base(it.Rel), path.Ext(it.Rel)); allFree(it.Root, d, s) {
					ri, destDir, stem = it.Root, d, s
				}
			}
			if stem == "" && allFree(ri, g.dir, prev.Stem) {
				stem = prev.Stem
			}
		}
		root := l.opt.Roots[ri].Path
		target := filepath.Join(root, filepath.FromSlash(destDir))
		// a link on the way would put the files where the scan (which does not
		// follow links) finds them under another name or not at all: the
		// destination must resolve to itself; check before creating anything,
		// and again after
		linked := func() bool {
			return realPath(target) != filepath.Join(realPath(root), filepath.FromSlash(destDir))
		}
		if linked() {
			if firstErr == nil {
				firstErr = errors.New("Ziel führt über einen Link: " + path.Join(destDir))
			}
			continue
		}
		if err := os.MkdirAll(target, 0o755); err != nil || linked() {
			if firstErr == nil {
				firstErr = errors.New("Ziel führt über einen Link: " + path.Join(destDir))
				if err != nil {
					firstErr = err
				}
			}
			continue
		}
		// a Live Photo half that comes alone keeps the name free for the other
		// half, and the other half later takes the same name
		hasPhoto, hasVideo := false, false
		for _, f := range g.files {
			switch KindOf(f) {
			case Photo:
				hasPhoto = true
			case Video:
				hasVideo = true
			}
		}
		if stem == "" {
			want := exts
			if hasPhoto != hasVideo {
				other := videoPairExts
				if hasVideo {
					other = photoPairExts
				}
				want = append(append([]string(nil), exts...), other...)
			}
			stem = freeStem(target, g.stem, want)
		}
		lead := g.files[0] // the photo of a pair (it sorts first), else the file
		for _, f := range g.files {
			if KindOf(f) == Photo {
				lead = f
				break
			}
		}
		if !g.extra {
			j.stems[k] = stemChoice{stem, time.Now(), itemID(l.opt.Roots[ri].Name, path.Join(destDir, stem+filepath.Ext(lead)))}
			j.dirty.Store(true)
		}
		for _, f := range g.files {
			name := stem + filepath.Ext(f)
			sideErr, err := moveWithSidecarsErr(f, filepath.Join(target, name))
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				l.logf("import %s: %v", f, err)
				continue
			}
			if sideErr != nil { // the photo is in, a sidecar stayed behind: say so
				if firstErr == nil {
					firstErr = sideErr
				}
				l.logf("import %s: %v", f, sideErr)
			}
			out = append(out, itemID(l.opt.Roots[ri].Name, path.Join(destDir, name)))
		}
	}
	return out, firstErr
}

// importSidecars moves sidecars whose photo was imported before next to it;
// it returns the ones moved and how many stay (their photo is unknown).
func (l *Library) importSidecars(paths []string) ([]string, int, error) {
	l.opMu.Lock()
	defer l.opMu.Unlock()
	j := &l.importJob
	left := 0
	var moved []string
	var firstErr error
	for _, p := range paths {
		rel, err := filepath.Rel(l.importJob.base, p)
		if err != nil {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		dir = cleanImportDir(dir)
		n := filepath.Base(p)
		base := strings.TrimSuffix(n, filepath.Ext(n))
		mediaExt := ""
		if KindOf(base) != 0 { // IMG_1.JPG.xmp
			mediaExt = filepath.Ext(base)
			base = strings.TrimSuffix(base, mediaExt)
		}
		prev, ok := j.stems[dir+"\x00"+strings.ToLower(base)]
		var it *Item
		if ok {
			it = l.Get(prev.ID)
		}
		if it == nil {
			left++
			continue
		}
		stem := strings.TrimSuffix(path.Base(it.Rel), path.Ext(it.Rel))
		target := filepath.Dir(l.Path(it))
		if mediaExt != "" {
			// IMG_1.jpg.xmp beside IMG_1.JPG becomes IMG_1.JPG.xmp, so moves take it along
			if es, err := os.ReadDir(target); err == nil {
				for _, e := range es {
					if x := filepath.Ext(e.Name()); strings.EqualFold(x, mediaExt) && strings.TrimSuffix(e.Name(), x) == stem {
						mediaExt = x
						break
					}
				}
			}
		}
		to := stem + mediaExt + filepath.Ext(n)
		if err := moveFile(p, filepath.Join(target, to)); err != nil {
			left++
			if firstErr == nil {
				firstErr = fmt.Errorf("Begleitdatei %s: %w", n, err)
			}
			continue
		}
		moved = append(moved, p)
	}
	return moved, left, firstErr
}

// cleanImportDir keeps a subfolder name inside the library and visible:
// no "..", no hidden folders.
func cleanImportDir(dir string) string {
	var parts []string
	for _, p := range strings.Split(dir, "/") {
		p = strings.TrimLeft(p, ".")
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/")
}

// pruneImportDirs removes subfolders of the import folder that are empty
// now (the import folder itself stays).
func (l *Library) pruneImportDirs(paths []string) {
	dirs := map[string]bool{}
	for _, p := range paths {
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		base := l.importJob.base
		for d != base && strings.HasPrefix(d, base+string(filepath.Separator)) {
			if es, err := os.ReadDir(d); err != nil || len(es) > 0 || os.Remove(d) != nil {
				break
			}
			d = filepath.Dir(d)
		}
	}
}
