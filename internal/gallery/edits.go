package gallery

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Override is what an admin changed on one file. Overrides live in
// edits.json (keyed by root name + "/" + rel) and win over metadata and
// folder names, also after rescans. With Options.WriteFiles the same values
// are written into the file as well, so they travel with the photo.
type Override struct {
	Taken    *int64   `json:"taken,omitempty"` // wall clock stored as UTC, like Item.Taken
	Place    *string  `json:"place,omitempty"`
	Category *string  `json:"category,omitempty"`
	Desc     *string  `json:"desc,omitempty"`
	GPS      *GPSEdit `json:"gps,omitempty"`
	Rot      int      `json:"rot,omitempty"` // clockwise rotation not written into the file
	At       int64    `json:"at"`
	By       string   `json:"by,omitempty"`
}

// GPSEdit sets or removes a position.
type GPSEdit struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Clear bool    `json:"clear,omitempty"`
}

// Edit is one admin change for one or more items; nil fields stay as they are.
type Edit struct {
	Taken    *int64 // exact capture time (wall clock as UTC)
	Shift    int64  // or: move each capture time by this many seconds
	Place    *string
	Category *string
	Desc     *string
	GPS      *GPSEdit
	By       string // account name, for the record
	// Record keeps the change in the history with this description, together
	// with the values before (taken while the change is made).
	Record map[string]any
	// Alone changes only these items, not their Live Photo videos too (undo
	// puts each file back on its own).
	Alone bool
}

// EditResult reports what an admin operation did.
type EditResult struct {
	Changed  int               `json:"changed"`
	Written  int               `json:"written"` // files changed on disk
	Warnings []string          `json:"warnings,omitempty"`
	IDs      map[string]string `json:"ids,omitempty"`     // move: old id -> new id; restore: entry -> item id
	Entries  []string          `json:"entries,omitempty"` // delete: the new trash entries (for undo)
	Moved    int               `json:"moved,omitempty"`   // edit: files the rules moved right away
	Queued   int               `json:"queued,omitempty"`  // edit: files the rules move in the background
	Failed   int               `json:"failed,omitempty"`  // rules: files that could not be moved
}

func (r *EditResult) warn(format string, args ...any) {
	if len(r.Warnings) < 50 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
	}
}

// --- storage ------------------------------------------------------------------

func (l *Library) loadEdits() {
	l.edits = map[string]*Override{}
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "edits.json"))
	if err != nil {
		return
	}
	var f struct {
		Items map[string]*Override `json:"items"`
	}
	if json.Unmarshal(b, &f) == nil && f.Items != nil {
		l.edits = f.Items
	}
}

func (l *Library) saveEdits() error {
	l.editMu.Lock()
	b, err := json.MarshalIndent(map[string]any{"version": 1, "items": l.edits}, "", " ")
	l.editMu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "edits.json"), b)
}

func (l *Library) override(it *Item) *Override {
	l.editMu.Lock()
	defer l.editMu.Unlock()
	return l.edits[l.favKey(it)]
}

// renameEdits moves overrides to new keys ("" drops them).
func (l *Library) renameEdits(pairs map[string]string) {
	l.editMu.Lock()
	defer l.editMu.Unlock()
	for from, to := range pairs {
		if o, ok := l.edits[from]; ok {
			delete(l.edits, from)
			if to != "" {
				l.edits[to] = o
			}
		}
	}
}

// applyOverride puts an item's edits on top of what metadata and folders
// said. It is the last step of finishItem.
func (l *Library) applyOverride(it *Item) {
	var o *Override
	if l.edits != nil {
		o = l.override(it)
	}
	rot := 0
	if o != nil {
		if o.Taken != nil {
			it.Taken, it.TakenSrc = *o.Taken, "edit"
		}
		if o.Place != nil {
			it.Place = *o.Place
		}
		if o.Category != nil {
			it.Category = *o.Category
		}
		if o.Desc != nil {
			it.Desc = *o.Desc
		}
		if o.GPS != nil {
			if o.GPS.Clear {
				it.GPS, it.Lat, it.Lon, it.Alt = false, 0, 0, 0
			} else {
				it.GPS, it.Lat, it.Lon = true, o.GPS.Lat, o.GPS.Lon
			}
		}
		rot = o.Rot
	}
	// Rot is the rotation already reflected in W, H and Aspect
	if d := ((rot-it.Rot)%360 + 360) % 360; d == 90 || d == 270 {
		it.W, it.H = it.H, it.W
		if it.Aspect > 0 {
			it.Aspect = 1 / it.Aspect
		}
	}
	it.Rot = rot
}

