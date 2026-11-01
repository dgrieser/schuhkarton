package gallery

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/bits"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/image/draw"
)

// picture draws a reproducible "photo": a gradient sky, ground and shapes.
// shift moves the shapes (a series: the same scene a moment later).
func picture(seed int64, w, h, shift int) *image.RGBA {
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c1 := color.RGBA{uint8(r.Intn(255)), uint8(r.Intn(255)), uint8(r.Intn(255)), 255}
	c2 := color.RGBA{uint8(r.Intn(255)), uint8(r.Intn(255)), uint8(r.Intn(255)), 255}
	for y := range h {
		f := float64(y) / float64(h)
		for x := range w {
			g := float64(x) / float64(w) * 0.3
			img.Set(x, y, color.RGBA{
				uint8(float64(c1.R)*(1-f) + float64(c2.R)*f*(1-g)),
				uint8(float64(c1.G)*(1-f) + float64(c2.G)*f),
				uint8(float64(c1.B)*(1-f)*(1-g) + float64(c2.B)*f),
				255})
		}
	}
	for range 14 {
		cx, cy := r.Intn(w), r.Intn(h)
		rad := w/30 + r.Intn(w/7)
		col := color.RGBA{uint8(r.Intn(255)), uint8(r.Intn(255)), uint8(r.Intn(255)), 255}
		round := r.Intn(2) == 0
		cx += shift
		for y := max(0, cy-rad); y < min(h, cy+rad); y++ {
			for x := max(0, cx-rad); x < min(w, cx+rad); x++ {
				if round && (x-cx)*(x-cx)+(y-cy)*(y-cy) > rad*rad {
					continue
				}
				img.Set(x, y, col)
			}
		}
	}
	return img
}

