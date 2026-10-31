package gallery

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Person is a group of faces: named by the user or found by clustering.
type Person struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Count  int       `json:"count"` // photos
	Faces  int       `json:"faces"`
	Cover  FaceRef   `json:"cover"`
	Items  []string  `json:"-"`
	refs   []FaceRef // all faces
	cent   []float32
	itemSt map[string]bool
}

// FaceRef points at one face of one item.
type FaceRef struct {
	Item string `json:"item"`
	Idx  int    `json:"idx"`
}

func (r FaceRef) key() string { return fmt.Sprintf("%s:%d", r.Item, r.Idx) }

const (
	labelHidden = "~"
	threshNamed = 0.42 // cosine similarity to a named person's centroid
	threshNew   = 0.48 // to an unnamed cluster; children look alike, rather split than mix
	minCluster  = 3
)

type peopleStore struct {
	path string

	mu     sync.RWMutex
	labels map[string]string // face key -> name, "!name" (not this person) or "~" (hidden)
	people []*Person
	byID   map[string]*Person
	byFace map[string]string // face key -> person id

	clusterMu sync.Mutex
}

func newPeopleStore(path string) *peopleStore {
	s := &peopleStore{path: path, labels: map[string]string{}, byID: map[string]*Person{}, byFace: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		var f struct {
			Labels map[string]string `json:"labels"`
		}
		if json.Unmarshal(b, &f) == nil && f.Labels != nil {
			s.labels = f.Labels
		}
	}
	return s
}

func (s *peopleStore) save() error {
	s.mu.RLock()
	b, err := json.MarshalIndent(map[string]any{"labels": s.labels}, "", " ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b)
}

func (s *peopleStore) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.people)
}

// People returns all people, named first.
func (l *Library) People() []*Person {
	l.people.mu.RLock()
	defer l.people.mu.RUnlock()
	return l.people.people
}

// Person returns one person.
func (l *Library) Person(id string) *Person {
	l.people.mu.RLock()
	defer l.people.mu.RUnlock()
	return l.people.byID[id]
}

// PersonOf returns the person id of a face ("" if none).
func (l *Library) PersonOf(item string, idx int) string {
	l.people.mu.RLock()
	defer l.people.mu.RUnlock()
	return l.people.byFace[FaceRef{item, idx}.key()]
}

// PersonItems returns the item ids of a person, newest first.
func (l *Library) PersonItems(id string) []string {
	l.people.mu.RLock()
	defer l.people.mu.RUnlock()
	if p := l.people.byID[id]; p != nil {
		return p.Items
	}
	return nil
}

// NamePerson names (or renames) a person; using an existing name merges.
// An empty name removes the names of its faces; "~" hides the person.
func (l *Library) NamePerson(id, name string) error {
	name = strings.TrimSpace(name)
	s := l.people
	s.mu.Lock()
	p := s.byID[id]
	if p == nil {
		s.mu.Unlock()
		return fmt.Errorf("unknown person %q", id)
	}
	for _, r := range p.refs {
		k := r.key()
		if name == "" {
			delete(s.labels, k)
		} else {
			s.labels[k] = name
		}
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return err
	}
	l.recluster()
	l.bump()
	return nil
}

// LabelFace sets the name of one face. "" removes it from its person
// (remembered as "not this person"), "~" hides the face.
func (l *Library) LabelFace(item string, idx int, name string) error {
	s := l.people
	k := FaceRef{item, idx}.key()
	s.mu.Lock()
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		cur := s.labels[k]
		pid := s.byFace[k]
		if cur == "" || strings.HasPrefix(cur, "!") {
			if p := s.byID[pid]; p != nil && p.Name != "" {
				cur = p.Name
			}
		}
		if cur != "" && cur != labelHidden && !strings.HasPrefix(cur, "!") {
			s.labels[k] = "!" + cur
		} else {
			s.labels[k] = labelHidden
		}
	default:
		s.labels[k] = name
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return err
	}
	l.recluster()
	l.bump()
	return nil
}

func dot(a, b []float32) float32 {
	var s0, s1, s2, s3 float32
	n := min(len(a), len(b))
	i := 0
	for ; i+4 <= n; i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < n; i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3
}

func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x * x)
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	if n == 0 {
		return out
	}
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

type faceRec struct {
	ref     FaceRef
	emb     []float32
	quality float32
	taken   int64
	label   string
}

type cluster struct {
	sum  []float32
	cent []float32
	refs []*faceRec
	name string
}

func (c *cluster) add(f *faceRec) {
	if c.sum == nil {
		c.sum = make([]float32, len(f.emb))
	}
	for i, x := range f.emb {
		c.sum[i] += x
	}
	c.cent = normalize(c.sum)
	c.refs = append(c.refs, f)
}

type faceSnap struct {
	id    string
	taken int64
	faces []Face
}

// recluster rebuilds the people from a consistent snapshot of the faces.
func (l *Library) recluster() {
	items := l.Items()
	snap := make([]faceSnap, 0, len(items))
	l.mu.RLock()
	for _, it := range items {
		if it.Kind == Photo && len(it.Faces) > 0 {
			snap = append(snap, faceSnap{it.ID, it.Taken, it.Faces})
		}
	}
	l.mu.RUnlock()
	l.people.cluster(snap)
}