// EditedFields lists what an admin changed on an item.
func (l *Library) EditedFields(it *Item) []string {
	o := l.override(it)
	if o == nil {
		return nil
	}
	var out []string
	add := func(ok bool, name string) {
		if ok {
			out = append(out, name)
		}
	}
	add(o.Taken != nil, "taken")
	add(o.Place != nil, "place")
	add(o.Category != nil, "category")
	add(o.Desc != nil, "desc")
	add(o.GPS != nil, "gps")
	add(o.Rot != 0, "rot")
	return out
}

// --- editing ------------------------------------------------------------------

// withLive adds the Live Photo videos of the given items (they share date,
// place and position with their photo).
func (l *Library) withLive(ids []string) (items []*Item, companion map[string]bool) {
	companion = map[string]bool{}
	seen := map[string]bool{}
	for _, id := range ids {
		it := l.Get(id)
		if it == nil || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		items = append(items, it)
		if it.LiveVideo != "" {
			if lv := l.Get(it.LiveVideo); lv != nil && !seen[lv.ID] {
				seen[lv.ID] = true
				companion[lv.ID] = true
				items = append(items, lv)
			}
		}
	}
	return items, companion
}

// EditItems changes date, place, category, text or position of items. The
// change is kept as an override and, with WriteFiles, written into the files.
func (l *Library) EditItems(ids []string, e Edit) (EditResult, error) {
	var res EditResult
	if e.Taken == nil && e.Shift == 0 && e.Place == nil && e.Category == nil && e.Desc == nil && e.GPS == nil {
		return res, errors.New("nichts zu ändern")
	}
	if e.GPS != nil && !e.GPS.Clear && (math.Abs(e.GPS.Lat) > 90 || math.Abs(e.GPS.Lon) > 180) {
		return res, errors.New("ungültige Position")
	}
	l.opMu.Lock()
	defer l.opMu.Unlock()
	items, companion := l.withLive(ids)
	if e.Alone {
		items, companion = nil, map[string]bool{}
		for _, id := range ids {
			if it := l.Get(id); it != nil {
				items = append(items, it)
			}
		}
	}
	if len(items) == 0 {
		return res, errors.New("keine Elemente")
	}
	var before []EditBefore
	var names []string
	if e.Record != nil {
		before = l.editBefores(items, companion, e)
		for _, it := range items {
			names = append(names, path.Base(it.Rel))
		}
	}
	trim := func(p *string) *string {
		if p == nil {
			return nil
		}
		v := strings.TrimSpace(*p)
		return &v
	}
	place, cat, desc := trim(e.Place), trim(e.Category), e.Desc
	now := time.Now().Unix()
	type job struct {
		it   *Item
		args []string
		tk   int64
	}
	var jobs []job
	l.editMu.Lock()
	for _, it := range items {
		key := l.favKey(it)
		o := l.edits[key]
		if o == nil {
			o = &Override{}
			l.edits[key] = o
		}
		var tk int64
		switch {
		case e.Taken != nil:
			tk = *e.Taken
		case e.Shift != 0:
			tk = it.Taken + e.Shift
		}
		if tk != 0 {
			o.Taken = &tk
		}
		if place != nil {
			o.Place = place
		}
		if cat != nil {
			o.Category = cat
		}
		if desc != nil && !companion[it.ID] {
			o.Desc = desc
		}
		if e.GPS != nil {
			g := *e.GPS
			o.GPS = &g
		}
		o.At, o.By = now, e.By
		res.Changed++
		var d *string
		if !companion[it.ID] {
			d = desc
		}
		jobs = append(jobs, job{it, tagArgs(it, tk, place, cat, d, e.GPS), tk})
	}
	l.editMu.Unlock()
	if err := l.saveEdits(); err != nil {
		return res, err
	}

	// write into the files, a few at a time
	if l.opt.WriteFiles {
		if l.opt.ExifTool == "" {
			res.warn("exiftool fehlt: Änderungen nur in Schuhkarton gespeichert")
		} else {
			var mu sync.Mutex
			var wg sync.WaitGroup
			ch := make(chan job)
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for j := range ch {
						err := l.writeTags(j.it, j.args)
						mu.Lock()
						if err != nil {
							res.warn("%s: %v", path.Base(j.it.Rel), err)
						} else {
							res.Written++
							l.afterWrite(j.it, 0)
							if j.tk != 0 {
								j.it.MetaTaken = j.tk
							}
							if place != nil {
								j.it.MetaPlace = *place
							}
							if cat != nil {
								j.it.MetaCategory = *cat
							}
						}
						mu.Unlock()
					}
				}()
			}
			for _, j := range jobs {
				if len(j.args) > 0 {
					ch <- j
				}
			}
			close(ch)
			wg.Wait()
		}
	}
	l.refresh(items)
	l.editGen.Add(1)
	if e.Record != nil && res.Changed > 0 {
		var changed []string
		for _, it := range items {
			changed = append(changed, it.ID)
		}
		// before the filing below: a move then takes the change along
		l.Record(Change{By: e.By, Kind: ChEdit, Items: changed, Names: names, Info: e.Record}, before)
	}
	// filing follows what the folders are made of: date, place, category
	if e.Taken != nil || e.Shift != 0 || place != nil || cat != nil {
		var ids2 []string
		for _, it := range items {
			ids2 = append(ids2, it.ID)
		}
		l.fileByRules(ids2, e.By, &res)
	}
	return res, nil
}