func recompress(t *testing.T, img image.Image, q int) image.Image {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	out, err := jpeg.Decode(&b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func resized(img image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	return dst
}

func TestSigFindsCopies(t *testing.T) {
	lim := DupeOptions{}.limits()
	orig := picture(1, 640, 480, 0)
	a := sigOf(orig)
	copies := map[string]image.Image{
		"jpeg q35":      recompress(t, orig, 35),
		"half size":     resized(orig, 320, 240),
		"thumb 120":     resized(recompress(t, orig, 60), 160, 120),
		"turned right":  orient(orig, 6),
		"turned around": orient(orig, 3),
		"mirrored":      orient(orig, 2),
		"turned+small":  resized(orient(orig, 8), 240, 320),
	}
	for name, img := range copies {
		if t2 := sameTurn(sigOf(img), a, lim); t2 < 0 {
			b := sigOf(img)
			best := 64
			for k := range 8 {
				best = min(best, bits.OnesCount64(b.PH[k]^a.PH[0]))
			}
			t.Errorf("%s: not found as the same picture (closest DCT distance %d)", name, best)
		}
	}
	for seed := int64(2); seed < 40; seed++ {
		other := sigOf(picture(seed, 640, 480, 0))
		if sameTurn(other, a, lim) >= 0 {
			t.Errorf("seed %d: a different picture counts as the same", seed)
		}
		if _, ok := similar(other, a, lim); ok {
			t.Errorf("seed %d: a different picture counts as a series", seed)
		}
	}
	// a series: the same scene with things moved a little
	burst := sigOf(picture(1, 640, 480, 18))
	if _, ok := similar(burst, a, lim); !ok {
		t.Errorf("moved shapes: not similar (distance %d)", bits.OnesCount64(burst.PH[0]^a.PH[0]))
	}
}

func TestPlainPicturesAreStrict(t *testing.T) {
	lim := DupeOptions{}.limits()
	white := image.NewRGBA(image.Rect(0, 0, 400, 560))
	page2 := image.NewRGBA(image.Rect(0, 0, 400, 560))
	for y := range 560 {
		for x := range 400 {
			white.Set(x, y, color.RGBA{250, 250, 248, 255})
			page2.Set(x, y, color.RGBA{250, 250, 248, 255})
		}
	}
	// two different "letters": lines of text in different places
	for l := range 18 {
		for x := 40; x < 360-(l*37)%120; x++ {
			for y := 60 + l*24; y < 64+l*24; y++ {
				white.Set(x, y, color.RGBA{30, 30, 30, 255})
			}
		}
		for x := 40 + (l*53)%90; x < 360; x++ {
			for y := 70 + l*26; y < 74+l*26 && y < 560; y++ {
				page2.Set(x, y, color.RGBA{30, 30, 30, 255})
			}
		}
	}
	if sameTurn(sigOf(page2), sigOf(white), lim) >= 0 {
		t.Error("two different text pages count as the same picture")
	}
	if sameTurn(sigOf(recompress(t, white, 50)), sigOf(white), lim) < 0 {
		t.Error("a recompressed text page is not found")
	}
}

func saveJPEG(t *testing.T, p string, img image.Image, q int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func copyTestFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dupeLib builds a library with known duplicates and lets the search run.
func dupeLib(t *testing.T) (*Library, string) {
	t.Helper()
	root := t.TempDir()
	a := picture(11, 1200, 900, 0)
	saveJPEG(t, filepath.Join(root, "2020/IMG_20200101_120000.jpg"), a, 92)
	saveJPEG(t, filepath.Join(root, "WhatsApp/IMG-20200105-WA0001.jpg"), resized(a, 600, 450), 60)
	saveJPEG(t, filepath.Join(root, "Gedreht/a.jpg"), orient(a, 6), 85)
	d := picture(12, 800, 600, 0)
	saveJPEG(t, filepath.Join(root, "2021/IMG_20210101_100000.jpg"), d, 90)
	copyTestFile(t, filepath.Join(root, "2021/IMG_20210101_100000.jpg"), filepath.Join(root, "Kopie/IMG_20210101_100000.jpg"))
	saveJPEG(t, filepath.Join(root, "2022/IMG_20220301_101500.jpg"), picture(13, 1000, 750, 0), 90)
	saveJPEG(t, filepath.Join(root, "2022/IMG_20220301_101503.jpg"), picture(13, 1000, 750, 45), 90)
	saveJPEG(t, filepath.Join(root, "2022/IMG_20220301_101509.jpg"), picture(13, 1000, 750, 90), 90)
	for i := range 6 {
		saveJPEG(t, filepath.Join(root, "2023", "IMG_2023010"+string(rune('1'+i))+"_120000.jpg"), picture(int64(100+i), 900, 600, 0), 90)
	}
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	l.GenerateAll(ctx)
	l.findDupes(ctx)
	return l, root
}

func relsOfIDs(l *Library, ids []string) []string {
	var out []string
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			out = append(out, it.Rel)
		}
	}
	return out
}

func TestGroupDupes(t *testing.T) {
	l, _ := dupeLib(t)
	groups, st := l.Dupes()
	if st.Done != st.Todo || st.Todo != 14 {
		t.Fatalf("hashed %d of %d", st.Done, st.Todo)
	}
	byClass := map[string][]DupeGroup{}
	for _, g := range groups {
		byClass[g.Class] = append(byClass[g.Class], g)
		t.Logf("%s: %v keep %v %v", g.Class, relsOfIDs(l, g.Items), relsOfIDs(l, g.Keep), g.Reasons)
	}
	if len(byClass[DupIdentical]) != 1 || len(byClass[DupIdentical][0].Items) != 2 {
		t.Errorf("identical: %+v", byClass[DupIdentical])
	}
	if len(byClass[DupSame]) != 1 || len(byClass[DupSame][0].Items) != 3 {
		t.Fatalf("same: %+v", byClass[DupSame])
	}
	if keep := l.Get(byClass[DupSame][0].Keep[0]); keep == nil || keep.Rel != "2020/IMG_20200101_120000.jpg" {
		t.Errorf("best copy: %v", relsOfIDs(l, byClass[DupSame][0].Keep))
	}
	if len(byClass[DupSimilar]) != 1 || len(byClass[DupSimilar][0].Items) != 3 {
		t.Errorf("series: %+v", byClass[DupSimilar])
	}
	if n := len(groups); n != 3 {
		t.Errorf("%d groups, want 3 (unrelated photos must stay alone)", n)
	}
	// ignoring a group keeps it away
	g := byClass[DupSimilar][0]
	if err := l.IgnoreDupes(g.Items); err != nil {
		t.Fatal(err)
	}
	l.groupDupes()
	groups, _ = l.Dupes()
	for _, x := range groups {
		if x.Class == DupSimilar {
			t.Errorf("ignored series came back: %v", relsOfIDs(l, x.Items))
		}
	}
}

func TestResolveDupesMerges(t *testing.T) {
	l, _ := dupeLib(t)
	groups, _ := l.Dupes()
	var g DupeGroup
	for _, x := range groups {
		if x.Class == DupSame {
			g = x
		}
	}
	keep := l.Get(g.Keep[0])
	var removed []string
	for _, id := range g.Items {
		if id != keep.ID {
			removed = append(removed, id)
		}
	}
	wa := l.Get(removed[0])
	// what only the copies know
	l.SetFav("anna", []string{wa.ID}, true)
	if _, err := l.AddNote("ben", wa.ID, "Oma am Strand"); err != nil {
		t.Fatal(err)
	}
	place := "Ostsee"
	if _, err := l.EditItems([]string{removed[1]}, Edit{Place: &place, GPS: &GPSEdit{Lat: 54.1, Lon: 10.8}}); err != nil {
		t.Fatal(err)
	}
	res, freed, err := l.ResolveDupes([]DupeResolution{{Keep: []string{keep.ID}, Remove: removed}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 || len(res.Entries) != 2 || freed <= 0 {
		t.Fatalf("result %+v freed %d", res, freed)
	}
	keep = l.Get(keep.ID)
	if !l.IsFav("anna", keep) {
		t.Error("favorite not taken over")
	}
	if ns := l.NotesOf(keep); len(ns) != 1 || ns[0].Text != "Oma am Strand" {
		t.Errorf("notes not taken over: %+v", ns)
	}
	if keep.Place != "Ostsee" || !keep.GPS {
		t.Errorf("place/position not taken over: %q %v", keep.Place, keep.GPS)
	}
	for _, id := range removed {
		if l.Get(id) != nil {
			t.Errorf("%s still in the library", id)
		}
	}
	if len(l.TrashList()) != 2 {
		t.Errorf("trash has %d entries", len(l.TrashList()))
	}
}

func TestRankCopies(t *testing.T) {
	l := &Library{opt: Options{Roots: []Root{{Name: "B", Path: "/x"}}}, favs: map[string]map[string]bool{}, edits: map[string]*Override{}}
	l.people = newPeopleStore("", 0.48, 0.42)
	l.loadAI()
	big := &Item{ID: "1", Rel: "WhatsApp/IMG-20200105-WA0001.jpg", W: 1600, H: 1200, Size: 300_000, TakenSrc: "mtime"}
	heic := &Item{ID: "2", Rel: "2020/IMG_1234.HEIC", W: 4032, H: 3024, Size: 2_000_000, TakenSrc: "exif", GPS: true}
	jpg := &Item{ID: "3", Rel: "2020/IMG_1234.jpg", W: 4032, H: 3024, Size: 3_000_000, TakenSrc: "exif"}
	ranked, why := l.rankCopies([]*Item{big, jpg, heic})
	if ranked[0] != heic {
		t.Fatalf("best: %s (%v)", ranked[0].Rel, why)
	}
	if ranked[2] != big {
		t.Errorf("worst: %s", ranked[2].Rel)
	}
	if len(why) == 0 {
		t.Error("no reason given")
	}
}

func TestHashSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	img := recompress(t, resized(picture(5, 1600, 1200, 0), 480, 360), 82)
	n := 50
	start := time.Now().UnixNano()
	for range n {
		sigOf(img)
	}
	per := float64(time.Now().UnixNano()-start) / float64(n) / 1e6
	t.Logf("hashing a 480×360 thumbnail: %.2f ms", per)
	if per > 200 || math.IsNaN(per) {
		t.Errorf("hashing too slow: %.1f ms", per)
	}
}

func TestDistinctShots(t *testing.T) {
	a := &Item{TakenSrc: "exif", Taken: 1000}
	if !distinctShots(a, &Item{TakenSrc: "exif", Taken: 1003}) {
		t.Error("camera times 3 s apart are two photos")
	}
	if distinctShots(a, &Item{TakenSrc: "exif", Taken: 1000}) {
		t.Error("the same camera time is the same photo")
	}
	if distinctShots(a, &Item{TakenSrc: "name", Taken: 5000}) {
		t.Error("a copy without camera time may still be the same photo")
	}
}
