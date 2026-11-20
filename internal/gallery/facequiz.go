package gallery

import (
	"errors"
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Quick rounds: a handful of faces, one after the other, for somebody to
// name or to approve. Which faces is chosen for the most gain per answer
// (active learning):
//
// Naming: one face per unnamed group, the largest groups first. A name given
// there goes to that face and, as a hint, to the rest of its group (see
// HintGroup), so one answer names a whole group, not approved yet. The face
// shown is the group's most typical one (close to its centroid) that is also
// clear (detector sure, not small): the easiest to recognise.
//
// Approving: faces that have a name but are not approved, where the system
// is least sure (uncertainty sampling): their likeness to the approved faces
// of their person is close to named_threshold, or another person is about as
// alike. Each is weighed by how many faces not approved of the same person
// look like it (an answer there stands for many), and people with few
// approved faces come first (they have least to be matched against). Near
// copies (a burst of photos) and the same photo are shown once.
//
// The answers become approved faces (names, approvals, "not this person"),
// which Neu zuordnen and grouping then match the others against.

// QuizFace is one face of a quick round.
type QuizFace struct {
	Item   string  `json:"item"`
	Idx    int     `json:"idx"`
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	W      float32 `json:"w"`
	H      float32 `json:"h"`
	Aspect float64 `json:"aspect"`
	V      string  `json:"v"` // version of the item's pictures
	Person string  `json:"person,omitempty"`
	Name   string  `json:"name,omitempty"`   // the name it has now (approving)
	Group  int     `json:"group,omitempty"`  // faces of its group (naming)
	Photos int     `json:"photos,omitempty"` // photos of its group (naming)
	Score  float32 `json:"score"`            // why it was chosen (larger: more worth it)
}

const (
	quizMax       = 30
	quizSample    = 2000 // faces not approved looked at, at most (a small machine)
	quizRefs      = 200  // approved faces per person compared, at most
	quizDense     = 0.55 // likeness of two faces that stand for each other
	quizDup       = 0.85 // likeness of near copies (a burst)
	quizUncertain = 0.04 // how quickly the uncertainty falls off away from the threshold
)

// QuizFaces chooses up to n faces for a quick round: mode "name" or "approve".
func (l *Library) QuizFaces(mode string, n int) ([]QuizFace, error) {
	if n <= 0 || n > quizMax {
		n = 12
	}
	if !l.FaceStatus().Recognition && l.people.count() == 0 {
		return nil, ErrNoRecognition
	}
	switch mode {
	case "name":
		return l.quizName(n), nil
	case "approve":
		return l.quizApprove(n), nil
	}
	return nil, errors.New("unknown mode")
}

// quizFace fills in a face of an item; the caller holds l.mu.
func quizFace(it *Item, idx int) QuizFace {
	f := it.Faces[idx]
	return QuizFace{Item: it.ID, Idx: idx, X: f.X, Y: f.Y, W: f.W, H: f.H, Aspect: it.Aspect, V: it.Key()[:8]}
}

// clarity is how easy a face is to recognise: the detector's certainty and
// its size in the picture (150 px on the short side and more count fully).
func clarity(it *Item, f Face) float32 {
	short := float32(150)
	if it.W > 0 && it.H > 0 {
		short = min(f.W*float32(it.W), f.H*float32(it.H))
	}
	return 0.5*min(f.Score, 1) + 0.5*min(short/150, 1)
}

func (l *Library) quizName(n int) []QuizFace {
	s := l.people
	type group struct {
		id     string
		refs   []FaceRef
		cent   []float32
		photos int
	}
	s.mu.RLock()
	var groups []group
	for _, p := range s.people {
		if p.Name == "" {
			groups = append(groups, group{p.ID, p.refs, p.cent, p.Count})
		}
	}
	labels := make(map[string]string, len(s.labels))
	for k, v := range s.labels {
		labels[k] = v
	}
	s.mu.RUnlock()
	sort.SliceStable(groups, func(a, b int) bool {
		if groups[a].photos != groups[b].photos {
			return groups[a].photos > groups[b].photos
		}
		return len(groups[a].refs) > len(groups[b].refs)
	})
	var out []QuizFace
	seen := map[string]bool{} // photos shown already
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, g := range groups {
		if len(out) >= n {
			break
		}
		best, bestV := -1, float32(-9)
		var bestIt *Item
		for k, r := range g.refs {
			it := l.byID[r.Item]
			if it == nil || it.Kind != Photo || r.Idx >= len(it.Faces) || seen[it.ID] || labels[r.key()] != "" {
				continue
			}
			f := it.Faces[r.Idx]
			typical := float32(0)
			if len(f.Emb) > 0 && len(f.Emb) == len(g.cent) {
				if nn := dot(f.Emb, f.Emb); nn > 0 {
					typical = dot(f.Emb, g.cent) / float32(math.Sqrt(float64(nn)))
				}
			}
			if v := typical + 0.6*clarity(it, f); v > bestV {
				best, bestV, bestIt = k, v, it
			}
		}
		if best < 0 {
			continue
		}
		q := quizFace(bestIt, g.refs[best].Idx)
		q.Person, q.Group, q.Photos, q.Score = g.id, len(g.refs), g.photos, float32(g.photos)
		seen[bestIt.ID] = true
		out = append(out, q)
	}
	return out
}

// quizCand is a face not approved that has a name, for approving.
type quizCand struct {
	it    *Item
	idx   int
	name  string
	emb   []float32 // unit (once sampled)
	raw2  []int8
	emb2  []float32
	clear float32 // clarity, taken under the lock
	value float32
}

func (l *Library) quizApprove(n int) []QuizFace {
	s := l.people
	s.mu.RLock()
	labels := make(map[string]string, len(s.labels))
	for k, v := range s.labels {
		labels[k] = v
	}
	personOf := make(map[string]string, len(s.byFace))
	idOf := map[string]string{}
	for k, id := range s.byFace {
		if p := s.byID[id]; p != nil && p.Name != "" {
			personOf[k] = p.Name
			idOf[p.Name] = p.ID
		}
	}
	s.mu.RUnlock()

	refs := map[string]*rematchRef{}
	var cands []quizCand
	l.mu.RLock()
	for _, it := range l.items {
		for i, f := range it.Faces {
			key := FaceRef{it.ID, i}.key()
			label := labels[key]
			kept := faceKept(f, approvedLabel(label))
			if !kept {
				name := personOf[key]
				if name == "" || it.FacesOK || it.Kind != Photo || (f.Emb == nil && f.Emb2 == nil) {
					continue
				}
				cands = append(cands, quizCand{it: it, idx: i, name: name, emb: f.Emb, raw2: f.Emb2, clear: clarity(it, f)})
				continue
			}
			name := ""
			switch {
			case label == labelHidden, label == labelNoFace, strings.HasPrefix(label, "!"):
			case label != "":
				name = label
			default:
				name = personOf[key]
			}
			if name == "" {
				continue
			}
			r := refs[name]
			if r == nil {
				r = &rematchRef{name: name}
				refs[name] = r
			}
			if f.Emb != nil && len(r.f1) < quizRefs {
				r.f1 = append(r.f1, normalize(f.Emb))
			}
			if f.Emb2 != nil && len(r.f2) < quizRefs {
				r.f2 = append(r.f2, unit2(f.Emb2))
			}
		}
	}
	l.mu.RUnlock()
	if len(cands) == 0 {
		return nil
	}
	for _, r := range refs {
		r.cent1, r.cent2 = meanUnit(r.f1), meanUnit(r.f2)
	}
	// a sample, the same share of every person (a small machine: the work grows
	// with the faces looked at times the approved faces)
	total := map[string]int{}
	for _, c := range cands {
		total[c.name]++
	}
	if len(cands) > quizSample {
		rand.Shuffle(len(cands), func(a, b int) { cands[a], cands[b] = cands[b], cands[a] })
		cands = cands[:quizSample]
	}
	for k := range cands { // unit length, for the sample only
		c := &cands[k]
		if c.emb != nil {
			c.emb = normalize(c.emb)
		}
		if c.raw2 != nil {
			c.emb2 = unit2(c.raw2)
		}
	}
	byName := map[string][]int{}
	for k, c := range cands {
		byName[c.name] = append(byName[c.name], k)
	}
	need := l.people.threshNamed
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range next {
				c := &cands[k]
				// uncertainty: near the threshold, or another person about as alike
				u := float32(1)
				if own := refs[c.name]; own != nil && (own.cent1 != nil || own.cent2 != nil) {
					sOwn := quizLikeness(c, own)
					sOther := float32(-1)
					for name, r := range refs {
						if name != c.name {
							if v := quizCentroid(c, r); v > sOther-0.1 { // only close ones face by face
								sOther = max(sOther, quizLikeness(c, r))
							}
						}
					}
					margin := min(sOwn-need, sOwn-sOther)
					u = float32(1 / (1 + math.Exp(float64(margin/quizUncertain))))
				}
				// how many faces of the person not approved look like it (in the sample,
				// scaled to all of them)
				same := byName[c.name]
				d := 0
				for _, j := range same {
					if j != k && len(c.emb) > 0 && len(cands[j].emb) == len(c.emb) && dot(c.emb, cands[j].emb) >= quizDense {
						d++
					}
				}
				dense := float64(d) * float64(total[c.name]) / float64(len(same))
				// people with few approved faces first
				anchors := 0
				if r := refs[c.name]; r != nil {
					anchors = max(len(r.f1), len(r.f2))
				}
				boost := 1 + 1/float32(1+anchors)
				c.value = (0.25 + u) * float32(math.Log(2+dense)) * boost * (0.5 + c.clear)
			}
		}()
	}
	for k := range cands {
		next <- k
	}
	close(next)
	wg.Wait()
	sort.Slice(cands, func(a, b int) bool { return cands[a].value > cands[b].value })

	// the most worth it, spread over people, no photo twice, no near copies
	perName := map[string]int{}
	people := len(byName)
	most := max(2, (n+people-1)/people+1)
	seen := map[string]bool{}
	var picked []*quizCand
	var out []QuizFace
	l.mu.RLock()
	defer l.mu.RUnlock()
	for k := range cands {
		c := &cands[k]
		if len(out) >= n {
			break
		}
		if seen[c.it.ID] || perName[c.name] >= most || c.idx >= len(c.it.Faces) {
			continue
		}
		dup := false
		for _, p := range picked {
			if len(c.emb) > 0 && len(p.emb) == len(c.emb) && dot(c.emb, p.emb) >= quizDup {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		picked = append(picked, c)
		seen[c.it.ID] = true
		perName[c.name]++
		q := quizFace(c.it, c.idx)
		q.Name, q.Person, q.Score = c.name, idOf[c.name], c.value
		out = append(out, q)
	}
	return interleave(out)
}

// interleave orders the faces so the same person does not come twice in a
// row where another can come between (each person's faces keep their order,
// the most worth it first).
func interleave(fs []QuizFace) []QuizFace {
	var names []string
	by := map[string][]QuizFace{}
	for _, f := range fs {
		if by[f.Name] == nil {
			names = append(names, f.Name)
		}
		by[f.Name] = append(by[f.Name], f)
	}
	out := make([]QuizFace, 0, len(fs))
	for len(out) < len(fs) {
		for _, n := range names {
			if len(by[n]) > 0 {
				out = append(out, by[n][0])
				by[n] = by[n][1:]
			}
		}
	}
	return out
}

// quizCentroid is a face's likeness to a person's approved centroids.
func quizCentroid(c *quizCand, r *rematchRef) float32 {
	s1, s2 := float32(-2), float32(-2)
	if len(c.emb) > 0 && len(c.emb) == len(r.cent1) {
		s1 = dot(c.emb, r.cent1)
	}
	if len(c.emb2) > 0 && len(c.emb2) == len(r.cent2) {
		s2 = dot(c.emb2, r.cent2)
	}
	return fuse(s1, s2)
}

// quizLikeness is a face's likeness to a person's approved faces, as
// Neu zuordnen measures it (the better of centroid and nearest faces, both
// systems).
func quizLikeness(c *quizCand, r *rematchRef) float32 {
	s1 := float32(-2)
	if len(c.emb) > 0 {
		if len(c.emb) == len(r.cent1) {
			s1 = dot(c.emb, r.cent1)
		}
		if len(r.f1) > 0 {
			s1 = max(s1, knnLikeness(c.emb, r.f1))
		}
	}
	s2 := float32(-2)
	if len(c.emb2) > 0 && len(r.f2) > 0 {
		s2 = knnLikeness(c.emb2, r.f2)
	}
	return fuse(s1, s2)
}

// HintGroup gives the faces of a group (a person's faces as grouped now)
// the name as their hint, except one face (the one named by hand) and faces
// approved or labeled: the group follows the name, not approved. It returns
// the hints before, for RestoreHints.
func (l *Library) HintGroup(person, name string, except FaceRef) (map[string]string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == labelHidden || name == labelNoFace || strings.HasPrefix(name, "!") {
		return nil, errors.New("not a name")
	}
	s := l.people
	s.mu.RLock()
	p := s.byID[person]
	var refs []FaceRef
	if p != nil {
		refs = p.refs
	}
	labeled := map[FaceRef]bool{}
	for _, r := range refs {
		if s.labels[r.key()] != "" {
			labeled[r] = true
		}
	}
	s.mu.RUnlock()
	if p == nil {
		return nil, errors.New("unknown person")
	}
	prev := map[string]string{}
	l.mu.Lock()
	byItem := map[*Item][]int{}
	for _, r := range refs {
		if r == except || labeled[r] {
			continue
		}
		if it := l.byID[r.Item]; it != nil && r.Idx < len(it.Faces) && !it.FacesOK && !it.Faces[r.Idx].OK && !it.Faces[r.Idx].Manual {
			byItem[it] = append(byItem[it], r.Idx)
		}
	}
	for it, idx := range byItem {
		faces := append([]Face(nil), it.Faces...) // copy on write: grouping reads snapshots without the lock
		for _, i := range idx {
			prev[FaceRef{it.ID, i}.key()] = faces[i].Hint
			faces[i].Hint = name
		}
		it.Faces = faces
	}
	l.mu.Unlock()
	if len(prev) > 0 {
		l.markDirty()
	}
	return prev, nil
}

// RejectFace says a face is not the named person (adds the name to those
// it is not), whoever it is grouped with now. It returns the label before.
func (l *Library) RejectFace(item string, idx int, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "!") || name == labelHidden || name == labelNoFace || strings.Contains(name, rejectSep) {
		return "", errors.New("not a name")
	}
	l.promoteNow.Store(true)
	s := l.people
	k := FaceRef{item, idx}.key()
	s.mu.Lock()
	prev := s.labels[k]
	if approvedLabel(prev) && prev != name {
		s.mu.Unlock()
		return prev, errors.New("the face has another name now")
	}
	if prev == name { // named so by hand before: now said not to be
		s.labels[k] = withReject("", name)
	} else {
		s.labels[k] = withReject(prev, name)
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return prev, err
	}
	l.regroup()
	return prev, nil
}

