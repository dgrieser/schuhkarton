package gallery

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgrieser/schuhkarton/internal/nasretry"
)

// Schuhkarton writes what it finds out about a file into the file's XMP
// metadata, so other programs see it and it survives a lost cache:
//
//   - AI description: XMP-iptcCore:AltTextAccessibility (x-default, de, en)
//   - AI keywords:    XMP-dc:Subject, next to the keywords already there
//   - people:         XMP-iptcExt:PersonInImage and MWG face regions
//   - recognized text and everything needed to read the rest back:
//     the XMP-schuhkarton namespace (exiftoolConfig)
//
// RAW files and formats exiftool cannot write get an .xmp file beside them
// (IMG_1.ARW.xmp). XMP-schuhkarton:Sig is a hash of what was written; a
// file whose Sig matches is left alone.

// xmpNamespace is the URI of the XMP-schuhkarton namespace.
const xmpNamespace = "https://github.com/dgrieser/schuhkarton/ns/1.0/"

// exiftoolConfig teaches exiftool the XMP-schuhkarton tags. The tag names
// carry "Sk" so they cannot clash with other tags in a JSON answer.
const exiftoolConfig = `# Written by Schuhkarton: the XMP-schuhkarton namespace.
%Image::ExifTool::UserDefined = (
    'Image::ExifTool::XMP::Main' => {
        schuhkarton => { SubDirectory => { TagTable => 'Image::ExifTool::UserDefined::schuhkarton' } },
    },
);
%Image::ExifTool::UserDefined::schuhkarton = (
    GROUPS    => { 0 => 'XMP', 1 => 'XMP-schuhkarton', 2 => 'Image' },
    NAMESPACE => { 'schuhkarton' => '` + xmpNamespace + `' },
    WRITABLE  => 'string',
    Sig         => { Name => 'SkSig' },
    Tags        => { Name => 'SkTags', List => 'Bag' },
    People      => { Name => 'SkPeople', List => 'Bag' },
    Regions     => { Name => 'SkRegions', List => 'Bag' },
    AIModel     => { Name => 'SkAIModel' },
    AIDate      => { Name => 'SkAIDate' },
    AIPeople    => { Name => 'SkAIPeople' },
    AISource    => { Name => 'SkAISource' },
    AITagsDe    => { Name => 'SkAITagsDe', List => 'Bag' },
    AITagsEn    => { Name => 'SkAITagsEn', List => 'Bag' },
    Text        => { Name => 'SkText' },
    TextModel   => { Name => 'SkTextModel' },
    TextDate    => { Name => 'SkTextDate' },
    Speech      => { Name => 'SkSpeech' },
    SpeechLang  => { Name => 'SkSpeechLang' },
    SpeechModel => { Name => 'SkSpeechModel' },
    SpeechDate  => { Name => 'SkSpeechDate' },
);
1;
`

// xmpReadTags are read during scans and before writing.
var xmpReadTags = []string{
	"-XMP-schuhkarton:all",
	"-XMP-iptcCore:AltTextAccessibility-de", "-XMP-iptcCore:AltTextAccessibility-en",
	"-struct", "-XMP-mwg-rs:RegionInfo", // face regions as a structure
}

// writeExifConfig puts the exiftool config into the cache folder.
func (l *Library) writeExifConfig() {
	p := filepath.Join(l.opt.CacheDir, "exiftool.config")
	if b, err := os.ReadFile(p); err == nil && string(b) == exiftoolConfig {
		l.exifCfg = p
		return
	}
	if err := writeFileAtomic(p, []byte(exiftoolConfig)); err != nil {
		l.logf("exiftool.config: %v", err)
		return
	}
	l.exifCfg = p
}

// xmpInPlace: formats exiftool writes XMP into safely. Everything else
// (RAW, BMP …) gets a sidecar, and so do all videos: writing into a video
// rewrites the whole file, gigabytes over a network share for a few lines.
var xmpInPlace = map[string]bool{
	".jpg": true, ".jpeg": true, ".jpe": true, ".tif": true, ".tiff": true, ".png": true, ".gif": true,
	".webp": true, ".heic": true, ".heif": true, ".avif": true,
}

