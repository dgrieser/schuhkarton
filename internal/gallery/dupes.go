package gallery

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"io"
	"math"
	"math/bits"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Duplicates are found from the small thumbnails, so it is cheap enough to
// run inside the gallery (a few milliseconds per photo):
//
//   - every photo and video poster gets perceptual hashes (a DCT hash, a
//     gradient hash and a 16×16 mean hash) for all eight turns and mirror
//     images, plus colour statistics;
//   - "gleich" (the same picture: recompressed, resized, another format,
//     turned or mirrored, EXIF stripped) needs all hashes, the colours and
//     the aspect ratio to agree closely; a multi-index over the DCT hash
//     finds candidates without comparing every pair;
//   - "identisch" are equal files (same size and SHA-256, hashed only for
//     candidates);
//   - "ähnlich" are series: looser hashes within a few minutes of each
//     other (bursts, the same scene photographed again).
//
// Hashes live in dupes.json by root/rel, like favorites, and follow moves.

// Duplicate classes.
const (
	DupIdentical = "identisch"
	DupSame      = "gleich"
	DupSimilar   = "aehnlich"
)

// DupeOptions configure the search for duplicates.
type DupeOptions struct {
	Off           bool
	Sensitivity   string        // strict, normal (default), loose
	SimilarWindow time.Duration // series: photos at most this far apart; 0: 10 minutes
}

type dupeLimits struct {
	same, sameD, sameA int     // max bit differences of DCT, gradient and mean hash for "gleich"
	sim, simA          int     // for "ähnlich"
	color, simColor    float64 // max difference of colour statistics
}

func (o DupeOptions) limits() dupeLimits {
	switch o.Sensitivity {
	case "strict":
		return dupeLimits{same: 3, sameD: 7, sameA: 18, sim: 10, simA: 56, color: 12, simColor: 22}
	case "loose":
		return dupeLimits{same: 7, sameD: 13, sameA: 34, sim: 18, simA: 84, color: 22, simColor: 36}
	}
	return dupeLimits{same: 5, sameD: 10, sameA: 26, sim: 14, simA: 70, color: 16, simColor: 30}
}

func (o DupeOptions) window() time.Duration {
	if o.SimilarWindow > 0 {
		return o.SimilarWindow
	}
	return 10 * time.Minute
}

// dupeSig is what is known about one file.
type dupeSig struct {
	Key string       `json:"k"` // cache key of the item when hashed
	PH  [8]uint64    `json:"p"` // DCT hash of the eight turns/mirrors; [0] as shown
	DH  [8]uint64    `json:"d"` // gradient hash, same order
	AH  [8][4]uint64 `json:"a"` // 16×16 mean hash, same order
	Col [6]float32   `json:"c"` // mean and spread of red, green, blue
	Var float32      `json:"v"` // spread of the brightness (detail)
	AR  float32      `json:"r"` // aspect ratio of the thumbnail
	Sha string       `json:"s,omitempty"`
	SK  string       `json:"sk,omitempty"` // cache key the SHA belongs to
	Err string       `json:"e,omitempty"`
}

// DupeGroup is one set of duplicates.
type DupeGroup struct {
	ID      string              `json:"id"`
	Class   string              `json:"class"`
	Items   []string            `json:"items"` // item ids, the suggestion first
	Keep    []string            `json:"keep"`  // suggested to keep
	Reasons map[string][]string `json:"reasons,omitempty"`
	Save    int64               `json:"save"`  // bytes freed by the suggestion
	Taken   int64               `json:"taken"` // newest capture time
}

type dupeStore struct {
	mu     sync.Mutex
	sigs   map[string]*dupeSig // root name + "/" + rel
	ignore [][]string          // sets of keys that are not duplicates of each other
	groups []DupeGroup
	ver    int64 // -1: group again; else the item list the groups were made for (see listVer)
	dirty  atomic.Bool

	dims             sync.Map // cache key -> [2]int pixel size from the file header
	wake             chan struct{}
	done, todo, nGrp atomic.Int64
	running          atomic.Bool
}

// --- storage ------------------------------------------------------------------

func (l *Library) loadDupes() {
	l.dupe.sigs = map[string]*dupeSig{}
	l.dupe.wake = make(chan struct{}, 1)
	l.dupe.ver = -1
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "dupes.json"))
	if err != nil {
		return
	}
	var f struct {
		Sigs   map[string]*dupeSig `json:"sigs"`
		Ignore [][]string          `json:"ignore"`
	}
	if json.Unmarshal(b, &f) == nil {
		if f.Sigs != nil {
			l.dupe.sigs = f.Sigs
		}
		l.dupe.ignore = f.Ignore
	}
}

func (l *Library) saveDupes() error {
	if !l.dupe.dirty.Swap(false) {
		return nil
	}
	l.dupe.mu.Lock()
	b, err := json.Marshal(map[string]any{"version": 1, "sigs": l.dupe.sigs, "ignore": l.dupe.ignore})
	l.dupe.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "dupes.json"), b)
}

func (l *Library) markDupesDirty() {
	if l.dupe.dirty.Swap(true) {
		return
	}
	time.AfterFunc(30*time.Second, func() {
		if err := l.saveDupes(); err != nil {
			l.logf("dupes.json: %v", err)
		}
	})
}

