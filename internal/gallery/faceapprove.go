package gallery

import (
	"errors"
	"fmt"
)

// Approval: a face somebody has looked at stays as it is when the faces of
// photos are found again (RethinkFaces). Faces drawn by hand and faces with
// a label (a name, "not this person", hidden, no face) count as approved
// without more; others are approved one by one (Face.OK). A photo is
// approved when all its faces are, or when it was approved as a whole
// (Item.FacesOK, also a photo without faces).

// faceKept tells whether a face keeps its index and box when the faces of
// its item are found again.
func faceKept(f Face, labeled bool) bool { return f.Manual || f.OK || labeled }

// hasApprovedFace tells whether one of the faces was approved by hand.
func hasApprovedFace(faces []Face) bool {
	for _, f := range faces {
		if f.OK {
			return true
		}
	}
	return false
}

// FaceApproval tells for each face of an item whether it is approved, and
// whether the item is.
func (l *Library) FaceApproval(it *Item) (faces []bool, item bool) {
	labeled := l.people.labeled(it.ID)
	l.mu.RLock()
	defer l.mu.RUnlock()
	return approval(it, labeled)
}

// FaceApprovals is FaceApproval for many items: the labels are looked at
// once, when it is called (for listing the whole library).
func (l *Library) FaceApprovals() func(it *Item) (faces []bool, item bool) {
	labeled := l.people.labeledAll()
	return func(it *Item) ([]bool, bool) {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return approval(it, labeled[it.ID])
	}
}

// FaceFlags tells whether an item was approved as a whole, and whether its
// faces wait to be looked for again.
func (l *Library) FaceFlags(it *Item) (whole, redo bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return it.FacesOK, it.FaceRedo
}

// approval is FaceApproval; the caller holds l.mu.
func approval(it *Item, labeled map[int]bool) (faces []bool, item bool) {
	faces = make([]bool, len(it.Faces))
	all := len(it.Faces) > 0
	for i, f := range it.Faces {
		faces[i] = faceKept(f, labeled[i])
		all = all && faces[i]
	}
	return faces, it.FacesOK || all
}

// Approval is what of an item is approved by hand: the item as a whole
// and the faces (indexes) approved one by one.
type Approval struct {
	Item  bool  `json:"item"`
	Faces []int `json:"faces"`
}

// ApproveFaces approves faces of an item (ok false takes that back): the
// faces at idx, or with all every face and the item itself. Taking back
// one face also takes back the item as a whole. Faces approved by a label
// or drawn by hand stay approved. An item approved as a whole is not
// looked at again (RethinkFaces). It returns what was approved before, for
// SetApproval.
func (l *Library) ApproveFaces(id string, idx []int, all, ok bool) (Approval, error) {
	it := l.Get(id)
	if it == nil {
		return Approval{}, fmt.Errorf("unknown item %q", id)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, i := range idx {
		if i < 0 || i >= len(it.Faces) {
			return Approval{}, fmt.Errorf("unknown face %d", i)
		}
	}
	prev := approvedLocked(it)
	it.Faces = append([]Face(nil), it.Faces...) // copy on write: grouping reads snapshots without the lock
	if all {
		it.FacesOK = ok
		for i := range it.Faces {
			it.Faces[i].OK = ok
		}
	}
	for _, i := range idx {
		it.Faces[i].OK = ok
		if !ok {
			it.FacesOK = false
		}
	}
	if it.FacesOK {
		it.FaceRedo = false
	}
	l.saveSoon()
	return prev, nil
}

// SetApproval sets what of an item is approved by hand back to a, as
// ApproveFaces returned it (undo). Faces that are not there any more are
// left out.
func (l *Library) SetApproval(id string, a Approval) error {
	it := l.Get(id)
	if it == nil {
		return fmt.Errorf("unknown item %q", id)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	it.FacesOK = a.Item
	it.Faces = append([]Face(nil), it.Faces...) // copy on write, as in ApproveFaces
	for i := range it.Faces {
		it.Faces[i].OK = false
	}
	for _, i := range a.Faces {
		if i >= 0 && i < len(it.Faces) {
			it.Faces[i].OK = true
		}
	}
	if it.FacesOK {
		it.FaceRedo = false
	}
	l.saveSoon()
	return nil
}

// approvedLocked is what of an item is approved by hand; the caller holds
// l.mu.
func approvedLocked(it *Item) Approval {
	a := Approval{Item: it.FacesOK, Faces: []int{}}
	for i, f := range it.Faces {
		if f.OK {
			a.Faces = append(a.Faces, i)
		}
	}
	return a
}

// rethinkable lists the photos whose faces are looked for again by
// RethinkFaces: those not approved, with their faces found once, and not
// already waiting for it. With complete, also those whose faces are all
// approved one by one (a face may still be missing); never those approved
// as a whole. Videos are left out: their frames are dear.
func (l *Library) rethinkable(complete bool) []*Item {
	labeled := l.people.labeledAll()
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []*Item
	for _, it := range l.items {
		if it.Kind != Photo || !it.FacesDone || it.FaceRedo || it.FacesOK {
			continue
		}
		if _, ok := approval(it, labeled[it.ID]); !ok || complete {
			out = append(out, it)
		}
	}
	return out
}

// RethinkCount returns how many photos RethinkFaces would look at, without
// and with complete.
func (l *Library) RethinkCount() (n, complete int) {
	return len(l.rethinkable(false)), len(l.rethinkable(true))
}

// RethinkFaces looks for the faces of every photo not approved again, in
// the background (and after a restart, if it did not get through):
// approved faces keep their place, the others are replaced by what is
// found now, faces found anew are added (grouping names them), and those
// not found any more are put aside as no face. With complete, photos whose
// faces are all approved one by one are looked at too (for faces missed).
// It returns the number of photos.
func (l *Library) RethinkFaces(complete bool) (int, error) {
	if !l.FaceStatus().Detection {
		return 0, errors.New("face detection is not available")
	}
	list := l.rethinkable(complete)
	if len(list) == 0 {
		return 0, nil
	}
	l.mu.Lock()
	for _, it := range list {
		it.FaceRedo = true
	}
	l.mu.Unlock()
	l.saveSoon()
	l.logf("faces: looking for the faces of %d photos not approved again", len(list))
	l.media.kick()
	return len(list), nil
}

// leftover returns the indexes of old faces that a new search did not find
// again (mergeFaces kept them at their place): neither kept nor taken by a
// found face.
func leftover(old []Face, labeled map[int]bool, place []int) []int {
	kept := false
	for i, f := range old {
		kept = kept || faceKept(f, labeled[i])
	}
	if !kept {
		return nil // the found faces replaced all old ones
	}
	taken := make(map[int]bool, len(place))
	for _, to := range place {
		taken[to] = true
	}
	var out []int
	for i, f := range old {
		if !faceKept(f, labeled[i]) && !taken[i] {
			out = append(out, i)
		}
	}
	return out
}
