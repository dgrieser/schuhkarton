package gallery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/schedule"
)

// Faces marked by hand: somebody draws a box on a photo, or on a paused
// video, and may name it. The box is cut out of the picture as the face
// crop and the face is kept at once. When a face backend runs, it looks at
// the box (with some room around it) for the embedding in the background
// (manualEmbLoop), so the face counts for recognition too; a busy or
// unreachable service is asked again later. Without an embedding a named
// face still belongs to its person.

// FaceBox is a box relative to the picture as shown (0..1).
type FaceBox struct{ X, Y, W, H float32 }

func (b FaceBox) valid() bool {
	return b.W > 0.005 && b.H > 0.005 && b.X >= -0.001 && b.Y >= -0.001 && b.X+b.W <= 1.001 && b.Y+b.H <= 1.001
}

// AddFace adds a face drawn by hand to an item (at second t of a video)
// and names it when name is set. It returns the index of the new face.
func (l *Library) AddFace(ctx context.Context, id string, box FaceBox, t float64, name string) (int, error) {
	it := l.Get(id)
	if it == nil {
		return 0, errors.New("unknown item")
	}
	if !box.valid() {
		return 0, errors.New("invalid box")
	}
	name = strings.TrimSpace(name)
	if name == labelHidden || name == labelNoFace || strings.HasPrefix(name, "!") {
		return 0, errors.New("invalid name")
	}
	frame, err := l.frameImage(ctx, it, t)
	if err != nil {
		return 0, err
	}
	px := boxPixels(frame.Bounds(), box.X, box.Y, box.W, box.H)
	if px.Dx() < 4 || px.Dy() < 4 {
		return 0, errors.New("box too small")
	}
	face := Face{X: box.X, Y: box.Y, W: box.W, H: box.H, Score: 1, Manual: true, T: float32(max(t, 0))}
	// the name suggestions looked at this box already: their answer is the embedding
	emb, known := l.takeSuggested(it, box, t)
	if known {
		face.Emb, face.NoEmb, face.EmbTry = emb, emb == nil, time.Now().Unix()
	}
	cropImg, _ := cropSquare(frame, px, 1.5, 200)
	crop, err := encodeJPEG(cropImg, 88)
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	if l.byID[id] != it { // replaced by a scan meanwhile: the box may not fit any more
		l.mu.Unlock()
		return 0, errors.New("the file changed meanwhile, please mark the face again")
	}
	idx := len(it.Faces)
	it.Faces = append(append([]Face(nil), it.Faces...), face)
	l.mu.Unlock()
	p := l.FacePath(it, idx)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		os.WriteFile(p, crop, 0o644)
	}
	l.saveSoon()
	if !known {
		l.freshEmb.Store(FaceRef{it.ID, idx}.key(), true)
		l.kickManualEmb() // the embedding comes in the background, for this face right away
	}
	if name != "" {
		if _, err := l.LabelFace(it.ID, idx, name); err != nil {
			return idx, err
		}
	} else {
		l.recluster()
		l.bump()
	}
	return idx, nil
}

// manualEmbRetry is how long a hand-marked face waits after the face
// service failed (busy, unreachable, the file could not be read).
const manualEmbRetry = 15 * time.Minute

func (l *Library) kickManualEmb() {
	select {
	case l.manualEmb <- struct{}{}:
	default:
	}
}

