package gallery

import (
	"context"
	"testing"
)

// listFaces finds what found holds, in every photo.
type listFaces struct{ found []Face }

func (f *listFaces) probe() FaceInfo {
	return FaceInfo{Mode: "fake", Detection: true, Recognition: true}
}
func (f *listFaces) detect(ctx context.Context, l *Library, it *Item, prefix string, extra bool) ([]Face, error) {
	return append([]Face(nil), f.found...), nil
}
func (f *listFaces) detectData(ctx context.Context, l *Library, data []byte, box *[4]float32) ([]Face, error) {
	return nil, nil
}

// TestRethinkFaces: approved faces (by hand, by a label, drawn by hand)
// stay; the others are replaced by what is found now, and an old face not
// found again is put aside as no face. Approved photos are left alone.
func TestRethinkFaces(t *testing.T) {
	l, _ := testLib(t, "a.jpg", "b.jpg", "c.jpg")
	fake := &listFaces{found: []Face{
		{X: .1, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{1, 0, 0}}, // named below
		{X: .4, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{0, 1, 0}}, // approved below
		{X: .7, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{0, 0, 1}}, // not found again
		{X: .1, Y: .6, W: .1, H: .1, Score: .9, Emb: []float32{1, 1, 0}}, // found again elsewhere
	}}
	l.faces = fake
	ctx := context.Background()
	for _, it := range l.Items() {
		l.scanFaces(ctx, it)
	}
	a, b, c := byRel(l, "a.jpg"), byRel(l, "b.jpg"), byRel(l, "c.jpg")
	if f, ok := l.FaceApproval(a); ok || len(f) != 4 || f[0] {
		t.Fatalf("approval at first: %v %v", f, ok)
	}
	if _, err := l.LabelFace(a.ID, 0, "Ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApproveFaces(a.ID, []int{1}, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApproveFaces(b.ID, nil, true, true); err != nil {
		t.Fatal(err)
	}
	if f, ok := l.FaceApproval(a); ok || !f[0] || !f[1] || f[2] || f[3] {
		t.Fatalf("approval of a: %v %v", f, ok)
	}
	if _, ok := l.FaceApproval(b); !ok {
		t.Fatal("b not approved")
	}
	if n, all := l.RethinkCount(); n != 2 || all != 2 {
		t.Fatalf("%d (%d) to look at again, want a and c", n, all)
	}

	// the search finds other things now
	fake.found = []Face{
		{X: .1, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{1, 0, 0}},
		{X: .4, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{0, 1, 0}},
		{X: .1, Y: .62, W: .1, H: .1, Score: .8, Emb: []float32{1, 1, 0}},
	}
	n, err := l.RethinkFaces(false)
	if err != nil || n != 2 {
		t.Fatalf("rethink %d %v", n, err)
	}
	if n, _ := l.RethinkCount(); n != 0 {
		t.Error("waiting photos counted again")
	}
	_, _, todo := l.mediaTodo()
	if len(todo) != 2 {
		t.Fatalf("todo %d, want 2", len(todo))
	}
	for _, it := range todo {
		if it == b {
			t.Fatal("approved photo looked at again")
		}
		l.scanFaces(ctx, it)
	}
	faces := l.ItemFaces(a)
	if len(faces) != 4 || a.FaceRedo {
		t.Fatalf("faces of a: %d %+v", len(faces), faces)
	}
	if faces[0].X != .1 || faces[1].X != .4 || !faces[1].OK || faces[2].X != .7 || faces[3].Y != .62 {
		t.Errorf("places: %+v", faces)
	}
	if !l.IsNoFace(a.ID, 2) || l.IsNoFace(a.ID, 3) || l.IsNoFace(a.ID, 1) {
		t.Error("the face not found again should be no face, only that one")
	}
	if _, name := l.FacePerson(a.ID, 0); name != "Ada" {
		t.Errorf("name of face 0: %q", name)
	}
	// c had nothing approved: the faces found now replace the old ones
	if fc := l.ItemFaces(c); len(fc) != 3 || fc[2].Y != .62 || l.IsNoFace(c.ID, 2) {
		t.Errorf("faces of c: %+v", fc)
	}
	// taking one face back takes back the photo as a whole
	if _, err := l.ApproveFaces(b.ID, []int{0}, false, false); err != nil {
		t.Fatal(err)
	}
	if f, ok := l.FaceApproval(b); ok || f[0] || !f[1] {
		t.Errorf("b after taking back: %v %v", f, ok)
	}
	if _, err := l.ApproveFaces(b.ID, []int{9}, false, true); err == nil {
		t.Error("unknown face approved")
	}
	// every face approved one by one: left out unless complete ones are asked for
	if _, err := l.ApproveFaces(c.ID, []int{0, 1, 2}, false, true); err != nil {
		t.Fatal(err)
	}
	if n, all := l.RethinkCount(); n != 2 || all != 3 {
		t.Errorf("count %d, with complete %d; want a and b, then c too", n, all)
	}
	if n, _ := l.RethinkFaces(false); n != 2 {
		t.Errorf("rethink %d", n)
	}
	if _, redo := l.FaceFlags(c); redo {
		t.Error("c waiting")
	}
	if n, _ := l.RethinkFaces(true); n != 1 {
		t.Errorf("rethink with complete %d", n)
	}
	if _, redo := l.FaceFlags(c); !redo {
		t.Error("c not waiting")
	}
}