// cluster rebuilds the people from labels and face embeddings.
func (s *peopleStore) cluster(items []faceSnap) {
	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()

	s.mu.RLock()
	labels := make(map[string]string, len(s.labels))
	for k, v := range s.labels {
		labels[k] = v
	}
	s.mu.RUnlock()

	taken := map[string]int64{}
	var all []*faceRec
	for _, it := range items {
		taken[it.id] = it.taken
		for i, f := range it.faces {
			if f.Emb == nil {
				continue
			}
			r := &faceRec{ref: FaceRef{it.id, i}, emb: f.Emb, quality: f.Score * float32(math.Sqrt(float64(f.W*f.H))), taken: it.taken}
			r.label = labels[r.ref.key()]
			all = append(all, r)
		}
	}

	// 1. named people from labels
	named := map[string]*cluster{}
	var rest []*faceRec
	for _, f := range all {
		switch {
		case f.label == labelHidden:
		case f.label != "" && !strings.HasPrefix(f.label, "!"):
			c := named[f.label]
			if c == nil {
				c = &cluster{name: f.label}
				named[f.label] = c
			}
			c.add(f)
		default:
			rest = append(rest, f)
		}
	}
	// 2. unlabeled faces join a named person when close enough
	var namedList []*cluster
	for _, c := range named {
		namedList = append(namedList, c)
	}
	sort.Slice(namedList, func(a, b int) bool { return namedList[a].name < namedList[b].name })
	var pool []*faceRec
	if len(namedList) > 0 {
		best := bestMatches(rest, namedList)
		for i, f := range rest {
			c, sim := best[i].c, best[i].sim
			if c != nil && sim >= threshNamed && f.label != "!"+c.name {
				c.refs = append(c.refs, f) // centroid stays the user's choice
			} else {
				pool = append(pool, f)
			}
		}
	} else {
		pool = rest
	}
	// 3. leader clustering of the rest, best faces first
	sort.Slice(pool, func(a, b int) bool { return pool[a].quality > pool[b].quality })
	var clusters []*cluster
	const chunk = 512
	for start := 0; start < len(pool); start += chunk {
		part := pool[start:min(start+chunk, len(pool))]
		best := bestMatches(part, clusters)
		before := len(clusters)
		for i, f := range part {
			c, sim := best[i].c, best[i].sim
			for _, nc := range clusters[before:] { // clusters made within this chunk
				if s := dot(f.emb, nc.cent); s > sim {
					c, sim = nc, s
				}
			}
			if c != nil && sim >= threshNew {
				c.add(f)
			} else {
				nc := &cluster{}
				nc.add(f)
				clusters = append(clusters, nc)
			}
		}
	}

	// build people
	var people []*Person
	mk := func(c *cluster, id, name string) {
		items := map[string]bool{}
		var cover *faceRec
		for _, f := range c.refs {
			items[f.ref.Item] = true
			if cover == nil || f.quality > cover.quality || (f.label == name && name != "" && cover.label != name) {
				cover = f
			}
		}
		if name == "" && (len(c.refs) < minCluster || len(items) < 2) {
			return
		}
		p := &Person{ID: id, Name: name, Faces: len(c.refs), Count: len(items), Cover: cover.ref, cent: c.cent, itemSt: items}
		for _, f := range c.refs {
			p.refs = append(p.refs, f.ref)
		}
		for it := range items {
			p.Items = append(p.Items, it)
		}
		sort.Slice(p.Items, func(a, b int) bool { return taken[p.Items[a]] > taken[p.Items[b]] })
		people = append(people, p)
	}
	for _, c := range namedList {
		mk(c, "n-"+slug(c.name), c.name)
	}
	for _, c := range clusters {
		mk(c, "c-"+strings.ReplaceAll(c.refs[0].ref.key(), ":", "-"), "")
	}
	sort.SliceStable(people, func(a, b int) bool {
		if (people[a].Name != "") != (people[b].Name != "") {
			return people[a].Name != ""
		}
		return people[a].Count > people[b].Count
	})
	byID := map[string]*Person{}
	byFace := map[string]string{}
	for _, p := range people {
		byID[p.ID] = p
		for _, r := range p.refs {
			byFace[r.key()] = p.ID
		}
	}
	s.mu.Lock()
	s.people, s.byID, s.byFace = people, byID, byFace
	s.mu.Unlock()
}

type match struct {
	c   *cluster
	sim float32
}

// bestMatches finds the closest cluster centroid per face, in parallel.
func bestMatches(faces []*faceRec, clusters []*cluster) []match {
	out := make([]match, len(faces))
	if len(clusters) == 0 {
		return out
	}
	var wg sync.WaitGroup
	n := runtime.NumCPU()
	step := (len(faces) + n - 1) / n
	for start := 0; start < len(faces); start += step {
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				m := match{sim: -2}
				for _, c := range clusters {
					if s := dot(faces[i].emb, c.cent); s > m.sim {
						m = match{c, s}
					}
				}
				out[i] = m
			}
		}(start, min(start+step, len(faces)))
	}
	wg.Wait()
	return out
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		default:
			fmt.Fprintf(&b, "%x", r)
		}
	}
	return b.String()
}
