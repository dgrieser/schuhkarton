package gallery

import (
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// The second recognition system: with a model for it in the face service,
// every face has a second embedding (Face.Emb2), and a second, more generous
// search finds faces the main one left out (Item.Extra). The main system
// stays in the lead: its embeddings make the groups and its thresholds
// decide. The second one helps where the first is unsure: faces drawn by
// hand, likeness close to the threshold, and the extra faces, which are
// only taken in when the second system knows whose they are.

// faceGenWant is what an item's faces are looked for with now: the second
// embedding and, for photos, the second search, when the service has them.
func faceGenWant(fs FaceInfo, it *Item) uint8 {
	var g uint8
	if fs.Aura && it.Kind == Photo { // a video is not read again for it (frames are dear here); new ones get it anyway
		g |= genAura
	}
	if fs.Extra && fs.Aura && it.Kind == Photo { // the extra faces are of use only with the second system
		g |= genExtra
	}
	return g
}

// ExtraPath is the crop of a face of the second search.
func (l *Library) ExtraPath(it *Item, i int) string { return l.extraFile(it.key, i) }

// faceGen bits: what an item's faces were looked for with.
const (
	genAura  uint8 = 1 << iota // the second embedding
	genExtra                   // the second search
)

// quant2 keeps an embedding as signed bytes (its direction is what counts).
func quant2(v []float32) []int8 {
	if len(v) == 0 {
		return nil
	}
	var m float32
	for _, x := range v {
		m = max(m, float32(math.Abs(float64(x))))
	}
	if m == 0 {
		return nil
	}
	out := make([]int8, len(v))
	for i, x := range v {
		out[i] = int8(math.Round(float64(x / m * 127)))
	}
	return out
}

// unit2 turns a quantized embedding back into a unit vector.
func unit2(q []int8) []float32 {
	if len(q) == 0 {
		return nil
	}
	v := make([]float32, len(q))
	for i, x := range q {
		v[i] = float32(x)
	}
	return normalize(v)
}

// cos2 is the likeness of two quantized embeddings.
func cos2(a, b []int8) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return -2
	}
	var d, na, nb int64
	for i := range a {
		x, y := int64(a[i]), int64(b[i])
		d += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return -2
	}
	return float32(float64(d) / math.Sqrt(float64(na)*float64(nb)))
}

// on2 maps a likeness of the second system onto the scale of the first: its
// likenesses run lower (measured on a real library: 0.35 there meant what
// 0.42 means for the first, 0.40 what 0.48 means).
const on2 = 1.2

// fuse is the likeness to a person from both systems: their mean where both
// know the face, else the one that does (-2: neither).
func fuse(s1, s2 float32) float32 {
	switch {
	case s1 > -2 && s2 > -2:
		return (s1 + s2*on2) / 2
	case s2 > -2:
		return s2 * on2
	}
	return s1
}

// knnLikeness2 is knnLikeness for the second system's embeddings.
func knnLikeness2(e []int8, faces [][]int8) float32 {
	var best [knnK]float32
	n := 0
	for _, x := range faces {
		v := cos2(e, x)
		if v < -1 {
			continue
		}
		if n < knnK {
			best[n] = v
			n++
			continue
		}
		lo := 0
		for j := 1; j < knnK; j++ {
			if best[j] < best[lo] {
				lo = j
			}
		}
		if v > best[lo] {
			best[lo] = v
		}
	}
	if n == 0 {
		return -2
	}
	var sum float32
	for _, v := range best[:n] {
		sum += v
	}
	return sum/float32(n) - 0.03*float32(knnK-n)
}

// Joining a named person with both systems (measured on 478 named faces of
// a real family: precision 94.6 % -> 98.2 %, about 6 % more left unnamed):
// their mean at least named_threshold (weak faces weakFused more), and at
// least fusedMargin ahead of the next person.
const (
	weakFused   = 0.06
	fusedMargin = 0.05
)

// Taking in faces of the second search: one becomes a face of a named person
// when the second system finds it clearly like that person's faces (and
// clearly ahead of the next person), and the first one agrees: it finds the
// same person most alike, or that person alike enough (or does not know the
// face). On 250 family photos this took in 15 of 197 extra faces, all right.
const (
	promote2       = 0.40 // second system: mean of the 3 most alike faces of the person
	promoteMargin  = 0.05 // ahead of the next person, second system
	promoteAgree1  = 0.30 // first system: this alike to the person is enough
	promoteRef     = 200  // faces of a person looked at, to keep it quick on small machines
	promoteMinRefs = 3
	promoteGap     = 15 * time.Minute
)