// renameDupes moves hashes and ignore entries to new keys ("" drops them).
func (l *Library) renameDupes(pairs map[string]string) {
	l.dupe.mu.Lock()
	changed := false
	for from, to := range pairs {
		if s, ok := l.dupe.sigs[from]; ok {
			delete(l.dupe.sigs, from)
			if to != "" {
				l.dupe.sigs[to] = s
			}
			changed = true
		}
	}
	for i, set := range l.dupe.ignore {
		var next []string
		for _, k := range set {
			if to, ok := pairs[k]; ok {
				changed = true
				if to == "" {
					continue
				}
				k = to
			}
			next = append(next, k)
		}
		l.dupe.ignore[i] = next
	}
	l.dupe.ver = -1
	l.dupe.mu.Unlock()
	if changed {
		l.markDupesDirty()
	}
	l.kickDupes()
}

// --- hashing ------------------------------------------------------------------

const hashN = 32

var dctCos = func() [8][hashN]float64 {
	var c [8][hashN]float64
	for u := range 8 {
		for x := range hashN {
			c[u][x] = math.Cos(float64(2*x+1) * float64(u) * math.Pi / (2 * hashN))
		}
	}
	return c
}()

// transform returns the n×n matrix m turned/mirrored: t 0..3 turn by
// 0/90/180/270 degrees clockwise, 4..7 the same after a mirror.
func transform(m []float64, n, t int) []float64 {
	out := make([]float64, n*n)
	for y := range n {
		for x := range n {
			sx, sy := x, y
			if t >= 4 {
				sx = n - 1 - sx
			}
			// the value shown at (x, y) after turning comes from here
			switch t % 4 {
			case 1:
				sx, sy = sy, n-1-sx
			case 2:
				sx, sy = n-1-sx, n-1-sy
			case 3:
				sx, sy = n-1-sy, sx
			}
			out[y*n+x] = m[sy*n+sx]
		}
	}
	return out
}

// shrink averages an n×n matrix down to w×h cells.
func shrink(m []float64, n, w, h int) []float64 {
	out := make([]float64, w*h)
	for y := range h {
		y0, y1 := y*n/h, (y+1)*n/h
		for x := range w {
			x0, x1 := x*n/w, (x+1)*n/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}
			var s float64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					s += m[yy*n+xx]
				}
			}
			out[y*w+x] = s / float64((x1-x0)*(y1-y0))
		}
	}
	return out
}

func dctHash(m []float64) uint64 {
	var tmp [8][hashN]float64
	for u := range 8 {
		for y := range hashN {
			var s float64
			for x := range hashN {
				s += dctCos[u][x] * m[y*hashN+x]
			}
			tmp[u][y] = s
		}
	}
	var coef [64]float64
	for u := range 8 {
		for v := range 8 {
			var s float64
			for y := range hashN {
				s += dctCos[v][y] * tmp[u][y]
			}
			coef[v*8+u] = s
		}
	}
	vals := append([]float64(nil), coef[1:]...)
	sort.Float64s(vals)
	med := (vals[31] + vals[32]) / 2
	var h uint64
	for i, c := range coef {
		if c > med {
			h |= 1 << i
		}
	}
	return h
}

func gradHash(m []float64) uint64 {
	g := shrink(m, hashN, 9, 8)
	var h uint64
	for y := range 8 {
		for x := range 8 {
			// a small step is noise (flat sky, white paper), not an edge
			if g[y*9+x+1]-g[y*9+x] > 2.5 {
				h |= 1 << (y*8 + x)
			}
		}
	}
	return h
}

func meanHash(m []float64) [4]uint64 {
	g := shrink(m, hashN, 16, 16)
	var mean float64
	for _, v := range g {
		mean += v
	}
	mean /= float64(len(g))
	var h [4]uint64
	for i, v := range g {
		if v > mean {
			h[i/64] |= 1 << (i % 64)
		}
	}
	return h
}

// sigOf hashes a picture (normally the small thumbnail).
func sigOf(img image.Image) *dupeSig {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	s := &dupeSig{}
	if w < 8 || h < 8 {
		s.Err = "zu klein"
		return s
	}
	s.AR = float32(w) / float32(h)
	gray := make([]float64, hashN*hashN)
	cnt := make([]float64, hashN*hashN)
	var sum, sum2 [3]float64
	var n float64
	// every pixel goes to the cell of its centre, so turned and mirrored
	// copies fill the cells the same way
	cellX := make([]int, w)
	for x := range w {
		cellX[x] = int((float64(x) + 0.5) * hashN / float64(w))
	}
	ycc, isYCC := img.(*image.YCbCr) // decoded JPEG: brightness straight from Y
	for y := range h {
		cy := int((float64(y) + 0.5) * hashN / float64(h))
		colour := y%3 == 1
		for x := range w {
			i := cy*hashN + cellX[x]
			if isYCC && !(colour && x%3 == 1) {
				gray[i] += float64(ycc.Y[ycc.YOffset(b.Min.X+x, b.Min.Y+y)])
				cnt[i]++
				continue
			}
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			R, G, B := float64(r>>8), float64(g>>8), float64(bl>>8)
			gray[i] += 0.299*R + 0.587*G + 0.114*B
			cnt[i]++
			if colour && x%3 == 1 { // colour statistics from every 9th pixel
				sum[0] += R
				sum[1] += G
				sum[2] += B
				sum2[0] += R * R
				sum2[1] += G * G
				sum2[2] += B * B
				n++
			}
		}
	}
	n = math.Max(n, 1)
	var gm, gv float64
	for i := range gray {
		if cnt[i] > 0 {
			gray[i] /= cnt[i]
		}
		gm += gray[i]
	}
	gm /= float64(len(gray))
	for _, v := range gray {
		gv += (v - gm) * (v - gm)
	}
	s.Var = float32(math.Sqrt(gv / float64(len(gray))))
	for c := range 3 {
		mean := sum[c] / n
		s.Col[c] = float32(mean)
		s.Col[3+c] = float32(math.Sqrt(math.Max(0, sum2[c]/n-mean*mean)))
	}
	for t := range 8 {
		m := transform(gray, hashN, t)
		s.PH[t] = dctHash(m)
		s.DH[t] = gradHash(m)
		s.AH[t] = meanHash(m)
	}
	return s
}