// RejectNamed says faces are none of the people named now (each name goes
// to those they are not): a face of the item, or with group the faces of its
// group (as grouped now) not settled by a label nor approved. Names given
// later are not meant. It returns the labels before (keys "item:idx").
func (l *Library) RejectNamed(item string, idx int, group bool) (map[string]string, error) {
	s := l.people
	self := FaceRef{item, idx}
	refs := []FaceRef{self}
	s.mu.RLock()
	if group {
		if p := s.byID[s.byFace[self.key()]]; p != nil && p.Name == "" {
			refs = append([]FaceRef(nil), p.refs...)
		}
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range s.people {
		if p.Name != "" && !seen[p.Name] {
			seen[p.Name] = true
			names = append(names, p.Name)
		}
	}
	// names given just now are in the labels before grouping caught up
	for _, v := range s.labels {
		if approvedLabel(v) && v != labelHidden && v != labelNoFace && !seen[v] {
			seen[v] = true
			names = append(names, v)
		}
	}
	s.mu.RUnlock()
	if len(names) == 0 {
		return nil, errors.New("nobody is named yet")
	}
	sort.Strings(names)
	// the faces there are, not approved (the one asked about always)
	l.mu.RLock()
	var faces []FaceRef
	for _, r := range refs {
		it := l.byID[r.Item]
		if it == nil || r.Idx < 0 || r.Idx >= len(it.Faces) {
			continue
		}
		if f := it.Faces[r.Idx]; r != self && (f.OK || f.Manual || it.FacesOK) {
			continue
		}
		faces = append(faces, r)
	}
	l.mu.RUnlock()
	if len(faces) == 0 {
		return nil, errors.New("unknown face")
	}
	l.promoteNow.Store(true)
	prev := map[string]string{}
	s.mu.Lock()
	for _, r := range faces {
		k := r.key()
		cur := s.labels[k]
		if r != self && cur != "" && !strings.HasPrefix(cur, "!") {
			continue // named, hidden or no face by somebody: stays
		}
		if r == self && approvedLabel(cur) && cur != labelHidden && cur != labelNoFace {
			cur = "" // named before: now none of them
		}
		next := cur
		for _, n := range names {
			next = withReject(next, n)
		}
		prev[k] = s.labels[k]
		s.labels[k] = next
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return prev, err
	}
	l.regroup()
	return prev, nil
}

// HintUndoPrefix marks the hints before in the undo of a face name (history).
const HintUndoPrefix = "hint:"

// RestoreHints sets hints back (key "item:idx" -> hint), as HintGroup
// returned them. It returns how many faces there were for them.
func (l *Library) RestoreHints(hints map[string]string) int {
	l.mu.Lock()
	byItem := map[*Item]map[int]string{}
	for k, h := range hints {
		id, rest, ok := strings.Cut(k, ":")
		it := l.byID[id]
		i, err := strconv.Atoi(rest)
		if !ok || err != nil || it == nil || i < 0 || i >= len(it.Faces) {
			continue
		}
		if byItem[it] == nil {
			byItem[it] = map[int]string{}
		}
		byItem[it][i] = h
	}
	n := 0
	for it, hs := range byItem {
		faces := append([]Face(nil), it.Faces...)
		for i, h := range hs {
			faces[i].Hint = h
			n++
		}
		it.Faces = faces
	}
	l.mu.Unlock()
	if len(byItem) > 0 {
		l.markDirty()
		l.regroup()
	}
	return n
}
