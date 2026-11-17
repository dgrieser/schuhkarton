package gallery

import (
	"errors"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Matching names anew: the faces nobody approved get the named person they
// are most alike, measured against the approved faces of each person only
// (faces with a name, and faces approved or drawn by hand in a named
// person), so a wrong name does not hold itself up. No face is looked for
// again. The person found goes into the face's hint (Face.Hint, which
// grouping follows like a face taken in from the second search), not into
// a label: the face stays not approved. A face that is like nobody clearly
// enough loses its hint and is grouped by likeness as before.

// rematchFace is a face looked at by RematchNames, with what was found.
type rematchFace struct {
	it    *Item
	idx   int
	f     Face // as it was, to see whether it changed meanwhile
	emb   []float32
	emb2  []float32
	weak  bool
	cands []match2
	hint  string
}

type match2 struct {
	name string
	sim  float32
}

// rematchRef is a named person with their approved faces.
type rematchRef struct {
	name         string
	f1, f2       [][]float32
	cent1, cent2 []float32
}

// RematchCount returns how many faces RematchNames would look at.
func (l *Library) RematchCount() int {
	_, faces, _ := l.rematchSnapshot()
	return len(faces)
}

// RematchNames matches the names of the faces nobody approved anew, in the
// background. It returns the number of faces looked at.
func (l *Library) RematchNames() (int, error) {
	if !l.FaceStatus().Recognition {
		return 0, ErrNoRecognition
	}
	if !l.rematching.CompareAndSwap(false, true) {
		return 0, errors.New("names are already being matched")
	}
	_, faces, _ := l.rematchSnapshot()
	if len(faces) == 0 {
		l.rematching.Store(false)
		return 0, nil
	}
	l.Go(func() {
		defer l.rematching.Store(false)
		l.rematch()
	})
	return len(faces), nil
}

// rematch is RematchNames, right here; it returns the faces looked at and
// the faces whose name changed.
func (l *Library) rematch() (looked, changed int) {
	refs, faces, here := l.rematchSnapshot()
	if len(faces) == 0 {
		return 0, 0
	}
	l.logf("faces: matching the names of %d faces not approved anew", len(faces))
	need := l.people.threshNamed
	var wg sync.WaitGroup
	next := atomic.Int64{}
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				k := int(next.Add(1) - 1)
				if k >= len(faces) || l.closing.Load() {
					return
				}
				faces[k].cands = rematchCands(faces[k], refs, need)
			}
		}()
	}
	wg.Wait()
	if l.closing.Load() {
		return 0, 0
	}
	// per picture the most alike pairs first: a person is in a picture once
	type pair struct {
		f    *rematchFace
		name string
		sim  float32
	}
	byFrame := map[frame][]*rematchFace{}
	for k := range faces {
		fr := frame{faces[k].it.ID, faces[k].f.T}
		byFrame[fr] = append(byFrame[fr], &faces[k])
	}
	for fr, fs := range byFrame {
		var pairs []pair
		for _, f := range fs {
			for _, c := range f.cands {
				pairs = append(pairs, pair{f, c.name, c.sim})
			}
		}
		sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].sim > pairs[b].sim })
		taken := map[string]bool{}
		for n := range here[fr] {
			taken[n] = true
		}
		done := map[*rematchFace]bool{}
		for _, p := range pairs {
			if done[p.f] || taken[p.name] {
				continue
			}
			p.f.hint, done[p.f], taken[p.name] = p.name, true, true
		}
	}
	// into the items, unless they changed meanwhile
	byItem := map[*Item][]*rematchFace{}
	for k := range faces {
		byItem[faces[k].it] = append(byItem[faces[k].it], &faces[k])
	}
	l.mu.Lock()
	for it, fs := range byItem {
		if l.byID[it.ID] != it || it.FacesOK {
			continue
		}
		var out []Face
		for _, f := range fs {
			if f.idx >= len(it.Faces) {
				continue
			}
			cur := it.Faces[f.idx]
			if !sameBox(cur, f.f) || cur.OK || cur.Manual || cur.Hint == f.hint {
				continue
			}
			if out == nil {
				out = append([]Face(nil), it.Faces...) // copy on write: grouping reads snapshots without the lock
			}
			out[f.idx].Hint = f.hint
			changed++
		}
		if out != nil {
			it.Faces = out
		}
	}
	l.mu.Unlock()
	l.logf("faces: names of %d faces not approved matched anew, %d changed", len(faces), changed)
	if changed > 0 {
		l.markDirty()
		l.regroup()
	}
	return len(faces), changed
}