func colorDiff(a, b *dupeSig) float64 {
	var d float64
	for i := range a.Col {
		d = math.Max(d, math.Abs(float64(a.Col[i]-b.Col[i])))
	}
	return d
}

func ahDist(a, b [4]uint64) int {
	return bits.OnesCount64(a[0]^b[0]) + bits.OnesCount64(a[1]^b[1]) + bits.OnesCount64(a[2]^b[2]) + bits.OnesCount64(a[3]^b[3])
}

// aspectOK compares aspect ratios; turns by 90/270 degrees swap them.
func aspectOK(a, b *dupeSig, t int, tol float64) bool {
	ar := float64(a.AR)
	if t%2 == 1 && ar > 0 {
		ar = 1 / ar
	}
	if ar <= 0 || b.AR <= 0 {
		return true
	}
	return math.Abs(math.Log(ar/float64(b.AR))) <= tol
}

// sameTurn returns the turn t of a that shows the same picture as b, or -1.
func sameTurn(a, b *dupeSig, lim dupeLimits) int {
	best, bestD := -1, 1<<10
	strict := a.Var < 12 || b.Var < 12 // nearly plain pictures (white pages, night): stricter
	for t := range 8 {
		dp := bits.OnesCount64(a.PH[t] ^ b.PH[0])
		limP, limD, limA := lim.same, lim.sameD, lim.sameA
		if strict {
			limP, limD, limA = limP/2, limD/2, limA/2
		}
		if dp > limP || dp >= bestD {
			continue
		}
		if bits.OnesCount64(a.DH[t]^b.DH[0]) > limD || ahDist(a.AH[t], b.AH[0]) > limA {
			continue
		}
		if !aspectOK(a, b, t, 0.04) || colorDiff(a, b) > lim.color {
			continue
		}
		best, bestD = t, dp
	}
	return best
}

// similar reports a series member: looser hashes, same orientation.
func similar(a, b *dupeSig, lim dupeLimits) (int, bool) {
	dp := bits.OnesCount64(a.PH[0] ^ b.PH[0])
	if dp > lim.sim || ahDist(a.AH[0], b.AH[0]) > lim.simA {
		return dp, false
	}
	if !aspectOK(a, b, 0, 0.12) || colorDiff(a, b) > lim.simColor {
		return dp, false
	}
	if a.Var < 10 && b.Var < 10 {
		return dp, false // plain pictures look alike without being a series
	}
	return dp, true
}

// distinctShots: two camera times seconds apart are two photos, however
// alike (copies keep the time of the original, or lose it).
func distinctShots(a, b *Item) bool {
	if a.TakenSrc != "exif" || b.TakenSrc != "exif" {
		return false
	}
	d := a.Taken - b.Taken
	return d >= 2 || d <= -2
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}

// --- background job -----------------------------------------------------------

func (l *Library) kickDupes() {
	if l.opt.Dupes.Off || l.dupe.wake == nil {
		return
	}
	select {
	case l.dupe.wake <- struct{}{}:
	default:
	}
}

func dupeEligible(it *Item) bool { return it.Kind == Photo || (it.Kind == Video && it.LivePhoto == "") }

func (l *Library) dupeLoop(ctx context.Context) {
	defer l.bg.Done()
	tick := time.NewTicker(10 * time.Second) // cheap when nothing changed
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			l.saveDupes()
			return
		case <-l.dupe.wake:
		case <-tick.C:
		}
		wctx, cancel, ok := l.opt.Window.Wait(ctx)
		if !ok {
			l.saveDupes()
			return
		}
		l.findDupes(wctx)
		cancel()
	}
}