// refresh recomputes derived fields of changed items and republishes the
// index (new slice: the search index and the UI notice the change).
func (l *Library) refresh(changed []*Item) {
	l.mu.Lock()
	for _, it := range changed {
		l.finishItem(it)
	}
	all := append([]*Item(nil), l.items...)
	l.mu.Unlock()
	pairLive(all)
	l.setItems(all)
	l.saveIndex()
}

// --- exiftool -------------------------------------------------------------------

func exifTime(t int64) string { return time.Unix(t, 0).UTC().Format("2006:01:02 15:04:05") }

// tagArgs returns the exiftool arguments for an edit of one file.
func tagArgs(it *Item, taken int64, place, cat, desc *string, gps *GPSEdit) []string {
	var a []string
	video := it.Kind == Video
	if taken != 0 {
		v := exifTime(taken)
		if video {
			a = append(a, "-QuickTime:CreateDate="+v, "-QuickTime:MediaCreateDate="+v, "-QuickTime:TrackCreateDate="+v, "-Keys:CreationDate="+v)
		} else {
			a = append(a, "-DateTimeOriginal="+v, "-CreateDate="+v)
		}
	}
	if place != nil {
		a = append(a, "-XMP-photoshop:City="+*place)
	}
	if cat != nil {
		a = append(a, "-XMP-photoshop:Category="+*cat)
	}
	if desc != nil {
		a = append(a, "-XMP-dc:Description="+*desc)
		if !video {
			a = append(a, "-EXIF:ImageDescription="+*desc)
		}
	}
	if gps != nil {
		switch {
		case gps.Clear && video:
			a = append(a, "-Keys:GPSCoordinates=", "-UserData:GPSCoordinates=", "-ItemList:GPSCoordinates=")
		case gps.Clear:
			a = append(a, "-GPS:all=", "-XMP-exif:GPSLatitude=", "-XMP-exif:GPSLongitude=")
		case video:
			v := fmt.Sprintf("%.6f, %.6f", gps.Lat, gps.Lon)
			a = append(a, "-Keys:GPSCoordinates="+v, "-UserData:GPSCoordinates="+v)
		default:
			ns, ew := "N", "E"
			if gps.Lat < 0 {
				ns = "S"
			}
			if gps.Lon < 0 {
				ew = "W"
			}
			a = append(a, fmt.Sprintf("-GPSLatitude=%.7f", math.Abs(gps.Lat)), "-GPSLatitudeRef="+ns,
				fmt.Sprintf("-GPSLongitude=%.7f", math.Abs(gps.Lon)), "-GPSLongitudeRef="+ew)
		}
	}
	return a
}

// writeTags runs exiftool on one file.
func (l *Library) writeTags(it *Item, args []string) error {
	full := []string{"-m", "-overwrite_original", "-charset", "filename=utf8"}
	if it.Kind == Video {
		full = append(full, "-api", "QuickTimeUTC=1")
	}
	full = append(full, args...)
	return l.withRealName(it, l.Path(it), func(p string) error {
		return writeExif(p, func() error { return exifToolWrite(l.opt.ExifTool, append(full, p)) })
	})
}

// exifToolWrite runs exiftool once and returns its error message.
func exifToolWrite(bin string, full []string) error {
	out, err := exifToolCmd(bin, full...).CombinedOutput()
	if strings.Contains(string(out), "1 image files updated") || strings.Contains(string(out), "1 image files unchanged") {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && !ee.Exited() { // killed
		return fmt.Errorf("%w (%v)", errExifToolGone, err)
	}
	msg := firstLine(out)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Error") {
			msg = strings.TrimSpace(line)
			break
		}
	}
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if msg == "" {
		msg = "nicht geschrieben"
	}
	return errors.New(msg)
}