// manualEmbLoop gives hand-marked faces their embedding, right after they
// were marked and for those still without one (also from before a restart).
func (l *Library) manualEmbLoop(ctx context.Context) {
	defer l.bg.Done()
	cropsDone := false
	for {
		wait := l.manualEmbPass(ctx)
		if !cropsDone {
			var w time.Duration
			cropsDone, w = l.redoManualCrops(ctx)
			if w > 0 && (wait == 0 || w < wait) {
				wait = w
			}
		}
		var again <-chan time.Time
		if wait > 0 {
			again = time.After(wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-l.manualEmb:
		case <-again:
		}
	}
}

// manualCropsMark marks that the crops of the hand-marked faces were made
// again once: before, small faces were enlarged with a scaler that only
// shrinks, which garbled their crops.
const manualCropsMark = ".manual-crops-v2"

// redoManualCrops makes the crops of all hand-marked faces again, once,
// inside the background window of media work. It returns whether that is
// done, else when to try again.
func (l *Library) redoManualCrops(ctx context.Context) (bool, time.Duration) {
	mark := filepath.Join(l.opt.CacheDir, "faces", manualCropsMark)
	if _, err := os.Stat(mark); err == nil {
		return true, 0
	}
	if l.Scanning() || !l.scannedOnce.Load() {
		return false, time.Minute // a scan makes items anew and copies their crops: after it
	}
	scans := l.scans.Load()
	type job struct {
		it  *Item
		idx int
		f   Face
		key string // the cache key then: a rotation changes it (a centred box may stay the same)
	}
	var jobs []job
	l.mu.RLock()
	for _, it := range l.items {
		for i, f := range it.Faces {
			if f.Manual {
				jobs = append(jobs, job{it, i, f, it.key})
			}
		}
	}
	l.mu.RUnlock()
	if len(jobs) > 0 {
		win := l.window(schedule.JobMedia)
		now := time.Now()
		if !win.Open(now) {
			if next := win.Next(now); !next.IsZero() {
				return false, max(next.Sub(now), time.Second)
			}
			return false, manualEmbRetry
		}
		wctx, cancel := ctx, context.CancelFunc(func() {})
		if end := win.End(now); !end.IsZero() {
			wctx, cancel = context.WithDeadline(ctx, end)
		}
		defer cancel()
		redone, failed, replaced := 0, 0, 0
		for _, j := range jobs {
			key := FaceRef{j.it.ID, j.idx}.key()
			if _, ok := l.cropsRedone.Load(key); ok {
				continue // made again earlier in this run
			}
			if wctx.Err() != nil {
				break // window closed or shutdown: the checks below, then on later
			}
			frame, err := l.frameImage(wctx, j.it, float64(j.f.T))
			if err != nil {
				if wctx.Err() != nil {
					break // stopped (window closed, shutdown): not done
				}
				l.logf("faces %s (marked by hand): crop not made again: %v", j.it.Rel, err)
				failed++ // e.g. the share is away: again later
				continue
			}
			px := boxPixels(frame.Bounds(), j.f.X, j.f.Y, j.f.W, j.f.H)
			cropImg, _ := cropSquare(frame, px, 1.5, 200)
			crop, err := encodeJPEG(cropImg, 88)
			if err != nil {
				failed++
				continue
			}
			// checked and written under opMu: a rotation moves the crops and changes the key
			l.opMu.Lock()
			l.mu.RLock()
			cur := l.byID[j.it.ID]
			still := cur != nil && j.idx < len(cur.Faces) && cur.Faces[j.idx].Manual // the face is there
			same := cur == j.it && still && sameBox(cur.Faces[j.idx], j.f) && cur.key == j.key
			l.mu.RUnlock()
			var werr error
			if same {
				p := l.FacePath(j.it, j.idx)
				os.MkdirAll(filepath.Dir(p), 0o755)
				werr = os.WriteFile(p, crop, 0o644)
			}
			l.opMu.Unlock()
			switch {
			case still && !same:
				replaced++ // made anew by a scan, or turned meanwhile: again with what it is now
				continue
			case werr != nil:
				failed++
				continue
			case same:
				redone++
			}
			l.cropsRedone.Store(key, true) // done, or the face is gone (nothing to make)
		}
		if redone > 0 {
			l.logf("faces: %d crops of faces marked by hand made again", redone)
			l.bump()
		}
		// a scan ran meanwhile: it may have copied crops not made again yet to
		// items it made anew (same IDs), so all of them again after it
		if l.Scanning() || l.scans.Load() != scans {
			l.cropsRedone.Clear()
			return false, time.Minute
		}
		// a scan may have made an item anew meanwhile (with the old crops): again then
		l.mu.RLock()
		for _, j := range jobs {
			if cur := l.byID[j.it.ID]; cur != nil && cur != j.it {
				l.cropsRedone.Delete(FaceRef{j.it.ID, j.idx}.key())
				replaced++
			}
		}
		l.mu.RUnlock()
		switch {
		case ctx.Err() != nil:
			return false, 0
		case wctx.Err() != nil:
			return false, manualEmbRetry // the window closed: on in the next one
		case replaced > 0:
			return false, time.Minute
		case failed > 0:
			return false, time.Hour
		}
	}
	os.MkdirAll(filepath.Dir(mark), 0o755)
	os.WriteFile(mark, nil, 0o644)
	return true, 0
}

// wantsEmb tells whether a hand-marked face still waits for its embedding.
func (f Face) wantsEmb() bool { return f.Manual && f.Emb == nil && !f.NoEmb }

// manualEmbPass looks for the embeddings of the hand-marked faces that
// need one. A face just marked is looked at right away (somebody waits for
// it); retries and faces from before keep to the background window of
// media work. It returns when to look again (0: when a face is marked).
func (l *Library) manualEmbPass(ctx context.Context) time.Duration {
	if l.faces == nil {
		return 0
	}
	if !l.FaceStatus().Recognition {
		// no embeddings now (the service is away, or has no recognition model): look again later
		return manualEmbRetry
	}
	type job struct {
		it    *Item
		idx   int
		f     Face
		fresh bool
	}
	var jobs []job
	var wait time.Duration
	later := func(d time.Duration) {
		if d > 0 && (wait == 0 || d < wait) {
			wait = d
		}
	}
	now := time.Now()
	win := l.window(schedule.JobMedia)
	// until the window opens (no window within days: look again after the retry time)
	untilOpen := func(t time.Time) time.Duration {
		if next := win.Next(t); !next.IsZero() {
			return max(next.Sub(t), time.Second)
		}
		return manualEmbRetry
	}
	l.mu.RLock()
	for _, it := range l.items {
		for i, f := range it.Faces {
			if !f.wantsEmb() {
				continue
			}
			_, fresh := l.freshEmb.Load(FaceRef{it.ID, i}.key())
			if !fresh {
				if f.EmbTry != 0 {
					if left := manualEmbRetry - now.Sub(time.Unix(f.EmbTry, 0)); left > 0 {
						later(left)
						continue
					}
				}
				if !win.Open(now) {
					later(untilOpen(now))
					continue
				}
			}
			jobs = append(jobs, job{it, i, f, fresh})
		}
	}
	l.mu.RUnlock()
	// just marked first
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].fresh && !jobs[b].fresh })
	changed := false
	for _, j := range jobs {
		if ctx.Err() != nil {
			return 0
		}
		if !j.fresh && !win.Open(time.Now()) {
			later(untilOpen(time.Now())) // the window closed meanwhile
			break
		}
		l.freshEmb.Delete(FaceRef{j.it.ID, j.idx}.key()) // a retry keeps to the window
		// background work stops when its window closes, like the media generator
		jctx, cancel := ctx, context.CancelFunc(func() {})
		if !j.fresh {
			if end := win.End(time.Now()); !end.IsZero() {
				jctx, cancel = context.WithDeadline(ctx, end)
			}
		}
		emb, err := l.manualEmbedding(jctx, j.it, j.f)
		closed := jctx.Err() != nil && ctx.Err() == nil
		cancel()
		if ctx.Err() != nil {
			return 0
		}
		if closed { // not tried to the end: the next window takes it up
			later(untilOpen(time.Now()))
			break
		}
		if err != nil {
			l.logf("faces %s (marked by hand): %v, again in %s", j.it.Rel, err, manualEmbRetry)
		}
		l.mu.Lock()
		// the same face still (no new scan of the file, same box)
		if l.byID[j.it.ID] == j.it && j.idx < len(j.it.Faces) && sameBox(j.it.Faces[j.idx], j.f) && j.it.Faces[j.idx].wantsEmb() {
			faces := append([]Face(nil), j.it.Faces...)
			f := &faces[j.idx]
			f.EmbTry = time.Now().Unix()
			switch {
			case err != nil:
			case emb != nil:
				f.Emb = emb
			default:
				f.NoEmb = true
			}
			j.it.Faces = faces
			changed = true
		} else {
			later(time.Minute) // replaced meanwhile (a scan): look at it again soon
		}
		l.mu.Unlock()
		if err != nil {
			later(manualEmbRetry)
			if errors.Is(err, errTransient) || errors.Is(err, context.DeadlineExceeded) {
				break // the service is away: the others wait too
			}
		}
	}
	if changed {
		l.saveSoon()
		l.reclusterSoon()
	}
	return wait
}