// rematchSnapshot lists the named people with their approved faces, the
// faces not approved (with an embedding, in items not approved as a
// whole), and per picture the names approved faces have there.
func (l *Library) rematchSnapshot() ([]*rematchRef, []rematchFace, map[frame]map[string]bool) {
	s := l.people
	s.mu.RLock()
	labels := make(map[string]string, len(s.labels))
	for k, v := range s.labels {
		labels[k] = v
	}
	personOf := make(map[string]string, len(s.byFace)) // face key -> name of its person
	for k, id := range s.byFace {
		if p := s.byID[id]; p != nil && p.Name != "" {
			personOf[k] = p.Name
		}
	}
	s.mu.RUnlock()

	byName := map[string]*rematchRef{}
	var faces []rematchFace
	here := map[frame]map[string]bool{}
	l.mu.RLock()
	for _, it := range l.items {
		for i, f := range it.Faces {
			key := FaceRef{it.ID, i}.key()
			label := labels[key]
			if !faceKept(f, label != "") {
				if it.FacesOK || (f.Emb == nil && f.Emb2 == nil) {
					continue
				}
				r := rematchFace{it: it, idx: i, f: f, hint: ""}
				if f.Emb != nil {
					r.emb = normalize(f.Emb)
				}
				if f.Emb2 != nil {
					r.emb2 = unit2(f.Emb2)
				}
				short := float32(-1)
				if it.W > 0 && it.H > 0 {
					short = min(f.W*float32(it.W), f.H*float32(it.H))
				}
				r.weak = f.Score < weakScore || (short >= 0 && short < weakPx)
				faces = append(faces, r)
				continue
			}
			name := ""
			switch {
			case label == labelHidden, label == labelNoFace, strings.HasPrefix(label, "!"):
			case label != "":
				name = label
			default: // approved or drawn by hand: the person it is in
				name = personOf[key]
			}
			if name == "" {
				continue
			}
			fr := frame{it.ID, f.T}
			if here[fr] == nil {
				here[fr] = map[string]bool{}
			}
			here[fr][name] = true
			ref := byName[name]
			if ref == nil {
				ref = &rematchRef{name: name}
				byName[name] = ref
			}
			if f.Emb != nil {
				ref.f1 = append(ref.f1, normalize(f.Emb))
			}
			if f.Emb2 != nil {
				ref.f2 = append(ref.f2, unit2(f.Emb2))
			}
		}
	}
	l.mu.RUnlock()

	refs := make([]*rematchRef, 0, len(byName))
	for _, r := range byName {
		r.cent1, r.cent2 = meanUnit(r.f1), meanUnit(r.f2)
		if r.cent1 != nil || r.cent2 != nil {
			refs = append(refs, r)
		}
	}
	sort.Slice(refs, func(a, b int) bool { return refs[a].name < refs[b].name })
	if len(refs) == 0 {
		return nil, nil, nil // nobody to match against
	}
	return refs, faces, here
}

// meanUnit is the centroid of unit vectors of the most common length, unit
// itself (nil: none).
func meanUnit(vs [][]float32) []float32 {
	count := map[int]int{}
	for _, v := range vs {
		count[len(v)]++
	}
	n := 0
	for d, c := range count {
		if c > count[n] || (c == count[n] && d > n) {
			n = d
		}
	}
	if n == 0 {
		return nil
	}
	sum := make([]float32, n)
	for _, v := range vs {
		if len(v) == n {
			for i, x := range v {
				sum[i] += x
			}
		}
	}
	return normalize(sum)
}

// rematchCands lists the people a face is alike enough, the most alike
// first, as grouping asks of a named person (named_threshold, more for weak
// faces; with both systems clearly ahead of the next person). None when two
// people are about as alike: left to grouping and the user.
func rematchCands(f rematchFace, refs []*rematchRef, need float32) []match2 {
	type pre struct {
		r  *rematchRef
		by float32
	}
	cdot := func(a, b []float32) float32 {
		if len(a) == 0 || len(a) != len(b) {
			return -2
		}
		return dot(a, b)
	}
	// the people most alike by their centroids, then face by face
	var ps []pre
	for _, r := range refs {
		s1, s2 := cdot(f.emb, r.cent1), cdot(f.emb2, r.cent2)
		if s1 < -1 && s2 < -1 {
			continue
		}
		ps = append(ps, pre{r, fuse(s1, s2)})
	}
	sort.Slice(ps, func(a, b int) bool { return ps[a].by > ps[b].by })
	if len(ps) > knnTop {
		ps = ps[:knnTop]
	}
	type cand struct {
		name  string
		sim   float32
		fused bool
	}
	var cs []cand
	for _, p := range ps {
		s1 := cdot(f.emb, p.r.cent1)
		if len(f.emb) > 0 && len(p.r.f1) > 0 {
			s1 = max(s1, knnLikeness(f.emb, p.r.f1))
		}
		s2 := float32(-2)
		if len(f.emb2) > 0 && len(p.r.f2) > 0 {
			s2 = knnLikeness(f.emb2, p.r.f2)
		}
		cs = append(cs, cand{p.r.name, fuse(s1, s2), s1 > -2 && s2 > -2})
	}
	sort.Slice(cs, func(a, b int) bool { return cs[a].sim > cs[b].sim })
	var out []match2
	for k, c := range cs {
		th := need
		switch {
		case f.weak && c.fused:
			th += weakFused
		case f.weak:
			th += weakExtra
		}
		if c.sim < th || math.IsNaN(float64(c.sim)) {
			break // sorted: the rest are lower
		}
		if c.fused && k == 0 && len(cs) > 1 && c.sim-cs[1].sim < fusedMargin {
			break // two people about as alike
		}
		out = append(out, match2{c.name, c.sim})
	}
	return out
}