// TestApprovePhotoWithoutFaces: a photo without faces can be approved.
func TestApprovePhotoWithoutFaces(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	l.faces = &listFaces{}
	a := byRel(l, "a.jpg")
	l.scanFaces(context.Background(), a)
	if _, ok := l.FaceApproval(a); ok {
		t.Fatal("approved without anybody")
	}
	if n, _ := l.RethinkCount(); n != 1 {
		t.Fatal("not to look at again")
	}
	if _, err := l.ApproveFaces(a.ID, nil, true, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.FaceApproval(a); !ok {
		t.Fatal("approval did not count")
	}
	if n, all := l.RethinkCount(); n != 0 || all != 0 {
		t.Fatal("approval did not count")
	}
	// a search that was on its way (a queued scan, new models) adds nothing
	l.faces = &listFaces{found: []Face{{X: .1, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{1, 0}}}}
	l.scanFaces(context.Background(), a)
	if n := len(l.ItemFaces(a)); n != 0 || len(a.Extra) != 0 {
		t.Errorf("approved photo got %d faces", n)
	}
}

// TestApprovalUndoAndQueued: undo gives back the approvals before; a photo
// approved as a whole while it waits to be looked at again stays as it is.
func TestApprovalUndoAndQueued(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	fake := &listFaces{found: []Face{
		{X: .1, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{1, 0}},
		{X: .5, Y: .1, W: .1, H: .1, Score: .9, Emb: []float32{0, 1}},
	}}
	l.faces = fake
	ctx := context.Background()
	a := byRel(l, "a.jpg")
	l.scanFaces(ctx, a)
	if _, err := l.ApproveFaces(a.ID, []int{0}, false, true); err != nil {
		t.Fatal(err)
	}
	prev, err := l.ApproveFaces(a.ID, nil, true, true)
	if err != nil || prev.Item || len(prev.Faces) != 1 || prev.Faces[0] != 0 {
		t.Fatalf("before: %+v %v", prev, err)
	}
	if err := l.SetApproval(a.ID, prev); err != nil {
		t.Fatal(err)
	}
	if f, ok := l.FaceApproval(a); ok || !f[0] || f[1] {
		t.Fatalf("after undo: %v %v", f, ok)
	}
	if n, _ := l.RethinkFaces(false); n != 1 {
		t.Fatalf("rethink %d", n)
	}
	// approved as a whole while waiting: nothing changes
	if _, err := l.ApproveFaces(a.ID, nil, true, true); err != nil {
		t.Fatal(err)
	}
	fake.found = nil
	_, _, todo := l.mediaTodo()
	if len(todo) != 0 {
		t.Fatalf("still waiting: %d", len(todo))
	}
	l.mu.Lock()
	a.FaceRedo = true // as if it was on the list already
	l.mu.Unlock()
	l.scanFaces(ctx, a)
	if len(l.ItemFaces(a)) != 2 || a.FaceRedo || l.IsNoFace(a.ID, 1) {
		t.Errorf("approved photo changed: %+v", l.ItemFaces(a))
	}
}
