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
	"strings"
	"time"
)

// Faces marked by hand: somebody draws a box on a photo, or on a paused
// video, and may name it. The box is cut out of the picture as the face
// crop; when a face backend runs, it looks at the box (with some room
// around it) for the embedding, so the face counts for recognition too.
// Without an embedding a named face still belongs to its person.

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
	// the embedding: the backend looks at the box with room around it
	if l.faces != nil {
		regionImg, sq := cropSquare(frame, px, 2.2, 0)
		if region, err := encodeJPEG(regionImg, 90); err == nil {
			dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			if found, err := l.faces.detectData(dctx, l, region); err == nil {
				// the drawn box within the region: only a face found there counts
				side := float32(sq.Dx())
				drawn := Face{X: float32(px.Min.X-sq.Min.X) / side, Y: float32(px.Min.Y-sq.Min.Y) / side,
					W: float32(px.Dx()) / side, H: float32(px.Dy()) / side}
				if f := matchingFace(found, drawn); f != nil {
					face.Emb = f.Emb
				}
			} else {
				l.logf("faces %s (marked by hand): %v", it.Rel, err)
			}
			cancel()
		}
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
	l.markDirty()
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