// findDupes hashes what is new and groups everything again when something
// changed.
func (l *Library) findDupes(ctx context.Context) {
	if !l.dupe.running.CompareAndSwap(false, true) {
		return
	}
	defer l.dupe.running.Store(false)
	items := l.Items()
	var todo []*Item
	var total, have int64
	l.dupe.mu.Lock()
	for _, it := range items {
		if !dupeEligible(it) {
			continue
		}
		total++
		if s := l.dupe.sigs[l.favKey(it)]; s != nil && s.Key == it.key {
			have++
			continue
		}
		todo = append(todo, it)
	}
	l.dupe.mu.Unlock()
	l.dupe.todo.Store(total)
	l.dupe.done.Store(have)
	var hashed atomic.Int64
	start := time.Now()
	if len(todo) > 0 {
		ch := make(chan *Item)
		var wg sync.WaitGroup
		for range max(1, l.opt.Workers/2) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for it := range ch {
					p := l.CachePath(it, VarSmall)
					if !exists(p) {
						continue // the thumbnail comes first
					}
					img, err := decodeFile(p)
					var s *dupeSig
					if err != nil {
						s = &dupeSig{Err: err.Error()}
					} else {
						s = sigOf(img)
					}
					s.Key = it.key
					l.dupe.mu.Lock()
					l.dupe.sigs[l.favKey(it)] = s
					l.dupe.mu.Unlock()
					l.dupe.done.Add(1)
					hashed.Add(1)
				}
			}()
		}
	feed:
		for _, it := range todo {
			select {
			case ch <- it:
			case <-ctx.Done():
				break feed
			}
		}
		close(ch)
		wg.Wait()
		if hashed.Load() > 0 {
			l.markDupesDirty()
		}
	}
	l.dupe.mu.Lock()
	stale := l.dupe.ver != l.listVer() || hashed.Load() > 0
	l.dupe.mu.Unlock()
	if stale && ctx.Err() == nil {
		g0 := time.Now()
		l.groupDupes()
		if n := hashed.Load(); n > 0 {
			l.logf("duplicates: %d hashed in %s, grouped in %s: %d groups", n, g0.Sub(start).Round(time.Millisecond),
				time.Since(g0).Round(time.Millisecond), l.dupe.nGrp.Load())
		}
	}
}

// --- grouping -----------------------------------------------------------------

type uf struct{ p, n []int }

func newUF(n int) *uf {
	u := &uf{p: make([]int, n), n: make([]int, n)}
	for i := range u.p {
		u.p[i], u.n[i] = i, 1
	}
	return u
}

func (u *uf) find(i int) int {
	for u.p[i] != i {
		u.p[i] = u.p[u.p[i]]
		i = u.p[i]
	}
	return i
}

func (u *uf) union(a, b, limit int) bool {
	a, b = u.find(a), u.find(b)
	if a == b {
		return true
	}
	if limit > 0 && u.n[a]+u.n[b] > limit {
		return false
	}
	if u.n[a] < u.n[b] {
		a, b = b, a
	}
	u.p[b] = a
	u.n[a] += u.n[b]
	return true
}

type dupNode struct {
	it  *Item
	key string
	sig *dupeSig
}