// xmpTarget returns the file the metadata goes into; ext is the format of
// the file at path (Item.Ext).
func xmpTarget(path, ext string) (target string, sidecar bool) {
	if xmpInPlace[ext] {
		return path, false
	}
	return path + ".xmp", true
}

// --- what goes into a file -------------------------------------------------------

// xmpRegion is a named face as an MWG region: center and size relative to
// the stored pixels (before the EXIF orientation).
type xmpRegion struct {
	X, Y, W, H float64
	Name       string
}

// xmpPlan is everything Schuhkarton writes into one file.
type xmpPlan struct {
	Alt, AltDe, AltEn string   `json:",omitempty"` // AI description (Alt: x-default)
	TagsDe, TagsEn    []string `json:",omitempty"`
	AIModel           string   `json:",omitempty"`
	AIAt              int64    `json:",omitempty"`
	AIPeople          int      `json:",omitempty"`
	AISource          string   `json:",omitempty"` // what the model saw of a video (AIInfo.From)
	Tags              []string `json:",omitempty"` // keywords for dc:Subject
	OCR               bool     `json:",omitempty"` // text recognition ran (Text may be empty)
	Text, TextModel   string   `json:",omitempty"`
	TextAt            int64    `json:",omitempty"`
	Speech            bool     `json:",omitempty"` // speech recognition ran (SpeechText may be empty)
	SpeechText        string   `json:",omitempty"`
	SpeechLang        string   `json:",omitempty"`
	SpeechModel       string   `json:",omitempty"`
	SpeechAt          int64    `json:",omitempty"`
	People            []string `json:",omitempty"`
	W, H              int      `json:",omitempty"` // stored size, for the regions
	Regions           []xmpRegion
}

func (p *xmpPlan) empty() bool {
	return p.AIModel == "" && !p.OCR && !p.Speech && len(p.People) == 0 && len(p.Regions) == 0
}

// sig identifies a plan; "" for an empty one.
func (p *xmpPlan) sig() string {
	if p.empty() {
		return ""
	}
	b, _ := json.Marshal(p)
	h := sha1.Sum(b)
	return "v1-" + hex.EncodeToString(h[:8])
}

// xmpPlanFor collects what Schuhkarton knows about an item.
func (l *Library) xmpPlanFor(it *Item) xmpPlan {
	var p xmpPlan
	if a := l.AIOf(it); a != nil {
		p.AltDe, p.AltEn = a.De, a.En
		p.TagsDe, p.TagsEn = a.TagsDe, a.TagsEn
		p.AIModel, p.AIAt, p.AIPeople = cmp.Or(a.Model, "?"), a.At, a.People
		p.AISource = a.From
		p.Alt, p.Tags = a.De, a.TagsDe
		if l.firstLang() == "en" {
			p.Alt, p.Tags = a.En, a.TagsEn
		}
		if p.Alt == "" {
			p.Alt = cmp.Or(a.De, a.En)
		}
		if len(p.Tags) == 0 {
			p.Tags = append(a.TagsDe, a.TagsEn...)
		}
	}
	if o := l.ocrOf(it); o != nil {
		p.OCR, p.Text, p.TextModel, p.TextAt = true, o.Text, cmp.Or(o.Model, "?"), o.At
	}
	if s := l.speechOf(it); s != nil {
		p.Speech, p.SpeechText, p.SpeechLang, p.SpeechModel, p.SpeechAt = true, s.Text, s.Lang, cmp.Or(s.Model, "?"), s.At
	}
	// MWG regions are relative to the image before its orientation. HEIF
	// stores the turn as container rotation, which the decoder applies and
	// which matches the EXIF orientation other programs go by, so EXIF counts
	// here for every format (not displayOrient, which skips it for HEIF).
	o := composeOrient(it.Orient, it.Rot)
	seen := map[string]bool{}
	for i, f := range l.ItemFaces(it) {
		pr := l.Person(l.PersonOf(it.ID, i))
		if pr == nil || pr.Name == "" {
			continue
		}
		if !seen[strings.ToLower(pr.Name)] {
			seen[strings.ToLower(pr.Name)] = true
			p.People = append(p.People, pr.Name)
		}
		if it.Kind == Photo {
			b := storedBox(f, o)
			p.Regions = append(p.Regions, xmpRegion{X: round4(b.X + b.W/2), Y: round4(b.Y + b.H/2),
				W: round4(b.W), H: round4(b.H), Name: pr.Name})
		}
	}
	l.mu.RLock()
	pending := it.MetaFaces
	l.mu.RUnlock()
	if len(pending) > 0 {
		// names read from the file that wait for (or found no) detected face: keep them
		for _, mf := range pending {
			if !seen[strings.ToLower(mf.Name)] {
				seen[strings.ToLower(mf.Name)] = true
				p.People = append(p.People, mf.Name)
			}
			if it.Kind == Photo {
				b := storedBox(Face{X: mf.X, Y: mf.Y, W: mf.W, H: mf.H}, o)
				p.Regions = append(p.Regions, xmpRegion{X: round4(b.X + b.W/2), Y: round4(b.Y + b.H/2),
					W: round4(b.W), H: round4(b.H), Name: mf.Name})
			}
		}
	}
	if len(p.Regions) > 0 {
		p.W, p.H = it.W, it.H
		if o >= 5 && o <= 8 {
			p.W, p.H = it.H, it.W
		}
	}
	return p
}

