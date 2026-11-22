package gallery

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
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
	// labelNoFace: not a face at all (a pattern, a toy, a sticker the
	// detector took for one): left out everywhere, never shown again
	labelNoFace = "-"
	minCluster  = 3
	// rejectSep parts the names of "!A\x1fB": a face that is neither A nor B
	rejectSep = "\x1f"
)

// rejects tells whether a face's label says it is not that person.
func rejects(label, name string) bool {
	if !strings.HasPrefix(label, "!") || name == "" {
		return false
	}
	for _, n := range strings.Split(label[1:], rejectSep) {
		if n == name {
			return true
		}
	}
	return false
}

// withReject adds a name to the names a face is not (label: "" or "!…").
func withReject(label, name string) string {
	if rejects(label, name) {
		return label
	}
	if !strings.HasPrefix(label, "!") || len(label) == 1 {
		return "!" + name
	}
	return label + rejectSep + name
}

type peopleStore struct {
	path string
	// cosine similarity needed to join an unnamed cluster (children look
	// alike: rather split than mix) and a named person's centroid
	threshNew, threshNamed float32
	// similarity a second face needs to join a person or group that already
	// has a face in the same picture (a collage of one person)
	threshSame float32

	mu     sync.RWMutex
	labels map[string]string // face key -> name, "!name" (not this person), "~" (hidden) or "-" (no face)
	people []*Person
	byID   map[string]*Person
	byFace map[string]string // face key -> person id
	hidden []hiddenRec       // groups hidden with "Ausblenden", newest last

	clusterMu sync.Mutex
	apart     int // faces the last clustering kept away from a person already in their picture
}

func newPeopleStore(path string, threshNew, threshNamed, threshSame float64) *peopleStore {
	s := &peopleStore{path: path, threshNew: float32(threshNew), threshNamed: float32(threshNamed), threshSame: float32(threshSame),
		labels: map[string]string{}, byID: map[string]*Person{}, byFace: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		var f struct {
			Labels map[string]string `json:"labels"`
			Hidden []hiddenRec       `json:"hidden"`
		}
		if json.Unmarshal(b, &f) == nil && f.Labels != nil {
			s.labels = f.Labels
			s.hidden = f.Hidden
		}
	}
	return s
}