// groupDupes builds the groups from the hashes.
func (l *Library) groupDupes() {
	ver := l.listVer()
	lim := l.opt.Dupes.limits()
	var nodes []dupNode
	l.dupe.mu.Lock()
	for _, it := range l.Items() {
		if !dupeEligible(it) {
			continue
		}
		k := l.favKey(it)
		if s := l.dupe.sigs[k]; s != nil && s.Key == it.key && s.Err == "" {
			nodes = append(nodes, dupNode{it, k, s})
		}
	}
	ignored := map[string][]int{}
	for i, set := range l.dupe.ignore {
		for _, k := range set {
			ignored[k] = append(ignored[k], i)
		}
	}
	l.dupe.mu.Unlock()
	skip := func(a, b string) bool {
		for _, i := range ignored[a] {
			for _, j := range ignored[b] {
				if i == j {
					return true
				}
			}
		}
		return false
	}

	// "gleich": candidates from a multi-index over the four 16-bit parts of
	// the DCT hash (two hashes within 7 bits share a part within 1 bit)
	var index [4]map[uint16][]int32
	for p := range index {
		index[p] = map[uint16][]int32{}
	}
	for i, n := range nodes {
		h := n.sig.PH[0]
		for p := range 4 {
			c := uint16(h >> (16 * p))
			index[p][c] = append(index[p][c], int32(i))
		}
	}
	same := newUF(len(nodes))
	seen := map[[2]int32]bool{}
	for i, a := range nodes {
		for t := range 8 {
			h := a.sig.PH[t]
			for p := range 4 {
				c := uint16(h >> (16 * p))
				probe := func(c uint16) {
					for _, j := range index[p][c] {
						if int(j) <= i {
							continue
						}
						pair := [2]int32{int32(i), j}
						if seen[pair] {
							continue
						}
						seen[pair] = true
						b := nodes[j]
						if a.it.Kind != b.it.Kind || skip(a.key, b.key) {
							continue
						}
						if a.it.Kind == Video && math.Abs(a.it.Duration-b.it.Duration) > math.Max(1, 0.02*math.Max(a.it.Duration, b.it.Duration)) {
							continue
						}
						if distinctShots(a.it, b.it) {
							continue // a burst: two photos, not two copies (the series pass sees them)
						}
						if sameTurn(a.sig, b.sig, lim) >= 0 {
							same.union(i, int(j), 0)
						}
					}
				}
				probe(c)
				for bit := range 16 {
					probe(c ^ 1<<bit)
				}
			}
		}
	}

	// equal files: SHA-256 only for candidates of the same size
	members := map[int][]int{}
	for i := range nodes {
		r := same.find(i)
		members[r] = append(members[r], i)
	}
	var groups []DupeGroup
	inSame := map[int]int{} // node -> representative
	for r, idx := range members {
		if len(idx) < 2 {
			continue
		}
		cls := DupIdentical
		var sha string
		for k, i := range idx {
			s := l.shaOf(nodes[i])
			if s == "" || (k > 0 && s != sha) || nodes[i].it.Size != nodes[idx[0]].it.Size {
				cls = DupSame
			}
			if k == 0 {
				sha = s
			}
		}
		g := l.makeGroup(nodes, idx, cls)
		groups = append(groups, g)
		for _, i := range idx {
			inSame[i] = r
		}
		_ = r
	}

	// "ähnlich": series within a time window; a "gleich" group counts as one
	// photo (its suggestion)
	keepOf := map[string]bool{}
	for _, g := range groups {
		for _, id := range g.Keep {
			keepOf[id] = true
		}
	}
	var cand []int
	for i, n := range nodes {
		if n.it.Kind != Photo {
			continue
		}
		if _, ok := inSame[i]; ok && !keepOf[n.it.ID] {
			continue
		}
		cand = append(cand, i)
	}
	sort.Slice(cand, func(a, b int) bool { return nodes[cand[a]].it.Taken < nodes[cand[b]].it.Taken })
	win := int64(l.opt.Dupes.window().Seconds())
	type edge struct{ a, b, d int }
	var edges []edge
	for x, i := range cand {
		for y := x + 1; y < len(cand) && y < x+200; y++ {
			j := cand[y]
			if nodes[j].it.Taken-nodes[i].it.Taken > win {
				break
			}
			if skip(nodes[i].key, nodes[j].key) {
				continue
			}
			if d, ok := similar(nodes[i].sig, nodes[j].sig, lim); ok {
				edges = append(edges, edge{i, j, d})
			}
		}
	}
	sort.Slice(edges, func(a, b int) bool { return edges[a].d < edges[b].d })
	sim := newUF(len(nodes))
	for _, e := range edges {
		sim.union(e.a, e.b, 30) // no endless chains
	}
	smembers := map[int][]int{}
	for _, i := range cand {
		r := sim.find(i)
		smembers[r] = append(smembers[r], i)
	}
	for _, idx := range smembers {
		if len(idx) < 2 {
			continue
		}
		groups = append(groups, l.makeGroup(nodes, idx, DupSimilar))
	}

	order := map[string]int{DupIdentical: 0, DupSame: 1, DupSimilar: 2}
	sort.Slice(groups, func(a, b int) bool {
		if order[groups[a].Class] != order[groups[b].Class] {
			return order[groups[a].Class] < order[groups[b].Class]
		}
		if groups[a].Save != groups[b].Save {
			return groups[a].Save > groups[b].Save
		}
		return groups[a].ID < groups[b].ID
	})
	l.dupe.mu.Lock()
	l.dupe.groups, l.dupe.ver = groups, ver
	l.dupe.mu.Unlock()
	l.dupe.nGrp.Store(int64(len(groups)))
}

// listVer changes whenever the item list is replaced (scans, deletes,
// restores, moves, edits), not with favorites or names.
func (l *Library) listVer() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.items) == 0 {
		return 0
	}
	return int64(uintptr(unsafe.Pointer(&l.items[0]))) ^ int64(len(l.items))<<48
}

// shaOf hashes a file once (and again when it changed).
func (l *Library) shaOf(n dupNode) string {
	l.dupe.mu.Lock()
	s := n.sig
	if s.Sha != "" && s.SK == n.it.key {
		l.dupe.mu.Unlock()
		return s.Sha
	}
	l.dupe.mu.Unlock()
	h, err := fileSHA(l.Path(n.it))
	if err != nil {
		return ""
	}
	l.dupe.mu.Lock()
	s.Sha, s.SK = h, n.it.key
	l.dupe.mu.Unlock()
	l.markDupesDirty()
	return h
}

func (l *Library) makeGroup(nodes []dupNode, idx []int, cls string) DupeGroup {
	items := make([]*Item, len(idx))
	keys := make([]string, len(idx))
	for k, i := range idx {
		items[k] = nodes[i].it
		keys[k] = nodes[i].key
	}
	ranked, reasons := l.rankCopies(items)
	g := DupeGroup{Class: cls, Reasons: map[string][]string{}}
	for _, it := range ranked {
		g.Items = append(g.Items, it.ID)
		g.Taken = max(g.Taken, it.Taken)
	}
	g.Keep = []string{ranked[0].ID}
	g.Reasons[ranked[0].ID] = reasons
	if cls != DupSimilar {
		for _, it := range ranked[1:] {
			g.Save += it.Size
			if lv := l.liveOf(it); lv != nil {
				g.Save += lv.Size
			}
		}
	}
	sort.Strings(keys)
	h := sha1.Sum([]byte(strings.Join(keys, "\x00")))
	g.ID = hex.EncodeToString(h[:6])
	return g
}

func (l *Library) liveOf(it *Item) *Item {
	if it.LiveVideo == "" {
		return nil
	}
	return l.Get(it.LiveVideo)
}

// --- the best copy ------------------------------------------------------------

