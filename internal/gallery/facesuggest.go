package gallery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Name suggestions: while somebody names a face (one marked by hand right
// now, or one that was found), the named people it looks most alike are
// offered first. A face's likeness to a person is the better of two: to the
// person's centroid, and to the person's few most alike faces (k nearest
// neighbours, which also catches people whose faces changed over the years,
// a child growing up, where the centroid lies between the ages).

// Suggestion is a named person a face looks like.
type Suggestion struct {
	Name   string  `json:"name"`
	Person string  `json:"person"`
	Score  float32 `json:"score"` // cosine similarity, -1..1
	// Level: "high" (very likely), "mid" (as alike as clustering asks of a
	// named person) or "low" (maybe)
	Level string `json:"level"`
}

const (
	suggestMax = 5
	suggestKNN = 3
	// how far below named_threshold a person is still offered ("low"), and
	// above it "high" starts
	suggestBelow = 0.1
	suggestAbove = 0.12
	suggestTTL   = 10 * time.Minute
)

// suggestedEmb is what the suggestions found for a drawn box: AddFace takes
// it when the same box is saved, so the face service is not asked twice.
type suggestedEmb struct {
	it    *Item
	emb   []float32 // nil: no face found in the box
	found bool      // the service answered
	at    time.Time
}

func suggestKey(id string, box FaceBox, t float64) string {
	return fmt.Sprintf("%s|%g|%g|%g|%g|%.3f", id, box.X, box.Y, box.W, box.H, max(t, 0))
}

// ErrNoRecognition: there is no face recognition (off, or no model), so no suggestions.
var ErrNoRecognition = errors.New("no face recognition")

// SuggestForBox suggests names for a box drawn by hand (at second t of a
// video) before it is saved. It asks the face service for the embedding of
// the box, like manualEmbedding does after saving. No face in the box:
// no suggestions, no error.
func (l *Library) SuggestForBox(ctx context.Context, id string, box FaceBox, t float64) ([]Suggestion, error) {
	it := l.Get(id)
	if it == nil {
		return nil, errors.New("unknown item")
	}
	if !box.valid() {
		return nil, errors.New("invalid box")
	}
	if l.faces == nil || !l.FaceStatus().Recognition {
		return nil, ErrNoRecognition
	}
	l.pruneSuggested()
	key := suggestKey(id, box, t)
	var emb []float32
	if c, ok := l.suggestEmb.Load(key); ok && c.(suggestedEmb).it == it {
		emb = c.(suggestedEmb).emb
	} else {
		f := Face{X: box.X, Y: box.Y, W: box.W, H: box.H, T: float32(max(t, 0)), Manual: true}
		var err error
		if emb, err = l.manualEmbedding(ctx, it, f); err != nil {
			return nil, err
		}
		l.suggestEmb.Store(key, suggestedEmb{it: it, emb: emb, found: true, at: time.Now()})
	}
	if emb == nil {
		return []Suggestion{}, nil
	}
	return l.suggest(emb, frame{id, float32(max(t, 0))}, FaceRef{Item: id, Idx: -1}, ""), nil
}

// SuggestForFace suggests names for a face of an item (one with an
// embedding): not the names it was said not to be, nor names already on
// another face of its picture.
func (l *Library) SuggestForFace(id string, idx int) ([]Suggestion, error) {
	it := l.Get(id)
	if it == nil {
		return nil, errors.New("unknown item")
	}
	faces := l.ItemFaces(it)
	if idx < 0 || idx >= len(faces) {
		return nil, errors.New("unknown face")
	}
	f := faces[idx]
	if f.Emb == nil {
		return []Suggestion{}, nil
	}
	ref := FaceRef{id, idx}
	l.people.mu.RLock()
	label := l.people.labels[ref.key()]
	l.people.mu.RUnlock()
	return l.suggest(f.Emb, frame{id, f.T}, ref, label), nil
}

// takeSuggested hands AddFace the embedding found for the same box shortly
// before (ok false: none, the face service is asked in the background).
func (l *Library) takeSuggested(it *Item, box FaceBox, t float64) (emb []float32, ok bool) {
	c, found := l.suggestEmb.LoadAndDelete(suggestKey(it.ID, box, t))
	if !found {
		return nil, false
	}
	s := c.(suggestedEmb)
	if s.it != it || !s.found || time.Since(s.at) > suggestTTL {
		return nil, false
	}
	return s.emb, true
}

func (l *Library) pruneSuggested() {
	l.suggestEmb.Range(func(k, v any) bool {
		if time.Since(v.(suggestedEmb).at) > suggestTTL {
			l.suggestEmb.Delete(k)
		}
		return true
	})
}

// suggest ranks the named people by likeness to emb, best first. self is
// the face itself (left out of the neighbours), fr its picture (names
// already there are left out), label its label (names it is not are left
// out).
func (l *Library) suggest(emb []float32, fr frame, self FaceRef, label string) []Suggestion {
	emb = normalize(emb)
	type cand struct {
		id, name string
		cent     []float32
		refs     []FaceRef
	}
	s := l.people
	s.mu.RLock()
	var cands []cand
	here := map[string]bool{} // names on other faces of the same picture
	for _, p := range s.people {
		if p.Name == "" {
			continue
		}
		cands = append(cands, cand{p.ID, p.Name, p.cent, p.refs})
	}
	s.mu.RUnlock()

	l.mu.RLock()
	it := l.byID[fr.item]
	var itemFaces []Face
	if it != nil {
		itemFaces = it.Faces
	}
	member := func(r FaceRef) []float32 {
		it := l.byID[r.Item]
		if it == nil || r.Idx >= len(it.Faces) {
			return nil
		}
		return it.Faces[r.Idx].Emb
	}
	out := []Suggestion{}
	for _, c := range cands {
		var best []float32 // the k highest similarities to the person's faces
		for _, r := range c.refs {
			if r.Item == fr.item {
				if r != self && r.Idx < len(itemFaces) && sameFrame(itemFaces[r.Idx], Face{T: fr.t}) {
					here[c.name] = true
				}
				if r == self {
					continue
				}
			}
			e := member(r)
			if len(e) != len(emb) {
				continue
			}
			v := dot(emb, e) // stored normalized
			if len(best) < suggestKNN {
				best = append(best, v)
				sort.Slice(best, func(a, b int) bool { return best[a] > best[b] })
			} else if v > best[len(best)-1] {
				best[len(best)-1] = v
				sort.Slice(best, func(a, b int) bool { return best[a] > best[b] })
			}
		}
		score := float32(-2)
		if len(c.cent) == len(emb) {
			score = dot(emb, c.cent)
		}
		if len(best) > 0 {
			var sum float32
			for _, v := range best {
				sum += v
			}
			// one face alone is a weaker hint than several
			knn := sum / float32(len(best))
			if len(best) < suggestKNN {
				knn -= 0.03 * float32(suggestKNN-len(best))
			}
			score = max(score, knn)
		}
		if score < -1 || here[c.name] || rejects(label, c.name) {
			continue
		}
		out = append(out, Suggestion{Name: c.name, Person: c.id, Score: score})
	}
	l.mu.RUnlock()

	named := s.threshNamed
	kept := out[:0]
	for _, sg := range out {
		switch {
		case sg.Score >= named+suggestAbove:
			sg.Level = "high"
		case sg.Score >= named:
			sg.Level = "mid"
		case sg.Score >= named-suggestBelow:
			sg.Level = "low"
		default:
			continue
		}
		kept = append(kept, sg)
	}
	sort.SliceStable(kept, func(a, b int) bool {
		if kept[a].Score != kept[b].Score {
			return kept[a].Score > kept[b].Score
		}
		return strings.ToLower(kept[a].Name) < strings.ToLower(kept[b].Name)
	})
	if len(kept) > suggestMax {
		kept = kept[:suggestMax]
	}
	return kept
}