func (l *Library) firstLang() string {
	if len(l.opt.AI.Languages) > 0 {
		return l.opt.AI.Languages[0]
	}
	return "de"
}

func round4(f float32) float64 { return math.Round(float64(f)*10000) / 10000 }

// storedBox turns a face box of the picture as shown back into the stored
// pixels: the inverse of orientation o (mirror first, then turn clockwise).
func storedBox(f Face, o int) Face {
	if o < 1 || o > 8 {
		o = 1
	}
	mirror, deg := orientMR[o][0] == 1, orientMR[o][1]
	pt := func(x, y float32) (float32, float32) {
		switch (360 - deg) % 360 { // turn back
		case 90:
			x, y = 1-y, x
		case 180:
			x, y = 1-x, 1-y
		case 270:
			x, y = y, 1-x
		}
		if mirror {
			x = 1 - x
		}
		return x, y
	}
	x1, y1 := pt(f.X, f.Y)
	x2, y2 := pt(f.X+f.W, f.Y+f.H)
	return Face{X: min(x1, x2), Y: min(y1, y2), W: abs32(x2 - x1), H: abs32(y2 - y1)}
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// --- reading back --------------------------------------------------------------

// adoptXMP takes what an earlier run wrote into a file: the signature, and
// descriptions and texts the cache does not have (a new cache, another
// machine).
func (l *Library) adoptXMP(it *Item, m map[string]any) {
	it.MetaSig = str(m, "SkSig")
	if it.MetaSig == "" {
		return
	}
	if model := str(m, "SkAIModel"); model != "" && l.AIOf(it) == nil {
		l.setAI(it, &AIInfo{De: str(m, "AltTextAccessibility-de"), En: str(m, "AltTextAccessibility-en"),
			TagsDe: list(m, "SkAITagsDe"), TagsEn: list(m, "SkAITagsEn"), People: int(num(m, "SkAIPeople")),
			Model: model, At: parseXMPDate(str(m, "SkAIDate")), From: str(m, "SkAISource")})
	}
	if model := str(m, "SkTextModel"); model != "" && l.ocrOf(it) == nil {
		l.setOCR(it, &OCRInfo{Text: str(m, "SkText"), Model: model, At: parseXMPDate(str(m, "SkTextDate"))})
	}
	if model := str(m, "SkSpeechModel"); model != "" && l.speechOf(it) == nil {
		l.setSpeech(it, &SpeechInfo{Text: str(m, "SkSpeech"), Lang: str(m, "SkSpeechLang"), Model: model,
			At: parseXMPDate(str(m, "SkSpeechDate"))})
	}
	it.MetaFaces = nil
	ours := map[string]bool{}
	for _, s := range list(m, "SkRegions") {
		ours[s] = true
	}
	_, regions := mwgList(m)
	o := composeOrient(it.Orient, it.Rot)
	for _, r := range regions {
		if ours[r.sig()] && r.name != "" {
			b := shownBox(r.box(), o)
			it.MetaFaces = append(it.MetaFaces, MetaFace{X: b.X, Y: b.Y, W: b.W, H: b.H, Name: r.name})
		}
	}
}

// mwgRegion is one entry of XMP-mwg-rs:RegionInfo as exiftool reads it
// with -struct: center and size relative to the stored pixels.
type mwgRegion struct {
	raw        map[string]any
	name       string
	x, y, w, h float64
}

func (r mwgRegion) sig() string { return regionSig(r.name, r.x, r.y, r.w, r.h) }

func (r mwgRegion) box() Face {
	return Face{X: float32(r.x - r.w/2), Y: float32(r.y - r.h/2), W: float32(r.w), H: float32(r.h)}
}

// regionSig identifies a region Schuhkarton wrote (XMP-schuhkarton:Regions).
func regionSig(name string, x, y, w, h float64) string {
	return fmt.Sprintf("%s|%.4f|%.4f|%.4f|%.4f", name, x, y, w, h)
}

// mwgList returns the dimensions and the regions of a file's RegionInfo.
func mwgList(m map[string]any) (dims map[string]any, out []mwgRegion) {
	ri, _ := m["RegionInfo"].(map[string]any)
	if ri == nil {
		return nil, nil
	}
	dims, _ = ri["AppliedToDimensions"].(map[string]any)
	rl, _ := ri["RegionList"].([]any)
	for _, v := range rl {
		r, _ := v.(map[string]any)
		if r == nil {
			continue
		}
		area, _ := r["Area"].(map[string]any)
		g := mwgRegion{raw: r, name: str(r, "Name")}
		if area != nil {
			g.x, g.y, g.w, g.h = num(area, "X"), num(area, "Y"), num(area, "W"), num(area, "H")
		}
		out = append(out, g)
	}
	return dims, out
}

// MetaFace is a named face from a file's metadata, as shown.
type MetaFace struct {
	X, Y, W, H float32
	Name       string
}

// applyMetaFaces names detected faces after the regions read from the file
// (the people cache was lost); names given in Schuhkarton win. It reports
// whether a name was set.
func (l *Library) applyMetaFaces(it *Item) bool {
	l.mu.RLock()
	pending, faces, done := it.MetaFaces, it.Faces, it.FacesDone
	l.mu.RUnlock()
	if len(pending) == 0 || !done {
		return false
	}
	s := l.people
	named := false
	var left []MetaFace // no face found there: kept, so the file keeps the name
	s.mu.Lock()
	for _, mf := range pending {
		best, bestIoU := -1, 0.3
		for i, f := range faces {
			if v := iou(f, Face{X: mf.X, Y: mf.Y, W: mf.W, H: mf.H}); v >= bestIoU {
				best, bestIoU = i, v
			}
		}
		if best < 0 {
			left = append(left, mf)
			continue
		}
		if k := (FaceRef{it.ID, best}).key(); s.labels[k] == "" {
			s.labels[k] = mf.Name
			named = true
		}
	}
	s.mu.Unlock()
	l.mu.Lock()
	it.MetaFaces = left
	l.mu.Unlock()
	if named {
		if err := s.save(); err != nil {
			l.logf("people.json: %v", err)
		}
	}
	return named
}

// shownBox turns a box of the stored pixels into the picture as shown with
// orientation o (mirror first, then turn clockwise); storedBox undoes it.
func shownBox(f Face, o int) Face {
	if o < 1 || o > 8 {
		o = 1
	}
	mirror, deg := orientMR[o][0] == 1, orientMR[o][1]
	pt := func(x, y float32) (float32, float32) {
		if mirror {
			x = 1 - x
		}
		switch deg {
		case 90:
			x, y = 1-y, x
		case 180:
			x, y = 1-x, 1-y
		case 270:
			x, y = y, 1-x
		}
		return x, y
	}
	x1, y1 := pt(f.X, f.Y)
	x2, y2 := pt(f.X+f.W, f.Y+f.H)
	return Face{X: min(x1, x2), Y: min(y1, y2), W: abs32(x2 - x1), H: abs32(y2 - y1)}
}

func xmpDate(t int64) string {
	if t == 0 {
		return ""
	}
	return time.Unix(t, 0).UTC().Format(time.RFC3339)
}

func parseXMPDate(s string) int64 {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return time.Now().Unix()
}

// ownKeywords removes the keywords Schuhkarton added (SkTags) from the
// keywords read from a file.
func ownKeywords(kws []string, m map[string]any) []string {
	ours := map[string]bool{}
	for _, t := range list(m, "SkTags") {
		ours[strings.ToLower(t)] = true
	}
	if len(ours) == 0 {
		return kws
	}
	var out []string
	for _, k := range kws {
		if !ours[strings.ToLower(k)] {
			out = append(out, k)
		}
	}
	return out
}

// --- the background job --------------------------------------------------------

const xmpRetryFailed = 6 * time.Hour

type xmpFailure struct {
	sig string
	at  time.Time
}

// xmpEligible: files with something to write, or with something to remove.
func (l *Library) xmpEligible(it *Item) bool {
	if !aiEligible(it) {
		return false
	}
	if it.MetaSig != "" {
		return true
	}
	p := l.xmpPlanFor(it)
	return !p.empty()
}

func (l *Library) xmpTask() jobTask {
	return jobTask{
		workers:  2,
		eligible: l.xmpEligible,
		state: func(it *Item, now time.Time) (handled, failed bool) {
			p := l.xmpPlanFor(it)
			sig := p.sig()
			if sig == it.MetaSig {
				return true, false
			}
			l.xmpMu.Lock()
			f, ok := l.xmpFails[l.favKey(it)]
			l.xmpMu.Unlock()
			if ok && f.sig == sig && now.Sub(f.at) < xmpRetryFailed {
				return true, true
			}
			return false, false
		},
		one:  l.writeXMPOne,
		save: l.closeXMPTools,
	}
}

func (l *Library) xmpLoop(ctx context.Context) { l.jobLoop(ctx, &l.xmpJob, l.xmpTask()) }

// kickXMP starts a pass a minute later: results come one by one.
func (l *Library) kickXMP() {
	if !l.xmpJob.on || !l.xmpSoon.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(time.Minute, func() {
		l.xmpSoon.Store(false)
		l.xmpJob.kick()
	})
}

// writeXMPOne writes the plan of one file.
func (l *Library) writeXMPOne(ctx context.Context, it *Item) error {
	j := &l.xmpJob
	p := l.xmpPlanFor(it)
	sig := p.sig()
	target, side := xmpTarget(l.Path(it), it.Ext())
	et, err := l.xmpTool()
	if err != nil {
		return &aiError{msg: "exiftool: " + err.Error(), fatal: true}
	}
	defer l.putXMPTool(et)
	// the share may go away for minutes (NAS under load): wait for it
	place := nasretry.In(filepath.Dir(target), l.opt.Roots[it.Root].Path)
	retry := func(fn func() error) error { return nasretry.Do(ctx, "metadata "+it.Rel, fn, place) }
	fail := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.xmpFailed(it, sig, err)
		return nil
	}
	if side {
		// an empty XMP packet to write into when there is no sidecar (newer
		// exiftool would create the file itself, older ones may not); one
		// that is there but out of reach is never replaced
		if err = retry(func() error { return createEmptyXMP(target) }); err != nil {
			return fail(err)
		}
	}
	// read what is there now (also a sidecar someone else just made) and
	// write the merge; a new try reads again, so edits made by others
	// while the share was away stay
	err = retry(func() error {
		cur, err := et.readOne(target, append([]string{"-XMP-dc:Subject", "-XMP-iptcExt:PersonInImage"}, xmpReadTags...))
		if err != nil {
			return err
		}
		args := xmpArgs(&p, sig, cur, it.Kind == Video)
		if side {
			return et.write(target, args)
		}
		return l.withRealName(it, target, func(p string) error { return et.write(p, args) })
	})
	if err != nil {
		return fail(err)
	}
	l.mu.Lock()
	it.MetaSig = sig
	l.mu.Unlock()
	if !side {
		l.afterWrite(it, 0)
	}
	l.markDirty()
	j.handled(false, 0)
	return nil
}