// afterWrite takes the new size and time of a written file and moves the
// cached derivatives to the new cache key (rot turns the images as well).
func (l *Library) afterWrite(it *Item, rot int) {
	st, err := os.Stat(l.Path(it))
	if err != nil {
		return
	}
	l.mu.Lock()
	old := it.key
	it.Size, it.MTime = st.Size(), st.ModTime().Unix()
	l.mu.Unlock()
	l.rekey(it, old, rot)
}

// --- cache keys -------------------------------------------------------------------

func (l *Library) cacheFile(key, variant string) string {
	ext := ".jpg"
	if variant == VarPreview || variant == VarH264 {
		ext = ".mp4"
	}
	return filepath.Join(l.opt.CacheDir, "thumbs", key[:2], key+"-"+variant+ext)
}

func (l *Library) faceFile(key string, idx int) string {
	return filepath.Join(l.opt.CacheDir, "faces", key[:2], fmt.Sprintf("%s_%d.jpg", key, idx))
}

// rekey gives an item the cache key that matches its file, path and
// rotation, and moves the cached thumbnails, previews and face crops along.
// rot > 0 turns the cached images clockwise on the way.
func (l *Library) rekey(it *Item, oldKey string, rot int) {
	newKey := l.keyOf(it)
	if newKey == oldKey || oldKey == "" {
		l.mu.Lock()
		it.key = newKey
		l.mu.Unlock()
		return
	}
	move := func(from, to string, turn bool) {
		if !exists(from) {
			return
		}
		os.MkdirAll(filepath.Dir(to), 0o755)
		if turn {
			if err := rotateJPEGFile(from, to, rot); err == nil {
				os.Remove(from)
			} else {
				os.Remove(from) // regenerated on demand
			}
			return
		}
		if err := os.Rename(from, to); err != nil {
			os.Remove(from)
		}
	}
	l.tiers.mu.Lock() // not while the file moves to tier 2
	for _, v := range []string{VarSmall, VarLarge, VarPreview, VarH264} {
		move(l.cacheFile(oldKey, v), l.cacheFile(newKey, v), rot != 0 && (v == VarSmall || v == VarLarge))
		if from := l.overflowFile(oldKey, v); from != "" && exists(from) {
			move(from, l.overflowFile(newKey, v), rot != 0 && v == VarLarge)
			// moved over just now, waiting for its removal here (spillGrace)
			// under the old name: the copy here is not needed any more
			if exists(l.overflowFile(newKey, v)) {
				os.Remove(l.cacheFile(newKey, v))
			}
		}
	}
	l.tiers.mu.Unlock()
	for i := range len(it.Faces) {
		move(l.faceFile(oldKey, i), l.faceFile(newKey, i), rot != 0)
	}
	l.mu.Lock()
	it.key = newKey
	l.mu.Unlock()
	if rot == 0 {
		l.carryDupeSig(it, oldKey, newKey)
	}
}

// removeDerivatives deletes everything cached for a key.
func (l *Library) removeDerivatives(key string, faces int) {
	if len(key) < 2 {
		return
	}
	l.tiers.mu.Lock()
	for _, v := range []string{VarSmall, VarLarge, VarPreview, VarH264} {
		os.Remove(l.cacheFile(key, v))
		if p := l.overflowFile(key, v); p != "" {
			os.Remove(p)
		}
	}
	l.tiers.mu.Unlock()
	for i := range faces {
		os.Remove(l.faceFile(key, i))
	}
}

// --- people labels ------------------------------------------------------------------

// renameLabelItems moves face names to new item ids ("" drops them).
func (s *peopleStore) renameLabelItems(ids map[string]string) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	changed := false
	keys := make([]string, 0, len(s.labels))
	for k := range s.labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		i := strings.LastIndexByte(k, ':')
		if i < 0 {
			continue
		}
		to, ok := ids[k[:i]]
		if !ok {
			continue
		}
		v := s.labels[k]
		delete(s.labels, k)
		if to != "" {
			s.labels[to+k[i:]] = v
		}
		changed = true
	}
	// remembered hidden groups follow their faces
	for i := range s.hidden {
		faces := map[string]string{}
		for k, v := range s.hidden[i].Faces {
			j := strings.LastIndexByte(k, ':')
			if to, ok := ids[k[:max(j, 0)]]; j >= 0 && ok {
				if to != "" {
					faces[to+k[j:]] = v
				}
				changed = true
				continue
			}
			faces[k] = v
		}
		s.hidden[i].Faces = faces
	}
	s.mu.Unlock()
	if changed && s.path != "" {
		s.save()
	}
}