// promoteExtras moves faces of the second search that the second system
// knows into their items' faces, with the person as their hint; true when
// it moved any. It runs after grouping, at most every promoteGap (unless
// forced: a face was named).
func (l *Library) promoteExtras(force bool) bool {
	if !force && time.Since(time.Unix(l.promotedAt.Load(), 0)) < promoteGap {
		return false
	}
	type ref struct {
		name string
		cent []float32   // second system, unit
		f2   [][]float32 // second system, unit (at most promoteRef)
		f1   [][]float32 // first system
		cnt1 []float32
	}
	s := l.people
	s.mu.RLock()
	var persons []*Person
	for _, p := range s.people {
		if p.Name != "" {
			persons = append(persons, p)
		}
	}
	s.mu.RUnlock()
	// what is looked at, copied under the lock (the rest runs without it:
	// it takes long on a small machine, and the library must not wait)
	type extraOf struct {
		it    *Item
		extra []Face // the list as it is (a scan replaces it, never changes it)
		here  map[string]bool
	}
	type rawRef struct {
		name string
		cnt1 []float32
		f2   [][]int8
		f1   [][]float32
	}
	var raws []rawRef
	var todo []extraOf
	l.mu.RLock()
	for _, it := range l.items {
		if len(it.Extra) > 0 && !it.FacesOK { // approved as a whole: no face comes in
			todo = append(todo, extraOf{it: it, extra: it.Extra})
		}
	}
	if len(todo) > 0 {
		for _, p := range persons {
			r := rawRef{name: p.Name, cnt1: p.cent}
			for _, fr := range p.refs {
				it := l.byID[fr.Item]
				if it == nil || fr.Idx >= len(it.Faces) {
					continue
				}
				f := it.Faces[fr.Idx]
				if len(f.Emb2) > 0 && len(r.f2) < promoteRef {
					r.f2 = append(r.f2, f.Emb2)
				}
				if len(f.Emb) > 0 && len(r.f1) < promoteRef {
					r.f1 = append(r.f1, f.Emb)
				}
			}
			raws = append(raws, r)
		}
	}
	l.mu.RUnlock()
	if len(todo) == 0 {
		return false
	}
	l.promotedAt.Store(time.Now().Unix()) // the pause starts with a pass that had something to look at
	for k := range todo {
		here := map[string]bool{}
		for i := range l.ItemFaces(todo[k].it) {
			if _, name := l.FacePerson(todo[k].it.ID, i); name != "" {
				here[name] = true
			}
		}
		todo[k].here = here
	}
	var refs []ref
	for _, r := range raws {
		if len(r.f2) < promoteMinRefs {
			continue
		}
		x := ref{name: r.name, cnt1: r.cnt1, f1: r.f1}
		sum := []float32(nil)
		for _, q := range r.f2 {
			u := unit2(q)
			x.f2 = append(x.f2, u)
			if sum == nil {
				sum = make([]float32, len(u))
			}
			for i, v := range u {
				sum[i] += v
			}
		}
		x.cent = normalize(sum)
		refs = append(refs, x)
	}
	type move struct {
		it   *Item
		i    int
		name string
		f    Face // as it was: a scan may have replaced the list meanwhile
	}
	// each extra face on its own, side by side
	found := make([][]move, len(todo))
	var wg sync.WaitGroup
	next := atomic.Int64{}
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				k := int(next.Add(1) - 1)
				if k >= len(todo) {
					return
				}
				t := todo[k]
				here := map[string]bool{}
				for n := range t.here {
					here[n] = true
				}
				for i, f := range t.extra {
					if len(f.Emb2) == 0 {
						continue
					}
					e2 := unit2(f.Emb2)
					// the people most alike by centroid, then face by face
					type cand struct {
						j int
						c float32
					}
					var cs []cand
					for j, r := range refs {
						if c := dot(e2, r.cent); c >= promote2-0.2 {
							cs = append(cs, cand{j, c})
						}
					}
					sort.Slice(cs, func(a, b int) bool { return cs[a].c > cs[b].c })
					if len(cs) > knnTop {
						cs = cs[:knnTop]
					}
					best, bestS, nextS := -1, float32(-2), float32(-2)
					for _, c := range cs {
						v := knnLikeness(e2, refs[c.j].f2)
						switch {
						case v > bestS:
							best, bestS, nextS = c.j, v, bestS
						case v > nextS:
							nextS = v
						}
					}
					if best < 0 || bestS < promote2 || bestS-nextS < promoteMargin || here[refs[best].name] {
						continue
					}
					// the first system agrees: the same person most alike (of the
					// people most alike by its centroids), or alike enough
					if len(f.Emb) > 0 {
						e1 := normalize(f.Emb)
						like := func(r ref) float32 { return max(dot(e1, r.cnt1), knnLikeness(e1, r.f1)) }
						mine := like(refs[best])
						if mine < promoteAgree1 {
							var c1 []cand
							for j, r := range refs {
								if j != best && len(r.cnt1) == len(e1) {
									c1 = append(c1, cand{j, dot(e1, r.cnt1)})
								}
							}
							sort.Slice(c1, func(a, b int) bool { return c1[a].c > c1[b].c })
							if len(c1) > knnTop {
								c1 = c1[:knnTop]
							}
							top := true
							for _, c := range c1 {
								if like(refs[c.j]) > mine {
									top = false
									break
								}
							}
							if !top {
								continue
							}
						}
					}
					found[k] = append(found[k], move{t.it, i, refs[best].name, f})
				}
			}
		}()
	}
	wg.Wait()
	var moves []move
	for _, ms := range found {
		moves = append(moves, ms...)
	}
	if len(moves) == 0 {
		return false
	}
	byItem := map[*Item][]move{}
	for _, m := range moves {
		byItem[m.it] = append(byItem[m.it], m)
	}
	l.mu.Lock()
	n := 0
	for it, ms := range byItem {
		if l.byID[it.ID] != it || it.FacesOK {
			continue // scanned anew or approved meanwhile
		}
		faces := append([]Face(nil), it.Faces...)
		gone := map[int]bool{}
		used := map[string]bool{} // a person once per picture: the first taken in
		for _, m := range ms {
			if m.i >= len(it.Extra) || !sameBox(it.Extra[m.i], m.f) || len(it.Extra[m.i].Emb2) == 0 {
				continue // scanned anew meanwhile
			}
			f := it.Extra[m.i]
			if used[m.name] { // a person once per picture: left waiting
				continue
			}
			used[m.name] = true
			f.Hint = m.name
			if b, err := os.ReadFile(l.ExtraPath(it, m.i)); err == nil {
				os.WriteFile(l.FacePath(it, len(faces)), b, 0o644)
			}
			faces = append(faces, f)
			gone[m.i] = true
			n++
		}
		// the rest keep their crops: the list keeps its order, taken ones stay as gaps
		extra := append([]Face(nil), it.Extra...)
		for i := range extra {
			if gone[i] {
				extra[i].Emb2, extra[i].Emb = nil, nil // taken: not looked at again
			}
		}
		it.Faces, it.Extra = faces, extra
	}
	l.mu.Unlock()
	if n > 0 {
		l.logf("faces: %d faces of the second search taken in", n)
		l.markDirty()
	}
	return n > 0
}