// createEmptyXMP creates a sidecar holding an empty XMP packet. A sidecar
// that is there already (also one that appeared meanwhile) is never
// touched.
func createEmptyXMP(target string) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(emptyXMP); err != nil {
		f.Close()
		os.Remove(target) // ours and incomplete: made again next time
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(target)
		return err
	}
	return nil
}

func (l *Library) xmpFailed(it *Item, sig string, err error) {
	l.logf("metadata %s: %v", it.Rel, err)
	l.xmpMu.Lock()
	l.xmpFails[l.favKey(it)] = xmpFailure{sig, time.Now()}
	l.xmpMu.Unlock()
	l.xmpJob.handled(true, 0)
}

// xmpArgs returns the exiftool assignments (for -E: values HTML-escaped)
// that turn the file's current XMP (cur) into the plan. Keywords, names and
// face regions of others stay; only what Schuhkarton wrote before (SkTags,
// SkPeople, SkRegions) is replaced.
func xmpArgs(p *xmpPlan, sig string, cur map[string]any, video bool) []string {
	var a []string
	set := func(tag, v string) { a = append(a, "-"+tag+"="+escE(v)) }
	// within one command exiftool adds each further -TAG=value of a list tag
	// to the list (the first replaces what was there; TestXMPWriteAndReadBack)
	setList := func(tag string, vs []string) {
		if len(vs) == 0 {
			set(tag, "")
		}
		for _, v := range vs {
			set(tag, v)
		}
	}
	// AI description
	if p.AIModel != "" || str(cur, "SkAIModel") != "" {
		set("XMP-iptcCore:AltTextAccessibility", p.Alt)
		set("XMP-iptcCore:AltTextAccessibility-de", p.AltDe)
		set("XMP-iptcCore:AltTextAccessibility-en", p.AltEn)
		setList("XMP-schuhkarton:SkAITagsDe", p.TagsDe)
		setList("XMP-schuhkarton:SkAITagsEn", p.TagsEn)
		set("XMP-schuhkarton:SkAIModel", p.AIModel)
		set("XMP-schuhkarton:SkAIDate", xmpDate(p.AIAt))
		people := ""
		if p.AIModel != "" {
			people = strconv.Itoa(p.AIPeople)
		}
		set("XMP-schuhkarton:SkAIPeople", people)
		set("XMP-schuhkarton:SkAISource", p.AISource)
	}
	// keywords: the others' stay, Schuhkarton's are replaced
	if subj, ours, ok := mergeList(list(cur, "Subject"), list(cur, "SkTags"), p.Tags); ok {
		setList("XMP-dc:Subject", subj)
		setList("XMP-schuhkarton:SkTags", ours)
	}
	// recognized text
	if p.OCR || str(cur, "SkTextModel") != "" {
		set("XMP-schuhkarton:SkText", p.Text)
		set("XMP-schuhkarton:SkTextModel", p.TextModel)
		set("XMP-schuhkarton:SkTextDate", xmpDate(p.TextAt))
	}
	// spoken text
	if p.Speech || str(cur, "SkSpeechModel") != "" {
		set("XMP-schuhkarton:SkSpeech", p.SpeechText)
		set("XMP-schuhkarton:SkSpeechLang", p.SpeechLang)
		set("XMP-schuhkarton:SkSpeechModel", p.SpeechModel)
		set("XMP-schuhkarton:SkSpeechDate", xmpDate(p.SpeechAt))
	}
	// people
	if names, ours, ok := mergeList(list(cur, "PersonInImage"), list(cur, "SkPeople"), p.People); ok {
		setList("XMP-iptcExt:PersonInImage", names)
		setList("XMP-schuhkarton:SkPeople", ours)
	}
	// face regions: the others' stay, Schuhkarton's are replaced
	if !video {
		if info, sigs, ok := mergeRegions(p, cur); ok {
			if info == "" {
				set("XMP-mwg-rs:RegionInfo", "")
			} else {
				a = append(a, "-XMP-mwg-rs:RegionInfo="+escE(info))
			}
			setList("XMP-schuhkarton:SkRegions", sigs)
		}
	}
	set("XMP-schuhkarton:SkSig", sig)
	return a
}

