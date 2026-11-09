package gallery

import (
	"fmt"
	"slices"
	"sort"
	"time"
)

// Hidden people: "Ausblenden" marks every face of a group as hidden ("~").
// The group is remembered together with the labels its faces had before, so
// it can be shown again exactly as it was. Faces hidden before groups were
// remembered are grouped by similarity among themselves.

type hiddenRec struct {
	ID    string            `json:"id"` // person id at the time of hiding
	Name  string            `json:"name,omitempty"`
	At    int64             `json:"at"`
	Faces map[string]string `json:"faces"` // face key -> label before hiding ("" for none)
}

// HiddenPerson is one hidden group in the list of hidden people.
type HiddenPerson struct {
	ID     string    `json:"id"`
	Name   string    `json:"name,omitempty"`
	Count  int       `json:"count"` // photos
	Faces  int       `json:"faces"`
	Cover  FaceRef   `json:"cover"`
	Sample []FaceRef `json:"sample"` // a few more faces to recognise the group
	At     int64     `json:"at,omitempty"`
	Loose  bool      `json:"loose,omitempty"` // single hidden faces of different people, shown together
	keys   []string
	recID  string
}

const (
	hiddenSample = 6
	looseID      = "h-einzeln"
)

// HidePerson hides all faces of a person and returns the id of the hidden
// group, for ShowHidden.
func (l *Library) HidePerson(id string) (string, error) {
	s := l.people
	hid, err := s.hide(id)
	if err != nil {
		return "", err
	}
	if err := s.save(); err != nil {
		return "", err
	}
	l.recluster()
	l.bump()
	return hid, nil
}

func (s *peopleStore) hide(id string) (string, error) {
	s.mu.Lock()
	p := s.byID[id]
	if p == nil {
		s.mu.Unlock()
		return "", fmt.Errorf("unknown person %q", id)
	}
	rec := hiddenRec{ID: p.ID, Name: p.Name, At: time.Now().Unix(), Faces: map[string]string{}}
	for _, r := range p.refs {
		k := r.key()
		rec.Faces[k] = s.labels[k]
		s.labels[k] = labelHidden
	}
	// the same group hidden again: keep the faces hidden the first time
	if i := slices.IndexFunc(s.hidden, func(h hiddenRec) bool { return h.ID == rec.ID }); i >= 0 {
		for k, v := range s.hidden[i].Faces {
			if _, ok := rec.Faces[k]; !ok && s.labels[k] == labelHidden {
				rec.Faces[k] = v
			}
		}
		s.hidden = slices.Delete(s.hidden, i, i+1)
	}
	s.hidden = append(s.hidden, rec)
	s.pruneHiddenLocked()
	s.mu.Unlock()
	return "h-" + rec.ID, nil
}

// HiddenPeople lists the hidden groups, the most recently hidden first.
func (l *Library) HiddenPeople() []*HiddenPerson {
	return l.people.hiddenGroups(l.faceSnapshot())
}

// ShowHidden shows a hidden group again; remembered groups get their
// former names back.
func (l *Library) ShowHidden(id string) error {
	s := l.people
	if err := s.show(l.faceSnapshot(), id); err != nil {
		return err
	}
	if err := s.save(); err != nil {
		return err
	}
	l.recluster()
	l.bump()
	return nil
}

func (s *peopleStore) show(items []faceSnap, id string) error {
	var g *HiddenPerson
	for _, h := range s.hiddenGroups(items) {
		if h.ID == id {
			g = h
			break
		}
	}
	if g == nil {
		return fmt.Errorf("unknown hidden group %q", id)
	}
	s.mu.Lock()
	var prior map[string]string
	if i := slices.IndexFunc(s.hidden, func(h hiddenRec) bool { return h.ID == g.recID }); g.recID != "" && i >= 0 {
		prior = s.hidden[i].Faces
		// faces not shown now (their photo is in the trash, say) stay remembered
		shown := map[string]bool{}
		for _, k := range g.keys {
			shown[k] = true
		}
		rest := map[string]string{}
		for k, v := range prior {
			if !shown[k] && s.labels[k] == labelHidden {
				rest[k] = v
			}
		}
		if len(rest) > 0 {
			s.hidden[i].Faces = rest
		} else {
			s.hidden = slices.Delete(s.hidden, i, i+1)
		}
	}
	for _, k := range g.keys {
		if s.labels[k] != labelHidden {
			continue
		}
		if v := prior[k]; v != "" {
			s.labels[k] = v
		} else {
			delete(s.labels, k)
		}
	}
	s.pruneHiddenLocked()
	s.mu.Unlock()
	return nil
}

// pruneHiddenLocked forgets remembered groups none of whose faces are hidden any more.
func (s *peopleStore) pruneHiddenLocked() {
	s.hidden = slices.DeleteFunc(s.hidden, func(h hiddenRec) bool {
		for k := range h.Faces {
			if s.labels[k] == labelHidden {
				return false
			}
		}
		return true
	})
}

func (s *peopleStore) hiddenGroups(items []faceSnap) []*HiddenPerson {
	s.mu.RLock()
	labels := make(map[string]string, len(s.labels))
	for k, v := range s.labels {
		labels[k] = v
	}
	recs := slices.Clone(s.hidden)
	s.mu.RUnlock()

	taken, all := faceRecs(items, labels)
	byKey := make(map[string]*faceRec, len(all))
	for _, f := range all {
		byKey[f.ref.key()] = f
	}
	claimed := map[string]bool{}
	var out []*HiddenPerson
	// remembered groups, newest first; a face belongs to the newest group that hid it
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		var refs []*faceRec
		for k := range r.Faces {
			if f := byKey[k]; f != nil && labels[k] == labelHidden && !claimed[k] {
				refs = append(refs, f)
				claimed[k] = true
			}
		}
		sort.Slice(refs, func(a, b int) bool { return refs[a].quality > refs[b].quality })
		if p := newPerson(refs, "h-"+r.ID, r.Name, taken); p != nil {
			out = append(out, hiddenOf(p, r.At, r.ID))
		}
	}
	// the other hidden faces, grouped among themselves; groups too small to
	// count as a person are listed together, so every hidden face can come back
	only := make(map[string]string, len(all))
	for _, f := range all {
		if k := f.ref.key(); labels[k] != labelHidden || claimed[k] {
			only[k] = labelHidden
		}
	}
	var loose []*faceRec
	people, _ := buildPeople(items, only, s.threshNew, s.threshNamed, s.threshSame, true)
	for _, p := range people {
		if p.Faces < minCluster || p.Count < 2 {
			for _, r := range p.refs {
				loose = append(loose, byKey[r.key()])
			}
			continue
		}
		p.ID = "h-" + p.ID
		out = append(out, hiddenOf(p, 0, ""))
	}
	sort.Slice(loose, func(a, b int) bool { return loose[a].quality > loose[b].quality })
	if p := newPerson(loose, looseID, "", taken); p != nil {
		h := hiddenOf(p, 0, "")
		h.Loose = true
		out = append(out, h)
	}
	return out
}

// hiddenOf turns a group into a list entry; its faces are sorted best first.
func hiddenOf(p *Person, at int64, recID string) *HiddenPerson {
	h := &HiddenPerson{ID: p.ID, Name: p.Name, Count: p.Count, Faces: p.Faces, Cover: p.Cover, At: at, recID: recID, Sample: []FaceRef{}}
	for _, r := range p.refs {
		h.keys = append(h.keys, r.key())
		if r != p.Cover && len(h.Sample) < hiddenSample {
			h.Sample = append(h.Sample, r)
		}
	}
	return h
}
