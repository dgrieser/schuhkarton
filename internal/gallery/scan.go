package gallery

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type found struct {
	root  int
	rel   string
	size  int64
	mtime int64
}

// Scan walks all roots, reads metadata for new or changed files and starts
// the background jobs (thumbnails, previews, faces).
func (l *Library) Scan(ctx context.Context) error {
	if !l.scanning.CompareAndSwap(false, true) {
		return nil
	}
	defer l.scanning.Store(false)

	l.phase.Store("Ordner werden gelesen")
	l.cnt.scanned.Store(0)
	// A folder that cannot be read (a network share that is down, a mount
	// that failed) must not empty the library: its items stay as they were.
	// So do the items below subfolders that could not be read. The library
	// folders and the mount points below them (where the device changes)
	// are remembered while they hold files; one that holds no photos or
	// videos now counts as emptied on purpose only when it is still the same
	// directory. Otherwise it is a mount point with nothing mounted (which
	// may hold lost+found, .DS_Store and the like) and keeps its files.
	oldRels := map[int][]string{}
	oldSet := map[int]map[string]fileSig{} // rel -> size and time
	for _, it := range l.Items() {
		oldRels[it.Root] = append(oldRels[it.Root], it.Rel)
		if oldSet[it.Root] == nil {
			oldSet[it.Root] = map[string]fileSig{}
		}
		oldSet[it.Root][it.Rel] = fileSig{it.Size, it.MTime}
	}
	keepAll := make([]bool, len(l.opt.Roots))       // root not read: keep everything
	keepBelow := make([][]string, len(l.opt.Roots)) // subfolders to keep ("a/b/")
	var offline []string
	var files []found
	mounts := mountPoints()
	for ri, root := range l.opt.Roots {
		if fi, err := os.Stat(root.Path); err != nil || !fi.IsDir() {
			keepAll[ri] = true
			offline = append(offline, root.Name)
			l.logf("scan: %s nicht erreichbar, der bisherige Stand bleibt (%v)", root.Name, err)
			continue
		}
		before := len(files)
		devs := map[string]string{}                // directory -> device, to find mount points
		checks := map[string]bool{root.Path: true} // the root and mount points below it
		if id, ok := deviceOf(root.Path); ok {
			devs[root.Path] = deviceOnly(id)
		}
		for p := range l.knownDirs() { // mount points seen before, mounted or not now
			if p != root.Path && within(p, root.Path) {
				checks[p] = true
			}
		}
		realRoot := realPath(root.Path) // mountinfo has resolved paths
		for _, p := range mounts {      // bind mounts on the same disk too
			if p != realRoot && within(p, realRoot) {
				if rel, err := filepath.Rel(realRoot, p); err == nil {
					checks[filepath.Join(root.Path, rel)] = true
				}
			}
		}
		err := filepath.WalkDir(root.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				l.logf("scan: %v", err)
				l.cnt.errors.Add(1)
				// a file that went away is fine; a folder that went away while it
				// was being read (a share dropping out) keeps what it had
				if !errors.Is(err, fs.ErrNotExist) || d == nil || d.IsDir() { // d is nil for the folder itself
					rel, _ := filepath.Rel(root.Path, p)
					rel = filepath.ToSlash(rel)
					if rel == "." {
						keepAll[ri] = true
					} else {
						keepBelow[ri] = append(keepBelow[ri], rel+"/")
					}
					offline = append(offline, path.Join(root.Name, rel))
				}
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			name := d.Name()
			if p != root.Path && (l.excluded(name) || name == TrashDir) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p != root.Path {
					if id, ok := deviceOf(p); ok {
						devs[p] = deviceOnly(id)
						if pd, ok := devs[filepath.Dir(p)]; ok && pd != devs[p] {
							checks[p] = true // a mount point
						}
					}
				}
				return nil
			}
			if KindOf(name) == 0 {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				l.cnt.errors.Add(1)
				if !errors.Is(err, fs.ErrNotExist) { // listed but not readable now: keep it
					rel, _ := filepath.Rel(root.Path, p)
					keepBelow[ri] = append(keepBelow[ri], filepath.ToSlash(rel))
				}
				return nil
			}
			rel, _ := filepath.Rel(root.Path, p)
			files = append(files, found{ri, filepath.ToSlash(rel), info.Size(), info.ModTime().Unix()})
			l.cnt.scanned.Add(1)
			return nil
		})
		if err != nil {
			return err
		}
		for dir := range checks {
			rel, _ := filepath.Rel(root.Path, dir)
			rel = filepath.ToSlash(rel)
			below := func(r string) bool { return rel == "." || strings.HasPrefix(r, rel+"/") }
			// files below now, and how many of them are the very files indexed
			// before (same size and time)
			if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
				l.forgetDevice(dir) // the folder itself is gone: deleted, not unmounted
				continue
			}
			found := map[string]fileSig{} // files below now
			for _, f := range files[before:] {
				if below(f.rel) {
					found[f.rel] = fileSig{f.size, f.mtime}
				}
			}
			var missing []string // indexed before, not here now
			oldN := 0
			for _, r := range oldRels[ri] {
				if below(r) {
					oldN++
					if _, ok := found[r]; !ok {
						missing = append(missing, r)
					}
				}
			}
			rec, known := l.dirRecord(dir)
			switch {
			case oldN == 0, l.sameDevice(dir):
				// a new folder, or the very directory it was: what is there counts
				if len(found) > 0 || known {
					l.rememberDevice(dir)
				}
				continue
			case !known:
				// first look (an existing library before folders were
				// remembered): with files it is what it is; without, keep
				if len(found) > 0 {
					l.rememberDevice(dir)
					continue
				}
			default:
				// another identity: a remount when most of the files from
				// before are back. While offline, only the files kept then
				// count, not what was written into the empty mount point since.
				evidence := rec.Kept
				if len(evidence) == 0 {
					evidence = nil
					for _, r := range oldRels[ri] {
						if below(r) {
							evidence = append(evidence, r)
						}
					}
				}
				back := 0
				for _, r := range evidence {
					if s, ok := found[r]; ok && s == oldSet[ri][r] {
						back++
					}
				}
				if back*2 > len(evidence) {
					l.rememberDevice(dir)
					continue
				}
			}
			l.markOffline(dir, missing)
			// another directory without any file from before (a mount point
			// with nothing mounted, maybe with something written into it):
			// keep what it had, add what is new
			if rel == "." {
				keepAll[ri] = true
			} else {
				keepBelow[ri] = append(keepBelow[ri], rel+"/")
			}
			offline = append(offline, path.Join(root.Name, rel))
			l.logf("scan: %s ohne Fotos und nicht der bisherige Ordner, der bisherige Stand bleibt", path.Join(root.Name, rel))
		}
	}

	// Reuse unchanged items, create the rest
	old := map[string]*Item{}
	for _, it := range l.Items() {
		old[it.ID] = it
	}
	var items, todo []*Item
	for _, f := range files {
		rname := l.opt.Roots[f.root].Name
		id := itemID(rname, f.rel)
		if it := old[id]; it != nil && it.Size == f.size && it.MTime == f.mtime && it.MetaDone {
			it.Root = f.root
			items = append(items, it)
			continue
		}
		it := &Item{ID: id, Root: f.root, RootName: rname, Rel: f.rel, Kind: KindOf(f.rel), Size: f.size, MTime: f.mtime}
		it.key = itemKey(rname, f.rel, f.size, f.mtime)
		items = append(items, it)
		todo = append(todo, it)
	}
	// keep what a folder that was not (completely) read had
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		seen[it.ID] = true
	}
	kept := 0
	for _, it := range l.Items() {
		if it.Root >= len(keepAll) || seen[it.ID] {
			continue
		}
		k := keepAll[it.Root]
		for _, dir := range keepBelow[it.Root] {
			if strings.HasSuffix(dir, "/") { // a folder: what is below it
				k = k || strings.HasPrefix(it.Rel, dir) || it.Rel+"/" == dir
			} else { // a single file
				k = k || it.Rel == dir
			}
		}
		if k {
			items = append(items, it)
			kept++
		}
	}
	if kept > 0 {
		l.logf("scan: %d files kept from folders that could not be read", kept)
	}
	l.offline.Store(offline)
	l.logf("scan: %d files, %d need metadata", len(items), len(todo))

	l.cnt.meta.Store(0)
	l.cnt.metaTodo.Store(int64(len(todo)))
	if len(todo) > 0 {
		l.phase.Store("Metadaten werden gelesen")
		if err := l.readMeta(ctx, todo); err != nil {
			return err
		}
	}
	for _, it := range items {
		l.finishItem(it)
	}
	pairLive(items)
	l.setItems(items)
	l.saveIndex()
	l.recluster()
	l.phase.Store("bereit")
	l.scannedOnce.Store(true)
	l.media.kick()
	l.kickAI()
	l.ocrJob.kick()
	l.speechJob.kick()
	l.kickXMP()
	l.PurgeTrash()
	return nil
}