// mergeRegions returns the new RegionInfo structure ("": none) and the
// signatures of Schuhkarton's regions in it. Regions of other programs stay;
// a face they marked already is not marked twice. ok is false when nothing
// changes.
func mergeRegions(p *xmpPlan, cur map[string]any) (info string, sigs []string, ok bool) {
	dims, have := mwgList(cur)
	old := list(cur, "SkRegions")
	ours := map[string]bool{}
	for _, s := range old {
		ours[s] = true
	}
	var keep []mwgRegion
	for _, r := range have {
		if !ours[r.sig()] {
			keep = append(keep, r)
		}
	}
	var mine []xmpRegion
	for _, r := range p.Regions {
		f := Face{X: float32(r.X - r.W/2), Y: float32(r.Y - r.H/2), W: float32(r.W), H: float32(r.H)}
		marked := false
		for _, k := range keep {
			marked = marked || iou(k.box(), f) > 0.5
		}
		if !marked {
			mine = append(mine, r)
			sigs = append(sigs, regionSig(r.Name, r.X, r.Y, r.W, r.H))
		}
	}
	var haveSigs, wantSigs []string
	for _, r := range have {
		haveSigs = append(haveSigs, r.sig())
	}
	for _, r := range keep {
		wantSigs = append(wantSigs, r.sig())
	}
	wantSigs = append(wantSigs, sigs...)
	if slices.Equal(haveSigs, wantSigs) && slices.Equal(sigs, old) {
		return "", nil, false
	}
	if len(keep)+len(mine) == 0 {
		return "", nil, true
	}
	var items []string
	for _, r := range keep {
		items = append(items, structValue(r.raw))
	}
	for _, r := range mine {
		items = append(items, fmt.Sprintf("{Area={X=%s,Y=%s,W=%s,H=%s,Unit=normalized},Name=%s,Type=Face}",
			ftoa(r.X), ftoa(r.Y), ftoa(r.W), ftoa(r.H), escStruct(r.Name)))
	}
	d := fmt.Sprintf("{W=%d,H=%d,Unit=pixel}", p.W, p.H)
	if len(mine) == 0 && dims != nil {
		d = structValue(dims)
	}
	return "{AppliedToDimensions=" + d + ",RegionList=[" + strings.Join(items, ",") + "]}", sigs, true
}