// An unnamed group goes into a named person when the centroids of the first
// system are at least merge1 alike and those of the second at least merge2,
// and the group shares no picture with the person.
const (
	merge1   = 0.50
	merge2   = 0.45
	mergeMin = 2 // faces with the second embedding the group needs
)

func centroid2(refs []*faceRec) ([]float32, int) {
	var sum []float32
	n := 0
	for _, r := range refs {
		if len(r.emb2) == 0 {
			continue
		}
		if sum == nil {
			sum = make([]float32, len(r.emb2))
		}
		for i, x := range r.emb2 {
			sum[i] += x
		}
		n++
	}
	if n == 0 {
		return nil, 0
	}
	return normalize(sum), n
}

// mergeIntoNamed returns the unnamed groups that stay, after the others went
// into the named person both systems find them alike.
func mergeIntoNamed(clusters, named []*cluster) []*cluster {
	if len(named) == 0 {
		return clusters
	}
	type nc struct {
		c     *cluster
		cent2 []float32
	}
	var ns []nc
	for _, c := range named {
		if cent2, n := centroid2(c.refs); n >= mergeMin && len(c.cent) > 0 {
			ns = append(ns, nc{c, cent2})
		}
	}
	if len(ns) == 0 {
		return clusters
	}
	kept := clusters[:0]
	for _, c := range clusters {
		cent2, n := centroid2(c.refs)
		var best *cluster
		bestS := float32(-2)
		if n >= mergeMin && len(c.cent) > 0 {
			for _, x := range ns {
				s1, s2 := dot(c.cent, x.c.cent), dot(cent2, x.cent2)
				if s1 < merge1 || s2 < merge2 {
					continue
				}
				shared := false
				for fr := range c.frames {
					if x.c.frames[fr] {
						shared = true
						break
					}
				}
				for _, r := range c.refs { // a face said not to be the person keeps the group apart
					shared = shared || rejects(r.label, x.c.name)
				}
				if !shared && s1+s2 > bestS {
					best, bestS = x.c, s1+s2
				}
			}
		}
		if best == nil {
			kept = append(kept, c)
			continue
		}
		for _, r := range c.refs {
			best.join(r)
		}
	}
	return kept
}