// Scanning reports whether a scan is running.
func (l *Library) Scanning() bool { return l.scanning.Load() }

func (l *Library) excluded(name string) bool {
	for _, pat := range l.opt.Exclude {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}

func (l *Library) readMeta(ctx context.Context, todo []*Item) error {
	et, err := startExifTool(l.opt.ExifTool, l.exifCfg)
	if err != nil {
		l.logf("exiftool unavailable (%v); using file names and folders only", err)
		for _, it := range todo {
			it.MetaDone = true
			l.cnt.meta.Add(1)
		}
		return nil
	}
	defer et.close()
	named := false // names of faces came back from the files
	defer func() {
		if named {
			l.recluster()
		}
	}()
	const batch = 64
	for i := 0; i < len(todo); i += batch {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		chunk := todo[i:min(i+batch, len(todo))]
		paths := make([]string, len(chunk))
		for j, it := range chunk {
			paths[j] = l.Path(it)
		}
		res, err := et.read(paths)
		if err != nil {
			l.logf("exiftool: %v", err)
			l.cnt.errors.Add(1)
		}
		// what Schuhkarton wrote into RAW files and the like lives beside them
		var sides []string
		for j, it := range chunk {
			it.MetaSig = ""
			if m := res[paths[j]]; m != nil {
				applyMeta(it, m)
				if _, side := xmpTarget(paths[j]); !side {
					l.adoptXMP(it, m)
				}
			}
			if p, side := xmpTarget(paths[j]); side && exists(p) {
				sides = append(sides, p)
			}
			it.MetaDone = true
		}
		if len(sides) > 0 {
			sres, err := et.read(sides)
			if err != nil {
				l.logf("exiftool: %v", err)
			}
			for j, it := range chunk {
				if p, side := xmpTarget(paths[j]); side {
					if m := sres[p]; m != nil {
						l.adoptXMP(it, m)
					}
				}
			}
		}
		l.cnt.meta.Add(int64(len(chunk)))
		for _, it := range chunk {
			named = l.applyMetaFaces(it) || named
		}
	}
	return nil
}

// --- derived fields ---------------------------------------------------------

var (
	reYear       = regexp.MustCompile(`^(19|20)\d\d$`)
	reMonthDir   = regexp.MustCompile(`^(\d\d) (\p{L}+)(?: (.+))?$`)
	reNameFull   = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d\d)[-_.]?(\d\d)[-_.]?(\d\d)[-_ T.]?(?:at )?(\d\d)[-_.:h]?(\d\d)[-_.:m]?(\d\d)`)
	reNameDate   = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d\d)[-_.]?(\d\d)[-_.]?(\d\d)(?:[^0-9]|$)`)
	noPlace      = map[string]bool{"kamera": true, "unbekannt": true, "unknown": true}
	monthsDE     = []string{"", "Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}
	weekdaysDE   = []string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}
	junkDescRe   = regexp.MustCompile(`(?i)^(olympus digital camera|sony dsc|default|image|picture|exif_jpeg_picture|\s*)$`)
	liveMaxDurat = 6.0
)

