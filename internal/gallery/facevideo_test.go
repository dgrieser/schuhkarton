package gallery

import (
	"context"
	"image"
	"image/color"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestVideoFaceTimes(t *testing.T) {
	if got := videoFaceTimes(0); len(got) != 1 || got[0] != 0 {
		t.Errorf("unknown length: %v", got)
	}
	if got := videoFaceTimes(3); len(got) != 2 || got[0] != .75 || got[1] != 2.25 {
		t.Errorf("3 s: %v", got)
	}
	got := videoFaceTimes(120)
	if len(got) != videoFaceFrames || got[0] <= 0 || got[len(got)-1] >= 120 {
		t.Errorf("120 s: %v", got)
	}
}

func TestPickVideoFaces(t *testing.T) {
	anna, bob := []float32{1, 0, 0}, []float32{0, 1, 0}
	vf := func(frame int, q float64, emb []float32, x float32) videoFace {
		return videoFace{Face: Face{X: x, Emb: emb, T: float32(frame)}, frame: frame, q: q}
	}
	// Anna in three frames, Bob in two: one face each, the best look
	got := pickVideoFaces([]videoFace{
		vf(0, .3, anna, .1), vf(1, .9, anna, .2), vf(2, .5, anna, .3),
		vf(0, .4, bob, .6), vf(2, .7, bob, .7),
	}, .48)
	if len(got) != 2 || got[0].T != 1 || got[1].T != 2 || got[1].X != .7 {
		t.Errorf("best looks: %+v", got)
	}
	// twins: two faces in one frame are two people, however alike
	got = pickVideoFaces([]videoFace{vf(0, .5, anna, .1), vf(0, .4, anna, .6)}, .48)
	if len(got) != 2 {
		t.Errorf("one frame, two faces: %+v", got)
	}
	// without embeddings: the faces of the best frame
	got = pickVideoFaces([]videoFace{vf(0, .9, nil, .1), vf(1, .5, nil, .1), vf(1, .5, nil, .5)}, .48)
	if len(got) != 2 || got[0].frame != 1 || got[1].frame != 1 {
		t.Errorf("best frame: %+v", got)
	}
}

func TestSharpness(t *testing.T) {
	flat := image.NewGray(image.Rect(0, 0, 32, 32))
	check := image.NewGray(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			flat.SetGray(x, y, color.Gray{128})
			if (x/2+y/2)%2 == 0 {
				check.SetGray(x, y, color.Gray{255})
			}
		}
	}
	if s, c := sharpness(flat), sharpness(check); s != 0 || c < 1000 {
		t.Errorf("flat %v, checkerboard %v", s, c)
	}
	if faceQuality(.9, 400, 1000) <= faceQuality(.9, 400, 5) || faceQuality(.9, 40, 1000) >= faceQuality(.9, 200, 1000) {
		t.Error("blur and size should count")
	}
}

// fakeFaces finds the same two people in every frame.
type fakeFaces struct{ calls atomic.Int32 }

func (f *fakeFaces) probe() FaceInfo {
	return FaceInfo{Mode: "fake", Detection: true, Recognition: true}
}
func (f *fakeFaces) detect(ctx context.Context, l *Library, it *Item, prefix string) ([]Face, error) {
	return nil, nil
}
func (f *fakeFaces) detectData(ctx context.Context, l *Library, data []byte) ([]Face, error) {
	f.calls.Add(1)
	return []Face{
		{X: .1, Y: .2, W: .2, H: .3, Score: .95, Emb: []float32{1, 0}},
		{X: .6, Y: .2, W: .2, H: .3, Score: .9, Emb: []float32{0, 1}},
	}, nil
}

// TestVideoFaces: a video is searched in several frames; each person counts
// once, with the second it was seen at and a crop.
func TestVideoFaces(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	et := FindExifTool("")
	if err != nil || et == "" {
		t.Skip("ffmpeg or exiftool not installed")
	}
	root := t.TempDir()
	out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=s=640x360:r=25:d=6", "-c:v", "mpeg4",
		filepath.Join(root, "clip.mp4")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, FFmpeg: ff, ExifTool: et})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	fake := &fakeFaces{}
	l.faces = fake
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	v := byRel(l, "clip.mp4")
	if v.Duration < 5 {
		t.Fatalf("duration %v", v.Duration)
	}
	_, _, todo := l.mediaTodo()
	if len(todo) != 1 || todo[0] != v {
		t.Fatalf("face todo %v", todo)
	}
	if !l.scanFaces(ctx, v) {
		t.Fatal("scan stopped")
	}
	faces := l.ItemFaces(v)
	if n := int(fake.calls.Load()); n != len(videoFaceTimes(v.Duration)) || n < 2 {
		t.Errorf("%d frames searched", n)
	}
	if len(faces) != 2 || faces[0].T <= 0 || faces[0].T >= float32(v.Duration) {
		t.Fatalf("faces %+v", faces)
	}
	for i := range faces {
		if !exists(l.FacePath(v, i)) {
			t.Errorf("no crop for face %d", i)
		}
	}
	if _, _, todo := l.mediaTodo(); len(todo) != 0 {
		t.Errorf("searched again: %v", todo)
	}

	// found before recognition ran and named: its frame is searched again for
	// the embedding, it keeps its index, and the same person from another
	// frame is not added twice
	l.mu.Lock()
	v.Faces, v.FacesDone = []Face{{X: .1, Y: .2, W: .2, H: .3, Score: .9, T: 1}}, false
	l.mu.Unlock()
	if _, err := l.LabelFace(v.ID, 0, "Anna"); err != nil {
		t.Fatal(err)
	}
	if !l.scanFaces(ctx, v) {
		t.Fatal("scan stopped")
	}
	faces = l.ItemFaces(v)
	if len(faces) != 2 || faces[0].T != 1 || len(faces[0].Emb) == 0 || faces[1].X != .6 {
		t.Fatalf("after recognition: %+v", faces)
	}
	if p := l.Person(l.PersonOf(v.ID, 0)); p == nil || p.Name != "Anna" {
		t.Errorf("name lost: %+v", p)
	}
}