func (s *peopleStore) save() error {
	s.mu.RLock()
	f := map[string]any{"labels": s.labels}
	if len(s.hidden) > 0 {
		f["hidden"] = s.hidden
	}
	b, err := json.MarshalIndent(f, "", " ")
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

// PendingName is a name given to a face that no person has yet.
type PendingName struct {
	Name  string  `json:"name"`
	Cover FaceRef `json:"cover"` // one of its faces
}

// PendingNames lists the names given to faces of items there that no person
// has yet: grouping runs behind (on a small machine for a while), and a name
// just given is offered meanwhile. A name undone or given another name is
// not in it any more.
func (l *Library) PendingNames() []PendingName {
	s := l.people
	s.mu.RLock()
	have := make(map[string]bool, len(s.people))
	for _, p := range s.people {
		if p.Name != "" {
			have[p.Name] = true
		}
	}
	cands := map[string]string{} // face key -> name
	for k, v := range s.labels {
		if !have[v] && approvedLabel(v) && v != labelHidden && v != labelNoFace {
			cands[k] = v
		}
	}
	s.mu.RUnlock()
	if len(cands) == 0 {
		return nil
	}
	byName := map[string]FaceRef{}
	l.mu.RLock()
	for k, v := range cands {
		i := strings.LastIndexByte(k, ':')
		if i < 0 {
			continue
		}
		idx, err := strconv.Atoi(k[i+1:])
		if err != nil {
			continue
		}
		ref := FaceRef{k[:i], idx}
		if it := l.byID[ref.Item]; it == nil || idx >= len(it.Faces) {
			continue // a label left of an item gone
		}
		if old, ok := byName[v]; !ok || ref.key() < old.key() { // the same face each time
			byName[v] = ref
		}
	}
	l.mu.RUnlock()
	out := make([]PendingName, 0, len(byName))
	for name, ref := range byName {
		out = append(out, PendingName{name, ref})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Person returns one person.
func (l *Library) Person(id string) *Person {
	l.people.mu.RLock()
	defer l.people.mu.RUnlock()
	return l.people.byID[id]
}

// FacePerson is the person a face is shown as: the name given to it by
// hand at once (before grouping again caught up), else its group. id may
// be one grouping makes only on its next run.
func (l *Library) FacePerson(item string, idx int) (id, name string) {
	s := l.people
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := FaceRef{item, idx}.key()
	label := s.labels[k]
	id = s.byFace[k]
	p := s.byID[id]
	switch {
	case label == labelHidden || label == labelNoFace:
		return "", ""
	case label != "" && !strings.HasPrefix(label, "!"):
		if p != nil && p.Name == label {
			return id, label
		}
		return "n-" + slug(label), label
	case rejects(label, func() string {
		if p != nil {
			return p.Name
		}
		return ""
	}()):
		return "", "" // said not to be the person it is still grouped with
	case p != nil:
		return id, p.Name
	}
	return id, ""
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
	l.promoteNow.Store(true)
	name = strings.TrimSpace(name)
	if name == labelHidden {
		_, err := l.HidePerson(id)
		return err
	}
	s := l.people
	s.mu.Lock()
	p := s.byID[id]
	if p == nil {
		s.mu.Unlock()
		return fmt.Errorf("unknown person %q", id)
	}
	for _, r := range p.refs {
		k := r.key()
		switch {
		case strings.HasPrefix(s.labels[k], "!") && (name == "" || rejects(s.labels[k], name)):
			// a face said not to be this name keeps saying so (and does not join it)
		case name == "":
			delete(s.labels, k)
		default:
			s.labels[k] = name
		}
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return err
	}
	l.recluster() // the page reloads the people right after: they must be there
	l.bump()
	return nil
}

// LabelFace sets the name of one face. "" removes it from its person
// (remembered as "not this person"), "~" hides the face, "-" marks it as
// no face at all. It returns the label the face had before (for undo).
func (l *Library) LabelFace(item string, idx int, name string) (string, error) {
	l.promoteNow.Store(true)
	s := l.people
	k := FaceRef{item, idx}.key()
	t0 := time.Now()
	s.mu.Lock()
	tLock := time.Since(t0)
	prev := s.labels[k]
	s.setLabel(k, strings.TrimSpace(name))
	s.mu.Unlock()
	l.notTwin(item, idx) // somebody decided: no automatic putting aside to undo any more
	t1 := time.Now()
	if err := s.save(); err != nil {
		return prev, err
	}
	t2 := time.Now()
	l.regroup()
	l.logSlowLabel("label", tLock, t2.Sub(t1), time.Since(t2))
	return prev, nil
}

// logSlowLabel logs where a slow label waited: for the people lock, saving
// the labels, grouping again (only right here when not in the background).
func (l *Library) logSlowLabel(what string, lock, save, regroup time.Duration) {
	if lock+save+regroup >= time.Second {
		r := func(d time.Duration) time.Duration { return d.Round(10 * time.Millisecond) }
		l.logf("slow face %s: lock %s, save %s, regroup %s", what, r(lock), r(save), r(regroup))
	}
}

// setLabel labels one face (see LabelFace); the caller holds s.mu. "Not
// this person" remembers the name the face has now, together with the names
// it was said not to be before, so the next clustering lets it join the next
// most alike person instead, and never one of those; a face without a named
// person is hidden.
func (s *peopleStore) setLabel(k, name string) {
	switch {
	case name == labelNoFace:
		s.labels[k] = labelNoFace
	case name == "":
		cur := s.labels[k]
		before := ""
		if strings.HasPrefix(cur, "!") {
			before, cur = cur, ""
		}
		if cur == "" {
			if p := s.byID[s.byFace[k]]; p != nil && p.Name != "" {
				cur = p.Name
			}
		}
		if cur != "" && cur != labelHidden && cur != labelNoFace {
			s.labels[k] = withReject(before, cur)
		} else {
			s.labels[k] = labelHidden
		}
	default:
		s.labels[k] = name
	}
}

// SelPerson is a person (or unnamed group) with faces in a set of items.
type SelPerson struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Faces int     `json:"faces"` // faces of the person in the items
	Items int     `json:"items"` // items the person is in
	Cover FaceRef `json:"cover"`
}

// PeopleIn lists the people with faces in the given items: named ones
// first, then by the number of items.
func (l *Library) PeopleIn(ids []string) []SelPerson { return l.people.peopleIn(ids) }

func (s *peopleStore) peopleIn(ids []string) []SelPerson {
	in := make(map[string]bool, len(ids))
	for _, id := range ids {
		in[id] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []SelPerson{}
	for _, p := range s.people {
		sp := SelPerson{ID: p.ID, Name: p.Name, Cover: p.Cover}
		seen := map[string]bool{}
		for _, r := range p.refs {
			if !in[r.Item] {
				continue
			}
			sp.Faces++
			if !seen[r.Item] {
				seen[r.Item] = true
				sp.Items++
			}
		}
		if sp.Faces > 0 {
			out = append(out, sp)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if (out[a].Name == "") != (out[b].Name == "") {
			return out[a].Name != ""
		}
		if out[a].Items != out[b].Items {
			return out[a].Items > out[b].Items
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// LabelPersonIn labels the faces of one person in the given items like
// LabelFace: a name, "" not this person, "-" no face, "~" hidden. It
// returns the labels the faces had before (for RestoreFaceLabels); their
// number is the number of faces changed.
func (l *Library) LabelPersonIn(ids []string, person, name string) (map[string]string, error) {
	l.promoteNow.Store(true)
	prev, err := l.people.labelPersonIn(ids, person, strings.TrimSpace(name))
	if err != nil || len(prev) == 0 {
		return prev, err
	}
	if err := l.people.save(); err != nil {
		return prev, err
	}
	l.recluster() // the page reloads the people right after: they must be there
	l.bump()
	return prev, nil
}

func (s *peopleStore) labelPersonIn(ids []string, person, name string) (map[string]string, error) {
	in := make(map[string]bool, len(ids))
	for _, id := range ids {
		in[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byID[person]
	if p == nil {
		return nil, fmt.Errorf("unknown person %q", person)
	}
	prev := map[string]string{}
	for _, r := range p.refs {
		if !in[r.Item] {
			continue
		}
		k := r.key()
		if rejects(s.labels[k], strings.TrimSpace(name)) {
			continue // said not to be that person: stays apart
		}
		prev[k] = s.labels[k]
		s.setLabel(k, name)
	}
	return prev, nil
}

// NoFacePerson marks every face of a person as no face (a group of
// misdetections). It returns the labels the faces had before, for
// RestoreFaceLabels.
func (l *Library) NoFacePerson(id string) (map[string]string, error) {
	s := l.people
	s.mu.Lock()
	p := s.byID[id]
	if p == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("unknown person %q", id)
	}
	prev := map[string]string{}
	for _, r := range p.refs {
		k := r.key()
		prev[k] = s.labels[k]
		s.labels[k] = labelNoFace
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return prev, err
	}
	l.recluster() // the page reloads the people right after: they must be there
	l.bump()
	return prev, nil
}

// RestoreFaceLabels sets the labels of faces back (undo of NoFacePerson
// and LabelFace): "" removes a label. Keys are "item:index" of faces that
// exist.
func (l *Library) RestoreFaceLabels(labels map[string]string) error {
	if err := l.checkFaceKeys(labels); err != nil {
		return err
	}
	for k, v := range labels { // a label set back by somebody: no automatic putting aside any more
		if strings.TrimSpace(v) != labelNoFace {
			if id, rest, ok := strings.Cut(k, ":"); ok {
				if i, err := strconv.Atoi(rest); err == nil {
					l.notTwin(id, i)
				}
			}
		}
	}
	s := l.people
	s.mu.Lock()
	for k, v := range labels {
		if v = strings.TrimSpace(v); v == "" {
			delete(s.labels, k)
		} else {
			s.labels[k] = v
		}
	}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return err
	}
	l.regroup()
	return nil
}

// labeled returns the indexes of an item's faces that carry a label (settled:
// only labels that settle a face, see approvedLabel; else every label, which
// keeps a face at its index when faces are found again).
func (s *peopleStore) labeled(item string, settled bool) map[int]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out map[int]bool
	for k, v := range s.labels {
		rest, ok := strings.CutPrefix(k, item+":")
		if !ok || (settled && !approvedLabel(v)) {
			continue
		}
		if i, err := strconv.Atoi(rest); err == nil {
			if out == nil {
				out = map[int]bool{}
			}
			out[i] = true
		}
	}
	return out
}

// setNoFace marks faces of an item as no face (faces a new search did not
// find again); the caller saves. A face labeled meanwhile keeps its label.
func (s *peopleStore) setNoFace(item string, idx []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range idx {
		if k := (FaceRef{item, i}).key(); s.labels[k] == "" {
			s.labels[k] = labelNoFace
		}
	}
}

// labelOf returns a face's label (key "item:idx"); the caller may hold l.mu.
func (s *peopleStore) labelOf(k string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.labels[k]
}

// clearNoFace removes the no face labels of faces of an item (put aside
// automatically, coming back); the caller may hold l.mu.
func (s *peopleStore) clearNoFace(item string, idx []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range idx {
		if k := (FaceRef{item, i}).key(); s.labels[k] == labelNoFace {
			delete(s.labels, k)
		}
	}
}

// setNoFaceKeys puts faces (keys "item:idx") aside as no face, those
// without a label; the caller may hold l.mu. It tells whether any changed.
func (s *peopleStore) setNoFaceKeys(keys []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range keys {
		if s.labels[k] == "" {
			s.labels[k] = labelNoFace
			n++
		}
	}
	return n > 0
}

// clearNoFaceKeys removes the no face labels of faces (keys "item:idx");
// the caller may hold l.mu. It tells whether any went (to be saved).
func (s *peopleStore) clearNoFaceKeys(keys []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range keys {
		if s.labels[k] == labelNoFace {
			delete(s.labels, k)
			n++
		}
	}
	return n > 0
}

// checkFaceKeys tells whether every key ("item:idx") is a face there is.
func (l *Library) checkFaceKeys(keys map[string]string) error {
	for k := range keys {
		item, idx, ok := strings.Cut(k, ":")
		n, err := strconv.Atoi(idx)
		it := l.Get(item)
		if !ok || err != nil || it == nil || n < 0 || n >= len(l.ItemFaces(it)) {
			return fmt.Errorf("unknown face %q", k)
		}
	}
	return nil
}

// approvedLabel tells whether a label settles a face: a name, hidden or no
// face. "Not this person" (!name) does not: who it is stays open.
func approvedLabel(label string) bool { return label != "" && !strings.HasPrefix(label, "!") }

// labeledAll returns labeled for every item with labels (settled: only
// labels that settle a face, for approval).
func (s *peopleStore) labeledAll(settled bool) map[string]map[int]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]map[int]bool{}
	for k, v := range s.labels {
		item, rest, ok := strings.Cut(k, ":")
		if !ok || (settled && !approvedLabel(v)) {
			continue
		}
		if i, err := strconv.Atoi(rest); err == nil {
			if out[item] == nil {
				out[item] = map[int]bool{}
			}
			out[item][i] = true
		}
	}
	return out
}

// IsNoFace tells whether a face was marked as no face.
func (l *Library) IsNoFace(item string, idx int) bool {
	l.people.mu.RLock()
	defer l.people.mu.RUnlock()
	return l.people.labels[FaceRef{item, idx}.key()] == labelNoFace
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
	frame   frame
	weak    bool      // the detector was unsure or the face is small: joins a named person only when clearly alike
	emb2    []float32 // the second system's embedding, unit length (nil: none)
	hint    string    // taken in from the second search for this person (Face.Hint)
}

// Weak faces: measured on a real library, faces the detector found with a
// score below 0.90 or with a short side under 80 px (in the picture as it
// is, not as scaled for detection) were named wrong about
// twice as often; they join a named person only weakExtra above
// named_threshold (left out, they form groups of their own to be named).
const (
	weakScore = 0.90
	weakPx    = 80
	weakExtra = 0.12
	// a person is matched by the better of its centroid and the mean of its
	// knnK most alike named faces (faces change over the years: the
	// centroid of a child lies between its ages). knnFar: a person whose
	// centroid is more than this below the threshold is not looked at (on a
	// real library 0.2 lost many matches of nearest faces, 0.4 two of 8,670)
	knnK   = 3
	knnFar = 0.4
)

// frame is one picture: a photo, or one moment of a video. A person is in a
// frame once, except in a collage.
type frame struct {
	item string
	t    float32
}

type cluster struct {
	sum    []float32
	cent   []float32
	refs   []*faceRec
	name   string
	frames map[frame]bool
}

// join makes f a member without a say in the centroid.
func (c *cluster) join(f *faceRec) {
	c.refs = append(c.refs, f)
	if c.frames == nil {
		c.frames = map[frame]bool{}
	}
	c.frames[f.frame] = true
}

func (c *cluster) add(f *faceRec) {
	c.join(f)
	if len(f.emb) == 0 {
		return // named by hand without an embedding: a member, no say in the centroid
	}
	if len(c.sum) == 0 {
		c.sum = make([]float32, len(f.emb))
	}
	for i, x := range f.emb {
		if i < len(c.sum) {
			c.sum[i] += x
		}
	}
	c.cent = normalize(c.sum)
}

type faceSnap struct {
	id    string
	taken int64
	faces []Face
	w, h  int // the picture's pixel size (0: unknown)
}

// recluster rebuilds the people from a consistent snapshot of the faces.
// reclusterSoon groups the faces again in the background; calls while
// one runs make one more run after it.
func (l *Library) reclusterSoon() {
	if l.reclusterQueued.Swap(true) {
		return
	}
	l.Go(func() { // Close waits for it (its changes go into the last save)
		l.reclusterRun.Lock()
		defer l.reclusterRun.Unlock()
		l.reclusterQueued.Store(false)
		if l.closing.Load() {
			return // grouped again at the next start
		}
		l.recluster()
		l.bump()
	})
}

// regroup groups the faces again after somebody named a face: right here
// (tools, tests), or, while the library serves, in the background (it
// takes long on a small machine; the name given counts at once, see
// FacePerson). Changes to whole groups regroup right away (the page reloads
// the people at once).
func (l *Library) regroup() {
	if l.opt.AsyncRegroup {
		l.reclusterSoon()
		return
	}
	l.recluster()
	l.bump()
}

func (l *Library) recluster() {
	l.dropDuplicateFaces() // a face found twice counts once
	if apart, changed := l.people.cluster(l.faceSnapshot()); changed {
		l.logf("faces: %d faces kept apart from a person already in their picture (same_photo_threshold %.2f)", apart, l.people.threshSame)
	}
	// faces of the second search whose person is known now join it (right
	// away after somebody named a face)
	if l.promoteExtras(l.promoteNow.Swap(false)) {
		l.dropDuplicateFaces() // faces taken in that are there already
		l.people.cluster(l.faceSnapshot())
	}
	l.kickXMP() // names go into the files
}

// faceSnapshot lists the photos with faces.
func (l *Library) faceSnapshot() []faceSnap {
	items := l.Items()
	snap := make([]faceSnap, 0, len(items))
	l.mu.RLock()
	for _, it := range items {
		if len(it.Faces) > 0 { // photos, and videos with faces marked by hand
			snap = append(snap, faceSnap{it.ID, it.Taken, it.Faces, it.W, it.H})
		}
	}
	l.mu.RUnlock()
	return snap
}

// cluster rebuilds the people from labels and face embeddings. It returns
// how many faces were kept away from a person already in their picture,
// and whether that number changed since the last time (worth a log line).
func (s *peopleStore) cluster(items []faceSnap) (apart int, changed bool) {
	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()

	s.mu.RLock()
	labels := make(map[string]string, len(s.labels))
	for k, v := range s.labels {
		labels[k] = v
	}
	s.mu.RUnlock()

	people, apart := buildPeople(items, labels, s.threshNew, s.threshNamed, s.threshSame, false)
	changed = apart != s.apart
	s.apart = apart
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
	return apart, changed
}

// faceRecs lists the faces with embeddings and their labels.
func faceRecs(items []faceSnap, labels map[string]string) (map[string]int64, []*faceRec) {
	taken := map[string]int64{}
	var all []*faceRec
	for _, it := range items {
		taken[it.id] = it.taken
		for i, f := range it.faces {
			label := labels[FaceRef{it.id, i}.key()]
			// without an embedding only a face named by hand counts (it
			// belongs to its person, but cannot be compared)
			// (hidden too, so it can be shown again with its group); a face
			// marked by hand always takes part (it cannot join anybody by
			// likeness, but can be listed and shown again)
			if f.Emb == nil && f.Emb2 == nil && !f.Manual && (label == "" || label == labelNoFace || strings.HasPrefix(label, "!")) {
				continue
			}
			r := &faceRec{ref: FaceRef{it.id, i}, emb: f.Emb, quality: f.Score * float32(math.Sqrt(float64(f.W*f.H))), taken: it.taken,
				frame: frame{it.id, f.T}, emb2: unit2(f.Emb2), hint: f.Hint}
			if !f.Manual {
				short := float32(-1)
				if it.w > 0 && it.h > 0 {
					short = min(f.W*float32(it.w), f.H*float32(it.h))
				}
				r.weak = f.Score < weakScore || (short >= 0 && short < weakPx)
			}
			r.label = label
			all = append(all, r)
		}
	}
	return taken, all
}

// newPerson makes a person from faces; the best face (or one the user named) is the cover.
func newPerson(refs []*faceRec, id, name string, taken map[string]int64) *Person {
	if len(refs) == 0 {
		return nil
	}
	items := map[string]bool{}
	var cover *faceRec
	for _, f := range refs {
		items[f.ref.Item] = true
		if cover == nil || f.quality > cover.quality || (f.label == name && name != "" && cover.label != name) {
			cover = f
		}
	}
	p := &Person{ID: id, Name: name, Faces: len(refs), Count: len(items), Cover: cover.ref, itemSt: items}
	for _, f := range refs {
		p.refs = append(p.refs, f.ref)
	}
	for it := range items {
		p.Items = append(p.Items, it)
	}
	sort.Slice(p.Items, func(a, b int) bool { return taken[p.Items[a]] > taken[p.Items[b]] })
	return p
}

// buildPeople groups the faces: named people from labels, the rest by
// similarity. Unnamed groups smaller than minCluster faces or with only one
// photo are left out unless keepSmall is set.
//
// A person is in a picture (a photo, a moment of a video) once: a face
// joins a person or group that already has a face of its picture only when
// at least threshSame alike (a collage), and else goes on to the next best
// one. Faces named by hand always stay with their name. apart counts the
// faces this kept away from their closest person or group.
func buildPeople(items []faceSnap, labels map[string]string, threshNew, threshNamed, threshSame float32, keepSmall bool) (people []*Person, apart int) {
	taken, all := faceRecs(items, labels)

	// 1. named people from labels
	named := map[string]*cluster{}
	var rest []*faceRec
	for _, f := range all {
		switch {
		case f.label == labelHidden, f.label == labelNoFace:
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
	// 2. unlabeled faces join a named person when close enough. The most
	// alike pairs go first, so of several faces of one picture that are like
	// one person the most alike gets that person and the others try the next.
	var namedList []*cluster
	for _, c := range named {
		namedList = append(namedList, c)
	}
	sort.Slice(namedList, func(a, b int) bool { return namedList[a].name < namedList[b].name })
	var pool []*faceRec
	if len(namedList) > 0 {
		type pair struct {
			f   int
			c   *cluster
			sim float32
		}
		var pairs []pair
		// the named faces of each person, for the nearest-neighbour likeness
		// (of both systems)
		// (of both systems; all of them: nearest faces are what counts, a
		// sample of them named on a real library twice as many wrong)
		members := make(map[*cluster][][]float32, len(namedList))
		members2 := make(map[*cluster][][]float32, len(namedList))
		for _, c := range namedList {
			for _, r := range c.refs {
				if len(r.emb) > 0 {
					members[c] = append(members[c], r.emb)
				}
				if len(r.emb2) > 0 {
					members2[c] = append(members2[c], r.emb2)
				}
			}
		}
		cent2 := make(map[*cluster][]float32, len(namedList))
		for _, c := range namedList {
			if cv, n := centroid2(c.refs); n > 0 {
				cent2[c] = cv
			}
		}
		cands := candidates(rest, namedList, threshNamed-knnFar)
		per := make([][]pair, len(rest))
		var wg sync.WaitGroup
		step := max(1, (len(rest)+runtime.NumCPU()-1)/runtime.NumCPU())
		for lo := 0; lo < len(rest); lo += step {
			wg.Add(1)
			go func(lo, hi int) {
				defer wg.Done()
				for i := lo; i < hi; i++ {
					f := rest[i]
					// taken in from the second search for this person: joins it
					if c := named[f.hint]; c != nil && !rejects(f.label, f.hint) {
						per[i] = append(per[i], pair{i, c, 2})
						continue
					}
					// both systems where both know the face and the person (their
					// mean, and clearly ahead of the next person), else the one
					// that does
					type cand struct {
						c     *cluster
						sim   float32
						fused bool
					}
					var cs []cand
					score := func(c *cluster, s1 float32) cand {
						if len(f.emb2) > 0 && len(members2[c]) > 0 {
							return cand{c, fuse(s1, knnLikeness(f.emb2, members2[c])), true}
						}
						return cand{c, s1, false}
					}
					if len(f.emb) == 0 { // only the second system knows it (a box drawn by hand)
						if len(f.emb2) > 0 {
							for _, c := range namedList {
								cs = append(cs, score(c, -2))
							}
						}
					} else {
						// the people alike by their centroids, the most alike first;
						// only the knnTop best are looked at face by face (on a
						// real library the others never won)
						type pre struct {
							c      *cluster
							s1, by float32
						}
						var ps []pre
						seen := map[*cluster]bool{}
						add := func(c *cluster, s1 float32) {
							seen[c] = true
							if rejects(f.label, c.name) {
								return // said not to be: takes no place among the few looked at
							}
							by := s1
							if len(f.emb2) > 0 && len(cent2[c]) > 0 {
								by = fuse(s1, dot(f.emb2, cent2[c]))
							}
							ps = append(ps, pre{c, s1, by})
						}
						for _, m := range cands[i] {
							add(m.c, m.sim)
						}
						// people only the second system finds alike take part too
						if len(f.emb2) > 0 {
							for _, c := range namedList {
								if !seen[c] && len(cent2[c]) > 0 && dot(f.emb2, cent2[c]) >= threshNamed/on2-knnFar {
									s1 := float32(-1)
									if len(c.cent) == len(f.emb) {
										s1 = dot(f.emb, c.cent)
									}
									add(c, s1)
								}
							}
						}
						sort.Slice(ps, func(a, b int) bool { return ps[a].by > ps[b].by })
						if len(ps) > knnTop {
							ps = ps[:knnTop]
						}
						for _, x := range ps {
							cs = append(cs, score(x.c, max(x.s1, knnLikeness(f.emb, members[x.c]))))
						}
					}
					// people it was said not to be take no part (nor in the margin)
					kept := cs[:0]
					for _, x := range cs {
						if !rejects(f.label, x.c.name) {
							kept = append(kept, x)
						}
					}
					cs = kept
					sort.Slice(cs, func(a, b int) bool { return cs[a].sim > cs[b].sim })
					for k, x := range cs {
						need := threshNamed
						switch {
						case f.weak && x.fused:
							need += weakFused
						case f.weak:
							need += weakExtra
						}
						if x.sim < need {
							break // sorted: the rest are lower
						}
						if x.fused && k == 0 && len(cs) > 1 && x.sim-cs[1].sim < fusedMargin {
							break // two people about as alike: left to the user
						}
						per[i] = append(per[i], pair{i, x.c, x.sim})
					}
				}
			}(lo, min(lo+step, len(rest)))
		}
		wg.Wait()
		for _, ps := range per {
			pairs = append(pairs, ps...)
		}
		sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].sim > pairs[b].sim })
		placed := make([]bool, len(rest))
		kept := make([]bool, len(rest))
		for _, p := range pairs {
			f := rest[p.f]
			switch {
			case placed[p.f]:
			case p.c.frames[f.frame] && p.sim < threshSame:
				kept[p.f] = true
			default:
				p.c.join(f) // centroid stays the user's choice
				placed[p.f] = true
			}
		}
		for i, f := range rest {
			if kept[i] {
				apart++
			}
			if !placed[i] {
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
			if c != nil && c.frames[f.frame] {
				// the closest group has this picture already: its likeness is taken
				// again (the centroid may have moved within this chunk), and below
				// threshSame the closest group without the picture is looked for
				if sim = dot(f.emb, c.cent); sim < threshSame {
					if sim >= threshNew {
						apart++
					}
					c, sim = nil, -2
					for _, oc := range clusters {
						if s := dot(f.emb, oc.cent); s > sim && (!oc.frames[f.frame] || s >= threshSame) {
							c, sim = oc, s
						}
					}
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

	// 4. an unnamed group that both systems find clearly alike a named
	// person goes into it (two different systems seldom err alike)
	clusters = mergeIntoNamed(clusters, namedList)

	// build people
	mk := func(c *cluster, id, name string) {
		if p := newPerson(c.refs, id, name, taken); p != nil && (name != "" || keepSmall || (p.Faces >= minCluster && p.Count >= 2)) {
			p.cent = c.cent
			people = append(people, p)
		}
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
	return people, apart
}

// knnTop: the people, most alike by centroid, nearest-neighbour likeness
// looks at for one face (on a real library the others never won: the same
// faces named, 4x quicker). Fewer, or fewer faces per person, or the second
// system for fewer of them, named more faces wrong.
const knnTop = 4

// knnLikeness is the mean likeness of e to its knnK most alike faces of a
// person (fewer faces count a little less: one face alone is a weaker hint).
func knnLikeness(e []float32, faces [][]float32) float32 {
	var best [knnK]float32
	n := 0
	for _, x := range faces {
		if len(x) != len(e) {
			continue
		}
		v := dot(e, x)
		if n < knnK {
			best[n] = v
			n++
		} else {
			lo := 0
			for j := 1; j < knnK; j++ {
				if best[j] < best[lo] {
					lo = j
				}
			}
			if v <= best[lo] {
				continue
			}
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

type match struct {
	c   *cluster
	sim float32
}

// candidates lists per face the clusters at least that alike, in parallel.
func candidates(faces []*faceRec, clusters []*cluster, least float32) [][]match {
	out := make([][]match, len(faces))
	var wg sync.WaitGroup
	n := runtime.NumCPU()
	step := max(1, (len(faces)+n-1)/n)
	for start := 0; start < len(faces); start += step {
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				for _, c := range clusters {
					if s := dot(faces[i].emb, c.cent); s >= least {
						out[i] = append(out[i], match{c, s})
					}
				}
			}
		}(start, min(start+step, len(faces)))
	}
	wg.Wait()
	return out
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