var formatRank = map[string]int{
	".arw": 5, ".cr2": 5, ".cr3": 5, ".nef": 5, ".dng": 5, ".orf": 5, ".rw2": 5, ".raf": 5,
	".heic": 4, ".heif": 4, ".avif": 4,
	".png": 3, ".tif": 3, ".tiff": 3,
	".jpg": 2, ".jpeg": 2, ".jpe": 2,
	".mov": 3, ".mp4": 2, ".m4v": 2,
}

var copyFolder = []string{"whatsapp", "telegram", "signal", "screenshot", "bildschirmfoto", "download", "kopie", "copy", "dupli", "backup", "sicherung", "export", "temp"}

func copyName(name string) bool {
	n := strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
	for _, s := range []string{" (1)", " (2)", " (3)", "_copy", " copy", " kopie", "-kopie", "_kopie", "-1", "_1", "~"} {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return strings.Contains(n, "-wa0") || strings.Contains(n, "img-20") && strings.Contains(n, "wa")
}

type rankInfo struct {
	score   float64
	reasons []string
}

// rankCopies orders copies best first and says why the first one wins.
func (l *Library) rankCopies(items []*Item) ([]*Item, []string) {
	info := map[*Item]*rankInfo{}
	var maxPx, maxSize int64
	maxFmt := 0
	px := map[*Item]int64{}
	for _, it := range items {
		w, h := l.dims(it)
		px[it] = int64(w) * int64(h)
		maxPx = max(maxPx, px[it])
		maxSize = max(maxSize, it.Size)
		maxFmt = max(maxFmt, formatRank[it.Ext()])
	}
	for _, it := range items {
		r := &rankInfo{}
		if p := px[it]; p > 0 {
			r.score += 40 * math.Log2(float64(p)+1) / math.Log2(float64(maxPx)+2)
		}
		fr := formatRank[it.Ext()]
		r.score += float64(fr) * 3
		if maxSize > 0 {
			r.score += 4 * float64(it.Size) / float64(maxSize)
		}
		switch it.TakenSrc {
		case "exif", "edit":
			r.score += 6
		case "name":
			r.score += 3
		}
		if it.GPS {
			r.score += 4
		}
		if it.Make != "" || it.Model != "" {
			r.score += 2
		}
		low := strings.ToLower(l.AlbumOf(it))
		for _, w := range copyFolder {
			if strings.Contains(low, w) {
				r.score -= 6
				break
			}
		}
		if copyName(path.Base(it.Rel)) {
			r.score -= 3
		}
		r.score += 2 * float64(min(3, l.favCount(it)))
		r.score += float64(min(4, l.namedFaces(it)))
		if l.override(it) != nil {
			r.score += 2
		}
		if l.AIOf(it) != nil {
			r.score += 1
		}
		if it.LiveVideo != "" {
			r.score += 2
		}
		info[it] = r
	}
	ranked := append([]*Item(nil), items...)
	sort.SliceStable(ranked, func(a, b int) bool {
		if info[ranked[a]].score != info[ranked[b]].score {
			return info[ranked[a]].score > info[ranked[b]].score
		}
		return ranked[a].Rel < ranked[b].Rel
	})
	best := ranked[0]
	var why []string
	larger := true
	for _, it := range ranked[1:] {
		if px[it] >= px[best] {
			larger = false
		}
	}
	if larger && len(ranked) > 1 {
		why = append(why, "größte Auflösung")
	}
	if fr := formatRank[best.Ext()]; fr == maxFmt && fr >= 3 {
		why = append(why, "Originalformat")
	}
	if best.TakenSrc == "exif" || best.TakenSrc == "edit" {
		why = append(why, "Aufnahmedatum der Kamera")
	}
	if best.GPS {
		why = append(why, "mit Ortsangabe")
	}
	if best.LiveVideo != "" {
		why = append(why, "Live Photo")
	}
	if l.favCount(best) > 0 {
		why = append(why, "Favorit")
	}
	if l.namedFaces(best) > 0 {
		why = append(why, "Personen benannt")
	}
	biggest := true
	for _, it := range ranked[1:] {
		if it.Size >= best.Size {
			biggest = false
		}
	}
	if biggest && !larger && len(ranked) > 1 {
		why = append(why, "größte Datei")
	}
	if len(why) == 0 {
		why = append(why, "gleichwertig")
	}
	return ranked, why
}

// dims returns the pixel size of a photo from its file header (the index
// may not know it without exiftool), else what the index says.
func (l *Library) dims(it *Item) (int, int) {
	if it.Kind == Photo {
		if v, ok := l.dupe.dims.Load(it.key); ok {
			d := v.([2]int)
			return d[0], d[1]
		}
		if f, err := os.Open(l.Path(it)); err == nil {
			cfg, _, err := image.DecodeConfig(f)
			f.Close()
			if err == nil && cfg.Width > 0 {
				w, h := cfg.Width, cfg.Height
				if it.Aspect > 0 && (it.Aspect < 1) != (w < h) {
					w, h = h, w // shown turned (EXIF orientation)
				}
				l.dupe.dims.Store(it.key, [2]int{w, h})
				return w, h
			}
		}
	}
	return it.W, it.H
}

func (l *Library) favCount(it *Item) int {
	k := l.favKey(it)
	l.favMu.Lock()
	defer l.favMu.Unlock()
	n := 0
	for _, set := range l.favs {
		if set[k] {
			n++
		}
	}
	return n
}

func (l *Library) namedFaces(it *Item) int {
	n := 0
	for i := range l.ItemFaces(it) {
		if p := l.Person(l.PersonOf(it.ID, i)); p != nil && p.Name != "" {
			n++
		}
	}
	return n
}

// --- API ------------------------------------------------------------------------

// DupeStatus reports the search for duplicates.
type DupeStatus struct {
	Mode    string         `json:"mode"`
	Done    int64          `json:"done"`
	Todo    int64          `json:"todo"`
	Running bool           `json:"running"`
	Counts  map[string]int `json:"counts"`
	Save    int64          `json:"save"` // bytes the suggestions of identical and same groups free
}

// Dupes returns the groups and the state of the search.
func (l *Library) Dupes() ([]DupeGroup, DupeStatus) {
	st := DupeStatus{Mode: "on", Done: l.dupe.done.Load(), Todo: l.dupe.todo.Load(), Running: l.dupe.running.Load(),
		Counts: map[string]int{DupIdentical: 0, DupSame: 0, DupSimilar: 0}}
	if l.opt.Dupes.Off {
		st.Mode = "off"
	}
	l.dupe.mu.Lock()
	groups := append([]DupeGroup(nil), l.dupe.groups...)
	l.dupe.mu.Unlock()
	// drop members that are gone since (deleted, moved)
	out := groups[:0]
	for _, g := range groups {
		var ids []string
		for _, id := range g.Items {
			if l.Get(id) != nil {
				ids = append(ids, id)
			}
		}
		if len(ids) < 2 {
			continue
		}
		g.Items = ids
		if l.Get(g.Keep[0]) == nil {
			g.Keep = []string{ids[0]}
		}
		out = append(out, g)
		st.Counts[g.Class]++
		if g.Class != DupSimilar {
			st.Save += g.Save
		}
	}
	return out, st
}

// DupeMember describes one copy for the comparison.
type DupeMember struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Album    string   `json:"album"`
	Kind     Kind     `json:"kind"`
	W        int      `json:"w"`
	H        int      `json:"h"`
	Size     int64    `json:"size"`
	Ext      string   `json:"ext"`
	Taken    int64    `json:"taken"`
	TakenSrc string   `json:"takenSrc"`
	Place    string   `json:"place,omitempty"`
	GPS      bool     `json:"gps"`
	Camera   string   `json:"camera,omitempty"`
	Duration float64  `json:"duration,omitempty"`
	Favs     int      `json:"favs"`
	Faces    int      `json:"faces"`
	Named    int      `json:"named"`
	Edited   []string `json:"edited,omitempty"`
	AI       bool     `json:"ai"`
	Live     bool     `json:"live"`
	Marks    int      `json:"marks"`
	V        string   `json:"v"`
}