// finishItem fills album, place, category, date and aspect from what is known.
func (l *Library) finishItem(it *Item) {
	dir := path.Dir(it.Rel)
	if dir == "." {
		dir = ""
	}
	it.Album = dir
	// the root's own name counts too: a root ".../Bilder/2025" holds "11 November Ort"
	parts := []string{path.Base(filepath.ToSlash(l.opt.Roots[it.Root].Path))}
	if dir != "" {
		parts = append(parts, strings.Split(dir, "/")...)
	}
	it.Place, it.Category = "", ""
	folderYear, folderMonth := 0, 0
	if l.opt.IgnoreFolders {
		parts = nil // no heuristics from folder names
	}
	// <Jahr>/<MM Monat Ort>  or  <Sammlung>/<Jahr>/<MM Monat Kategorie>
	for i := 0; i+1 < len(parts); i++ {
		if !reYear.MatchString(parts[i]) {
			continue
		}
		m := reMonthDir.FindStringSubmatch(parts[i+1])
		if m == nil {
			continue
		}
		folderYear = atoi(parts[i])
		folderMonth = atoi(m[1])
		if folderMonth < 1 || folderMonth > 12 {
			folderMonth = 0
		}
		if tail := strings.TrimSpace(m[3]); tail != "" {
			if i <= 1 {
				if !noPlace[strings.ToLower(tail)] {
					it.Place = tail
				}
			} else {
				it.Category = tail
			}
		}
		break
	}
	if folderYear == 0 && len(parts) > 1 {
		for _, p := range parts[1:] {
			if reYear.MatchString(p) {
				folderYear = atoi(p)
				break
			}
		}
	}

	// Capture date: metadata, then file name; the folder wins when it disagrees
	// (the library folders were sorted with care, camera clocks were not).
	name := path.Base(it.Rel)
	nameT := dateFromName(name)
	t, src := it.MetaTaken, "exif"
	if t == 0 {
		t, src = nameT, "name"
	}
	if folderYear != 0 {
		agrees := func(ts int64) bool {
			if ts == 0 {
				return false
			}
			d := time.Unix(ts, 0).UTC()
			return d.Year() == folderYear && (folderMonth == 0 || int(d.Month()) == folderMonth)
		}
		if !agrees(t) {
			switch {
			case agrees(nameT):
				t, src = nameT, "name"
			default:
				mon := max(folderMonth, 1)
				day := 1
				if t != 0 {
					// keep day and time when only the year or month is off by a reset clock
					d := time.Unix(t, 0).UTC()
					if folderMonth != 0 && int(d.Month()) == folderMonth {
						day = d.Day()
					}
				}
				t, src = time.Date(folderYear, time.Month(mon), day, 12, 0, 0, 0, time.UTC).Unix(), "folder"
			}
		}
	}
	if t == 0 {
		mt := time.Unix(it.MTime, 0)
		_, off := mt.Zone()
		t, src = it.MTime+int64(off), "mtime"
	}
	it.Taken, it.TakenSrc = t, src

	if it.Aspect == 0 {
		w, h := it.W, it.H
		if w > 0 && h > 0 {
			it.Aspect = float64(w) / float64(h)
		} else if it.Kind == Video {
			it.Aspect = 16.0 / 9
		} else {
			it.Aspect = 4.0 / 3
		}
	}
	if junkDescRe.MatchString(it.Desc) {
		it.Desc = ""
	}
	// metadata fills what the folders do not say
	if it.Place == "" {
		it.Place = it.MetaPlace
	}
	if it.Category == "" {
		it.Category = it.MetaCategory
	}
	// edits by an admin win over everything
	l.applyOverride(it)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func dateFromName(name string) int64 {
	if m := reNameFull.FindStringSubmatch(name); m != nil {
		if t := mkTime(m[1], m[2], m[3], m[4], m[5], m[6]); t != 0 {
			return t
		}
	}
	if m := reNameDate.FindStringSubmatch(name); m != nil {
		return mkTime(m[1], m[2], m[3], "12", "00", "00")
	}
	return 0
}

func mkTime(y, mo, d, h, mi, s string) int64 {
	Y, M, D, H, Mi, S := atoi(y), atoi(mo), atoi(d), atoi(h), atoi(mi), atoi(s)
	if Y < 1980 || Y > 2100 || M < 1 || M > 12 || D < 1 || D > 31 || H > 23 || Mi > 59 || S > 60 {
		return 0
	}
	return time.Date(Y, time.Month(M), D, H, Mi, S, 0, time.UTC).Unix()
}

// pairLive links Live Photo videos to their photo: same folder, same name,
// short video.
func pairLive(items []*Item) {
	photos := map[string]*Item{}
	for _, it := range items {
		it.LiveVideo, it.LivePhoto = "", ""
		if it.Kind == Photo {
			photos[strings.ToLower(strings.TrimSuffix(it.Rel, path.Ext(it.Rel)))] = it
		}
	}
	for _, it := range items {
		if it.Kind != Video || (it.Duration > liveMaxDurat) {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(it.Rel, path.Ext(it.Rel)))
		if p := photos[stem]; p != nil && p.LiveVideo == "" {
			p.LiveVideo, it.LivePhoto = it.ID, p.ID
			it.Taken = p.Taken
		}
	}
}