// structValue writes a value read with -struct back in exiftool's
// structure syntax.
func structValue(v any) string {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+structValue(v[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, 0, len(v))
		for _, x := range v {
			parts = append(parts, structValue(x))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case float64:
		return ftoa(v)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	}
	return escStruct(fmt.Sprint(v))
}

// mergeList replaces the entries Schuhkarton added before (old) with want,
// keeping everybody else's. ok is false when nothing changes.
func mergeList(have, old, want []string) (all, ours []string, ok bool) {
	drop := map[string]bool{}
	for _, v := range old {
		drop[strings.ToLower(v)] = true
	}
	seen := map[string]bool{}
	for _, v := range have {
		if !drop[strings.ToLower(v)] && !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			all = append(all, v)
		}
	}
	for _, v := range want {
		if v = strings.TrimSpace(v); v != "" && !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			all = append(all, v)
			ours = append(ours, v)
		}
	}
	return all, ours, !slices.Equal(all, have) || !slices.Equal(ours, old)
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// escStruct escapes a value inside an exiftool structure.
func escStruct(s string) string {
	var b strings.Builder
	for i, r := range s {
		if strings.ContainsRune(",}]|", r) || (i == 0 && strings.ContainsRune("[{ ", r)) {
			b.WriteByte('|')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escE escapes a value for exiftool -E in an argument file: entities for
// &, <, > and line breaks, and spaces at the ends that would get lost.
func escE(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r\n", "&#xa;", "\n", "&#xa;", "\r", "&#xa;").Replace(s)
	lead := len(s) - len(strings.TrimLeft(s, " \t"))
	trail := len(s) - len(strings.TrimRight(s, " \t"))
	if lead == 0 && trail == 0 {
		return s
	}
	if lead == len(s) {
		return strings.Repeat("&#x20;", len(s))
	}
	return strings.Repeat("&#x20;", lead) + s[lead:len(s)-trail] + strings.Repeat("&#x20;", trail)
}

// emptyXMP starts a sidecar.
const emptyXMP = `<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?>
<x:xmpmeta xmlns:x='adobe:ns:meta/'>
</x:xmpmeta>
<?xpacket end='w'?>
`

// --- exiftool processes for writing -----------------------------------------------

func (l *Library) xmpTool() (*exifTool, error) {
	l.xmpMu.Lock()
	if n := len(l.xmpIdle); n > 0 {
		et := l.xmpIdle[n-1]
		l.xmpIdle = l.xmpIdle[:n-1]
		l.xmpMu.Unlock()
		return et, nil
	}
	l.xmpMu.Unlock()
	return startExifTool(l.opt.ExifTool, l.exifCfg)
}

func (l *Library) putXMPTool(et *exifTool) {
	l.xmpMu.Lock()
	defer l.xmpMu.Unlock()
	l.xmpIdle = append(l.xmpIdle, et)
}

// closeXMPTools ends the exiftool processes after a pass.
func (l *Library) closeXMPTools() {
	l.xmpMu.Lock()
	idle := l.xmpIdle
	l.xmpIdle = nil
	l.xmpMu.Unlock()
	for _, et := range idle {
		et.close()
	}
}

// readOne returns the tags of one file.
func (e *exifTool) readOne(path string, tags []string) (map[string]any, error) {
	out, _, err := e.run(append(append([]string{"-json", "-n", "-charset", "filename=utf8", "-m", "-api", "LargeFileSupport=1"}, tags...), path))
	if err != nil {
		return nil, err
	}
	var arr []map[string]any
	if strings.TrimSpace(out) == "" {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal([]byte(out), &arr); err != nil || len(arr) == 0 {
		return nil, fmt.Errorf("exiftool: %v", err)
	}
	return arr[0], nil
}

// write applies assignments (escaped for -E) to one file, keeping its time.
// An exiftool that was stopped mid-write (Schuhkarton restarted) leaves
// "<file>_exiftool_tmp" behind, and exiftool then refuses the file; such a
// leftover that nobody writes to any more goes, and the write is tried again.
func (e *exifTool) write(path string, args []string) error {
	err := e.writeOnce(path, args)
	if err != nil && strings.Contains(err.Error(), "Temporary file already exists") && removeStaleTmp(path+"_exiftool_tmp") {
		err = e.writeOnce(path, args)
	}
	return err
}

// staleTmpAge: a temporary file not written to for this long is a leftover
// (a running write keeps changing it).
var staleTmpAge = 2 * time.Minute

func removeStaleTmp(p string) bool {
	st, err := os.Stat(p)
	if err != nil || time.Since(st.ModTime()) < staleTmpAge {
		return false
	}
	return os.Remove(p) == nil
}

func (e *exifTool) writeOnce(path string, args []string) error {
	full := append([]string{"-E", "-m", "-overwrite_original", "-P", "-charset", "filename=utf8", "-api", "LargeFileSupport=1"}, args...)
	out, stderr, err := e.run(append(full, path))
	if err != nil {
		return err
	}
	for _, ok := range []string{"1 image files updated", "1 image files created", "1 image files unchanged"} {
		if strings.Contains(out, ok) {
			return nil
		}
	}
	msg := firstLine([]byte(stderr))
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Error") {
			msg = strings.TrimSpace(line)
			break
		}
	}
	if msg == "" {
		msg = "nicht geschrieben"
	}
	return errors.New(msg)
}

// run sends one command to the running exiftool and returns its output.
func (e *exifTool) run(args []string) (stdout, stderr string, err error) {
	var b bytes.Buffer
	for _, a := range args {
		if strings.ContainsAny(a, "\n\r") {
			return "", "", fmt.Errorf("line break in argument %q", a)
		}
		b.WriteString(a + "\n")
	}
	b.WriteString("-execute\n")
	e.errBuf.reset()
	if _, err := e.in.Write(b.Bytes()); err != nil {
		return "", "", err
	}
	var out strings.Builder
	for {
		line, err := e.out.ReadString('\n')
		if err != nil {
			return out.String(), e.errBuf.take(), err
		}
		if strings.HasPrefix(line, "{ready}") {
			break
		}
		out.WriteString(line)
	}
	s := e.errBuf.take()
	if s == "" && !strings.Contains(out.String(), "image files") && !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		time.Sleep(20 * time.Millisecond) // the error text may still be on its way
		s = e.errBuf.take()
	}
	return out.String(), s, nil
}

// syncBuffer collects exiftool's error output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.b.Len() < 64<<10 {
		s.b.Write(p)
	}
	return len(p), nil
}

func (s *syncBuffer) reset() {
	s.mu.Lock()
	s.b.Reset()
	s.mu.Unlock()
}

func (s *syncBuffer) take() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.b.String()
	s.b.Reset()
	return out
}