// DupeMembers describes items for the comparison view.
func (l *Library) DupeMembers(ids []string) map[string]DupeMember {
	out := map[string]DupeMember{}
	for _, id := range ids {
		it := l.Get(id)
		if it == nil {
			continue
		}
		w, h := l.dims(it)
		m := DupeMember{ID: it.ID, Name: path.Base(it.Rel), Album: l.AlbumOf(it), Kind: it.Kind, W: w, H: h, Size: it.Size,
			Ext: strings.TrimPrefix(strings.ToUpper(path.Ext(it.Rel)), "."), Taken: it.Taken, TakenSrc: it.TakenSrc,
			Place: it.Place, GPS: it.GPS, Camera: strings.TrimSpace(it.Make + " " + it.Model), Duration: it.Duration,
			Favs: l.favCount(it), Faces: len(l.ItemFaces(it)), Named: l.namedFaces(it), Edited: l.EditedFields(it),
			AI: l.AIOf(it) != nil, Live: it.LiveVideo != "", Marks: len(l.MarkedBy(it)), V: it.key}
		if len(m.V) > 8 {
			m.V = m.V[:8]
		}
		out[id] = m
	}
	return out
}

// IgnoreDupes remembers that items are not duplicates of each other.
func (l *Library) IgnoreDupes(ids []string) error {
	var keys []string
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			keys = append(keys, l.favKey(it))
		}
	}
	if len(keys) < 2 {
		return errors.New("zu wenige Elemente")
	}
	l.dupe.mu.Lock()
	l.dupe.ignore = append(l.dupe.ignore, keys)
	l.dupe.ver = -1
	// drop the group at once, the next pass agrees
	var keep []DupeGroup
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	for _, g := range l.dupe.groups {
		all := true
		for _, id := range g.Items {
			if !set[id] {
				all = false
				break
			}
		}
		if !all {
			keep = append(keep, g)
		}
	}
	l.dupe.groups = keep
	l.dupe.mu.Unlock()
	l.markDupesDirty()
	l.kickDupes()
	return l.saveDupes()
}

// DupeResolution is one group decided: keep these, trash those.
type DupeResolution struct {
	Keep   []string `json:"keep"`
	Remove []string `json:"remove"`
}