// manualEmbedding asks the face service for the embedding of a hand-marked
// face: it looks at the box with room around it, and only a face found
// there counts. nil without an error: no face there.
func (l *Library) manualEmbedding(ctx context.Context, it *Item, f Face) ([]float32, error) {
	frame, err := l.frameImage(ctx, it, float64(f.T))
	if err != nil {
		return nil, err
	}
	px := boxPixels(frame.Bounds(), f.X, f.Y, f.W, f.H)
	if px.Dx() < 4 || px.Dy() < 4 {
		return nil, nil
	}
	regionImg, sq := cropSquare(frame, px, 2.2, 0)
	region, err := encodeJPEG(regionImg, 90)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	found, err := l.faces.detectData(dctx, l, region)
	if err != nil {
		return nil, err
	}
	side := float32(sq.Dx())
	drawn := Face{X: float32(px.Min.X-sq.Min.X) / side, Y: float32(px.Min.Y-sq.Min.Y) / side,
		W: float32(px.Dx()) / side, H: float32(px.Dy()) / side}
	if m := matchingFace(found, drawn); m != nil {
		return m.Emb, nil
	}
	// a face there, but without an embedding: the service cannot recognize
	// right now (no model), so this is asked again, not taken as "no face"
	for _, m := range found {
		if m.Emb == nil && boxIoU(m, drawn) > 0.25 {
			return nil, errors.New("Gesicht gefunden, aber ohne Merkmale (fehlt das Erkennungsmodell?)")
		}
	}
	return nil, nil
}

