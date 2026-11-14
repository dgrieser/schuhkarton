package gallery

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"sort"
)

// Faces in videos: a few frames spread over the video go to the face
// backend. The same person seen in several frames counts once, with the
// frame where the face looks best (detector score, size, sharpness). Each
// face keeps the second it was seen at (Face.T), so the viewer can show it.

const (
	videoFaceFrames = 8  // frames looked at per video
	maxVideoFaces   = 12 // a crowd: the best looks only
)

// videoFaceTimes spreads the frames over a video: about one per 1.5 s in
// short clips, at most videoFaceFrames, never the very first or last frame
// (fades, black frames).
func videoFaceTimes(dur float64) []float64 {
	if dur <= 0 {
		return []float64{0}
	}
	n := min(videoFaceFrames, max(1, int(dur/1.5)))
	out := make([]float64, n)
	for i := range out {
		out[i] = dur * (float64(i) + 0.5) / float64(n)
	}
	return out
}

type videoFace struct {
	Face
	frame int     // which of the frames it was found in
	q     float64 // how good the face looks there
	crop  []byte
}

// detectVideo finds the faces of a video and writes their crops as
// prefix_<i>.jpg. A frame that cannot be read is skipped; the error is
// returned only when no frame could be searched.
// Faces the item has without an embedding (found before recognition ran,
// kept at their index for their name) get one: their frames are searched
// too, and what is found on their spot comes first in the list (without a
// crop: mergeFaces folds it into the old face).
func (l *Library) detectVideo(ctx context.Context, it *Item, prefix string, old []Face, labeled map[int]bool) ([]Face, error) {
	var extra []Face
	frames := map[float32][]Face{} // searched already
	for i, o := range old {
		if len(o.Emb) > 0 || !(o.Manual || labeled[i]) || !l.FaceStatus().Recognition {
			continue
		}
		found, ok := frames[o.T]
		if !ok {
			var err error
			if _, found, err = l.searchFrame(ctx, it, float64(o.T)); err != nil && (ctx.Err() != nil || isTransient(err)) {
				return nil, err
			}
			frames[o.T] = found
		}
		for _, f := range found {
			f.T = o.T
			if len(f.Emb) > 0 && sameSpot(f, o) {
				extra = append(extra, f)
				break
			}
		}
	}
	var all []videoFace
	var lastErr error
	searched := 0
	for fi, t := range videoFaceTimes(it.Duration) {
		img, found, err := l.searchFrame(ctx, it, t)
		if err != nil {
			if ctx.Err() != nil || isTransient(err) {
				return nil, err // the container is down: the whole video again later
			}
			lastErr = err
			continue
		}
		searched++
		for _, f := range found {
			r := boxPixels(img.Bounds(), f.X, f.Y, f.W, f.H)
			if r.Dx() < 4 || r.Dy() < 4 {
				continue
			}
			cropImg, _ := cropSquare(img, r, 1.5, 200)
			crop, err := encodeJPEG(cropImg, 88)
			if err != nil {
				continue
			}
			tight, _ := cropSquare(img, r, 1, 96)
			f.T = float32(t)
			all = append(all, videoFace{Face: f, frame: fi, q: faceQuality(f.Score, max(r.Dx(), r.Dy()), sharpness(tight)), crop: crop})
		}
	}
	if searched == 0 && lastErr != nil {
		return nil, lastErr
	}
	best := pickVideoFaces(all, l.people.threshNew)
	out := extra
	for _, f := range best {
		os.WriteFile(fmt.Sprintf("%s_%d.jpg", prefix, len(out)), f.crop, 0o644)
		out = append(out, f.Face)
	}
	return out, nil
}

// searchFrame finds the faces in the frame of a video at second t.
func (l *Library) searchFrame(ctx context.Context, it *Item, t float64) (image.Image, []Face, error) {
	release, err := decodeSlot(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	data, err := l.videoFrame(ctx, it, t, 1600)
	release()
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, err
	}
	found, err := l.faces.detectData(ctx, l, data, nil)
	if err != nil || len(found) == 0 {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, found, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	return img, found, nil
}

// pickVideoFaces keeps the best look of each person. Faces are taken best
// first; one close to a face already kept (the same person in another
// frame) is left out. Two faces in one frame are always two people. Without
// embeddings nobody can be told apart across frames: the faces of the best
// frame are kept.
func pickVideoFaces(all []videoFace, thresh float32) []videoFace {
	if len(all) == 0 {
		return nil
	}
	for _, f := range all {
		if len(f.Emb) == 0 {
			return bestFrame(all)
		}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].q > all[b].q })
	type person struct {
		emb    []float32
		frames map[int]bool
	}
	var people []person
	var out []videoFace
	for _, f := range all {
		e := normalize(f.Emb)
		same, bestSim := -1, thresh
		for i, p := range people {
			if p.frames[f.frame] {
				continue
			}
			if s := dot(e, p.emb); s >= bestSim {
				same, bestSim = i, s
			}
		}
		if same >= 0 {
			people[same].frames[f.frame] = true
			continue
		}
		people = append(people, person{e, map[int]bool{f.frame: true}})
		out = append(out, f)
	}
	if len(out) > maxVideoFaces {
		out = out[:maxVideoFaces]
	}
	return out
}

// bestFrame returns the faces of the frame whose faces look best together.
func bestFrame(all []videoFace) []videoFace {
	sum := map[int]float64{}
	for _, f := range all {
		sum[f.frame] += f.q
	}
	best, bestQ := -1, -1.0
	for fr, q := range sum {
		if q > bestQ || (q == bestQ && fr < best) {
			best, bestQ = fr, q
		}
	}
	var out []videoFace
	for _, f := range all {
		if f.frame == best {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].q > out[b].q })
	if len(out) > maxVideoFaces {
		out = out[:maxVideoFaces]
	}
	return out
}

// faceQuality rates how good a face looks: the detector's score, the size
// (up to 200 px, more does not help recognition) and the sharpness (a
// blurred face from motion counts little).
func faceQuality(score float32, side int, sharp float64) float64 {
	size := math.Min(float64(side), 200) / 200
	return float64(score) * size * (sharp / (sharp + 60))
}

// sharpness is the variance of the Laplacian of the grey picture: high
// for crisp edges, low for blur.
func sharpness(img image.Image) float64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return 0
	}
	g := make([]float64, w*h)
	for y := range h {
		for x := range w {
			r, gr, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			g[y*w+x] = (0.299*float64(r) + 0.587*float64(gr) + 0.114*float64(bl)) / 257
		}
	}
	var sum, sum2 float64
	n := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			v := 4*g[i] - g[i-1] - g[i+1] - g[i-w] - g[i+w]
			sum += v
			sum2 += v * v
			n++
		}
	}
	m := sum / float64(n)
	return sum2/float64(n) - m*m
}

// boxPixels turns a box relative to the picture (0..1) into pixels,
// clamped to the picture.
func boxPixels(b image.Rectangle, x, y, w, h float32) image.Rectangle {
	return image.Rect(
		b.Min.X+int(float32(b.Dx())*x), b.Min.Y+int(float32(b.Dy())*y),
		b.Min.X+int(math.Ceil(float64(float32(b.Dx())*(x+w)))), b.Min.Y+int(math.Ceil(float64(float32(b.Dy())*(y+h))))).Intersect(b)
}
