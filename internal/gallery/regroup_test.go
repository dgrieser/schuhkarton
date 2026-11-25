package gallery

import (
	"testing"
	"time"
)

// with grouping in the background a name shows at once and the groups follow
func TestAsyncRegroup(t *testing.T) {
	l, _ := testLib(t, "a.jpg")
	a := byRel(l, "a.jpg")
	l.mu.Lock()
	a.Faces = []Face{{X: .1, Y: .1, W: .2, H: .2, Score: .9, Emb: normalize([]float32{1, 0})}}
	l.mu.Unlock()
	l.opt.AsyncRegroup = true
	defer func(d time.Duration) { regroupSettle = d }(regroupSettle)
	regroupSettle = 10 * time.Millisecond
	if _, err := l.LabelFace(a.ID, 0, "Anna"); err != nil {
		t.Fatal(err)
	}
	if _, name := l.FacePerson(a.ID, 0); name != "Anna" {
		t.Errorf("not at once: %q", name)
	}
	for range 100 {
		if p := l.Person(l.PersonOf(a.ID, 0)); p != nil && p.Name == "Anna" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("groups did not follow")
}