func sameBox(a, b Face) bool {
	return a.X == b.X && a.Y == b.Y && a.W == b.W && a.H == b.H && a.T == b.T
}

// frameImage is the picture faces are found in: the oriented photo (at
// most 1600 px) or the frame of a video at second t.
func (l *Library) frameImage(ctx context.Context, it *Item, t float64) (image.Image, error) {
	var data []byte
	var err error
	if it.Kind == Video {
		data, err = l.videoFrame(ctx, it, t, 1600)
	} else {
		data, _, err = l.faceImage(ctx, it)
	}
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// videoFrame is one frame of a video at second t as a JPEG, at most long
// pixels on the long side.
func (l *Library) videoFrame(ctx context.Context, it *Item, t float64, long int) ([]byte, error) {
	if err := l.unreadable(it); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(l.opt.CacheDir, "frame-*.jpg")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := l.runFFmpeg(ctx, "-ss", fmt.Sprintf("%.3f", max(t, 0)), "-i", l.Path(it), "-map", "0:v:0", "-frames:v", "1",
		"-vf", scaleLong(long), "-q:v", "3", tmp.Name()); err != nil {
		return nil, err
	}
	return os.ReadFile(tmp.Name())
}

// cropSquare cuts a square around r, k times its longer side, clamped to
// the picture; size > 0 scales it to size × size. It also returns the
// square in the picture.
func cropSquare(img image.Image, r image.Rectangle, k float64, size int) (image.Image, image.Rectangle) {
	b := img.Bounds()
	side := int(float64(max(r.Dx(), r.Dy())) * k)
	side = min(side, b.Dx(), b.Dy())
	cx, cy := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	x0 := min(max(cx-side/2, b.Min.X), b.Max.X-side)
	y0 := min(max(cy-side/2, b.Min.Y), b.Max.Y-side)
	sq := image.Rect(x0, y0, x0+side, y0+side)
	out := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			out.Set(x, y, img.At(sq.Min.X+x, sq.Min.Y+y))
		}
	}
	if size > 0 && side != size {
		return areaResize(out, size, size), sq
	}
	return out, sq
}

func encodeJPEG(img image.Image, q int) ([]byte, error) {
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q})
	return buf.Bytes(), err
}