// ResolveDupes merges what the removed copies know into the first kept
// one (favorites of every account, names of faces, place, category, text,
// position, a camera date, the AI description) and moves them to the
// trash, from where they can be restored.
func (l *Library) ResolveDupes(rs []DupeResolution, by string) (EditResult, int64, error) {
	var res EditResult
	var freed int64
	var remove []string
	for _, r := range rs {
		if len(r.Keep) == 0 || len(r.Remove) == 0 {
			continue
		}
		keep := l.Get(r.Keep[0])
		if keep == nil {
			res.warn("Element nicht gefunden")
			continue
		}
		kept := map[string]bool{}
		for _, id := range r.Keep {
			kept[id] = true
		}
		var rem []*Item
		for _, id := range r.Remove {
			if kept[id] {
				continue
			}
			if it := l.Get(id); it != nil {
				rem = append(rem, it)
			}
		}
		if len(rem) == 0 {
			continue
		}
		l.mergeInto(keep, rem, by, &res)
		for _, it := range rem {
			remove = append(remove, it.ID)
			freed += it.Size
			if lv := l.liveOf(it); lv != nil {
				freed += lv.Size
			}
		}
	}
	if len(remove) == 0 {
		return res, 0, errors.New("nichts zu löschen")
	}
	tr, err := l.Trash(remove, by)
	res.Changed, res.Entries = tr.Changed, tr.Entries
	res.Warnings = append(res.Warnings, tr.Warnings...)
	l.kickDupes()
	return res, freed, err
}

func (l *Library) mergeInto(keep *Item, rem []*Item, by string, res *EditResult) {
	// favorites of every account
	kk := l.favKey(keep)
	l.favMu.Lock()
	favChanged := false
	for _, set := range l.favs {
		for _, it := range rem {
			if set[l.favKey(it)] && !set[kk] {
				set[kk] = true
				favChanged = true
			}
		}
	}
	if favChanged {
		l.bump()
		if err := l.saveFavs(); err != nil {
			res.warn("Favoriten: %v", err)
		}
	}
	l.favMu.Unlock()

	// names of faces: same boxes in the same place
	kf := l.ItemFaces(keep)
	for _, it := range rem {
		for i, f := range l.ItemFaces(it) {
			p := l.Person(l.PersonOf(it.ID, i))
			if p == nil || p.Name == "" {
				continue
			}
			for j, g := range kf {
				if iou(f, g) < 0.5 {
					continue
				}
				if q := l.Person(l.PersonOf(keep.ID, j)); q != nil && q.Name != "" {
					break
				}
				if err := l.LabelFace(keep.ID, j, p.Name); err != nil {
					res.warn("Namen: %v", err)
				}
				break
			}
		}
	}

	// place, category, text, position, camera date
	var e Edit
	e.By = by
	for _, it := range rem {
		if keep.Place == "" && it.Place != "" && e.Place == nil {
			v := it.Place
			e.Place = &v
		}
		if keep.Category == "" && it.Category != "" && e.Category == nil {
			v := it.Category
			e.Category = &v
		}
		if keep.Desc == "" && it.Desc != "" && e.Desc == nil {
			v := it.Desc
			e.Desc = &v
		}
		if !keep.GPS && it.GPS && e.GPS == nil {
			e.GPS = &GPSEdit{Lat: it.Lat, Lon: it.Lon}
		}
		if keep.TakenSrc != "exif" && keep.TakenSrc != "edit" && (it.TakenSrc == "exif" || it.TakenSrc == "edit") && e.Taken == nil {
			v := it.Taken
			e.Taken = &v
		}
	}
	if e.Place != nil || e.Category != nil || e.Desc != nil || e.GPS != nil || e.Taken != nil {
		if er, err := l.EditItems([]string{keep.ID}, e); err != nil {
			res.warn("Angaben übernehmen: %v", err)
		} else {
			res.Warnings = append(res.Warnings, er.Warnings...)
		}
	}

	// notes (the removed copies keep theirs for a restore)
	l.notes.mu.Lock()
	nk := l.favKey(keep)
	copied := false
	for _, it := range rem {
		for _, n := range l.notes.items[l.favKey(it)] {
			cp := *n
			l.notes.items[nk] = append(l.notes.items[nk], &cp)
			copied = true
		}
	}
	if copied {
		if err := l.saveNotes(); err != nil {
			res.warn("Notizen: %v", err)
		}
	}
	l.notes.mu.Unlock()
	if copied {
		l.notesChanged()
	}

	// the AI description
	if l.AIOf(keep) == nil {
		for _, it := range rem {
			if a := l.AIOf(it); a != nil {
				l.setAI(keep, a)
				break
			}
		}
	}
	// the recognized text
	if l.ocrOf(keep) == nil {
		for _, it := range rem {
			if o := l.ocrOf(it); o != nil {
				l.setOCR(keep, o)
				break
			}
		}
	}
}

func iou(a, b Face) float64 {
	x0, y0 := math.Max(float64(a.X), float64(b.X)), math.Max(float64(a.Y), float64(b.Y))
	x1 := math.Min(float64(a.X+a.W), float64(b.X+b.W))
	y1 := math.Min(float64(a.Y+a.H), float64(b.Y+b.H))
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	in := (x1 - x0) * (y1 - y0)
	return in / (float64(a.W*a.H+b.W*b.H) - in)
}

// RescanDupes groups everything again soon.
func (l *Library) RescanDupes() {
	l.dupe.mu.Lock()
	l.dupe.ver = -1
	l.dupe.mu.Unlock()
	l.kickDupes()
}
