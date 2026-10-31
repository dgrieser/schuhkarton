package gallery

import (
	"context"
	"io/fs"
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
	var files []found
	for ri, root := range l.opt.Roots {
		err := filepath.WalkDir(root.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				l.logf("scan: %v", err)
				l.cnt.errors.Add(1)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			name := d.Name()
			if p != root.Path && l.excluded(name) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || kindOf(name) == 0 {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				l.cnt.errors.Add(1)
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
		it := &Item{ID: id, Root: f.root, RootName: rname, Rel: f.rel, Kind: kindOf(f.rel), Size: f.size, MTime: f.mtime}
		it.key = itemKey(rname, f.rel, f.size, f.mtime)
		items = append(items, it)
		todo = append(todo, it)
	}
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
	l.media.kick()
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
	et, err := startExifTool(l.opt.ExifTool)
	if err != nil {
		l.logf("exiftool unavailable (%v); using file names and folders only", err)
		for _, it := range todo {
			it.MetaDone = true
			l.cnt.meta.Add(1)
		}
		return nil
	}
	defer et.close()
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
		for j, it := range chunk {
			if m := res[paths[j]]; m != nil {
				applyMeta(it, m)
			}
			it.MetaDone = true
		}
		l.cnt.meta.Add(int64(len(chunk)))
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
	parts := strings.Split(dir, "/")
	it.Place, it.Category = "", ""
	folderYear, folderMonth := 0, 0
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
			if i == 0 {
				if !noPlace[strings.ToLower(tail)] {
					it.Place = tail
				}
			} else {
				it.Category = tail
			}
		}
		break
	}
	if folderYear == 0 {
		for _, p := range parts {
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