// mergeFaces puts what detection found next to the faces that keep their
// index: those marked by hand and those with a label (labeled is the set of
// their indexes; names, "not this person", hidden and no face belong to the
// index, and so does the crop). A found face that is one of them is dropped
// (it may lend its embedding); the others take the places of the other faces
// found before, where they are, then in order, then follow at the end. A
// place no new face takes keeps its old face, so no index moves.
// One face: the same box in the same frame (a photo has one frame, a video
// one per moment) unless the embeddings tell two people apart; in another
// frame of a video the same person (similarity of at least same).
// place[i] is the index found face i got, -1 when it was dropped (for
// moving its crop).
func mergeFaces(old, found []Face, labeled map[int]bool, same float32) (out []Face, place []int) {
	place = make([]int, len(found))
	keep := make([]bool, len(old))
	kept := false
	for i, f := range old {
		keep[i] = f.Manual || labeled[i]
		kept = kept || keep[i]
	}
	if !kept {
		for i := range place {
			place[i] = i
		}
		return found, place
	}
	out = append([]Face(nil), old...)
	var fresh []int // found faces that are none of the kept ones
	for i, f := range found {
		place[i] = -1
		covered := false
		for j := range out {
			m := &out[j]
			if !keep[j] {
				continue
			}
			switch {
			case sameSpot(f, *m) && !twoPeople(f, *m, same):
				covered = true
				if m.Emb == nil && f.Emb != nil {
					m.Emb = f.Emb // now it counts for recognition too
				}
			case !sameFrame(f, *m) && samePerson(f, *m, same):
				covered = true // already there from another frame of the video
			}
		}
		if !covered {
			fresh = append(fresh, i)
		}
	}
	// a found face takes the place of the old face where it is (names
	// belong to that index), the rest fill old places nobody took, in order,
	// then follow at the end
	taken := make([]bool, len(out))
	var rest []int
	for _, j := range fresh {
		best, bestIoU := -1, float32(0.4)
		for i := range out {
			if !keep[i] && !taken[i] && sameFrame(found[j], old[i]) {
				if v := boxIoU(found[j], old[i]); v > bestIoU {
					best, bestIoU = i, v
				}
			}
		}
		if best < 0 {
			rest = append(rest, j)
			continue
		}
		out[best], place[j], taken[best] = found[j], best, true
	}
	for i := range out {
		if !keep[i] && !taken[i] && len(rest) > 0 {
			out[i], place[rest[0]], taken[i] = found[rest[0]], i, true
			rest = rest[1:]
		}
	}
	for _, j := range rest {
		place[j] = len(out)
		out = append(out, found[j])
	}
	return out, place
}

// sameFrame tells whether two faces were seen in the same picture (always
// in a photo; in a video at the same moment, within a frame at 25 fps).
func sameFrame(a, b Face) bool { return math.Abs(float64(a.T-b.T)) < 0.04 }

// sameSpot tells whether two boxes are the same face.
func sameSpot(a, b Face) bool { return sameFrame(a, b) && boxIoU(a, b) > 0.4 }

// twoPeople tells whether the embeddings of two faces differ too much for
// one person.
func twoPeople(a, b Face, same float32) bool {
	return len(a.Emb) > 0 && len(b.Emb) > 0 && !samePerson(a, b, same)
}

// samePerson tells whether two faces with embeddings look alike enough.
func samePerson(a, b Face, same float32) bool {
	return len(a.Emb) > 0 && len(b.Emb) > 0 && dot(normalize(a.Emb), normalize(b.Emb)) >= same
}

func boxIoU(a, b Face) float32 {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	in := (x1 - x0) * (y1 - y0)
	return in / (a.W*a.H + b.W*b.H - in)
}

// matchingFace is the found face (with an embedding) that overlaps the
// drawn box most, nil when none does enough: better no embedding than the
// neighbour's.
func matchingFace(faces []Face, drawn Face) *Face {
	var best *Face
	var bestIoU float32 = 0.25
	for i := range faces {
		if f := &faces[i]; f.Emb != nil {
			if v := boxIoU(*f, drawn); v > bestIoU {
				best, bestIoU = f, v
			}
		}
	}
	return best
}

// copyFaceCrops copies the crops of an item's faces from its old cache key
// to its new one (the faces are kept, see Scan).
func (l *Library) copyFaceCrops(from, to *Item) {
	for i := range from.Faces {
		b, err := os.ReadFile(l.FacePath(from, i))
		if err != nil {
			continue
		}
		p := l.FacePath(to, i)
		if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
			os.WriteFile(p, b, 0o644)
		}
	}
}

func hasManualFace(faces []Face) bool {
	for _, f := range faces {
		if f.Manual {
			return true
		}
	}
	return false
}

// needsEmbedding tells whether detection still has to run for recognition:
// no face it found has an embedding (faces marked by hand may have none for
// good, an old found face kept at its place may have none either).
func needsEmbedding(faces []Face) bool {
	auto := false
	for _, f := range faces {
		if !f.Manual {
			if f.Emb != nil {
				return false
			}
			auto = true
		}
	}
	return auto
}
