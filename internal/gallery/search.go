package gallery

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// fold lowercases and strips accents so "Lübbecke" matches "lubbecke".
func fold(s string) string {
	f, _ := foldMap(s)
	return f
}

var foldTable = map[rune]string{
	'ä': "a", 'á': "a", 'à': "a", 'â': "a", 'ã': "a", 'å': "a", 'æ': "ae",
	'ö': "o", 'ó': "o", 'ò': "o", 'ô': "o", 'õ': "o", 'ø': "o", 'œ': "oe",
	'ü': "u", 'ú': "u", 'ù': "u", 'û': "u",
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i",
	'ç': "c", 'ñ': "n", 'ß': "ss", 'ý': "y", 'ÿ': "y", 'š': "s", 'ž': "z", 'č': "c", 'ł': "l",
}

// foldMap folds s and returns, for every byte of the result, the byte offset
// in s it came from.
func foldMap(s string) (string, []int) {
	var b strings.Builder
	b.Grow(len(s))
	idx := make([]int, 0, len(s))
	for i, r := range s {
		r = unicode.ToLower(r)
		if rep, ok := foldTable[r]; ok {
			b.WriteString(rep)
			for range len(rep) {
				idx = append(idx, i)
			}
			continue
		}
		if unicode.IsSpace(r) {
			r = ' '
		}
		n := utf8.RuneLen(r)
		if n < 0 {
			continue
		}
		b.WriteRune(r)
		for range n {
			idx = append(idx, i)
		}
	}
	return b.String(), idx
}

// germanVariants turns "ue" into "u" etc., so "luebbecke" finds "Lübbecke".
func germanVariants(tok string) []string {
	out := []string{tok}
	if r := strings.NewReplacer("ae", "a", "oe", "o", "ue", "u").Replace(tok); r != tok {
		out = append(out, r)
	}
	return out
}

var spaces = regexp.MustCompile(`\s+`)

// timeOfDay names the part of the day (only when the time is known).
func timeOfDay(h int) string {
	switch {
	case h >= 5 && h < 11:
		return "morgens vormittags"
	case h >= 11 && h < 14:
		return "mittags"
	case h >= 14 && h < 18:
		return "nachmittags"
	case h >= 18 && h < 22:
		return "abends"
	}
	return "nachts"
}

// seasons by month (meteorological), folded for search
var seasons = []string{"", "winter", "winter", "fruhling", "fruhling", "fruhling", "sommer", "sommer", "sommer", "herbst", "herbst", "herbst", "winter"}

type doc struct {
	name, album, place, cat, date, cam, kw, desc, kind string
	notes                                              string // notes by people with an account
	aiTags, aiDesc                                     string // AI tags (padded with spaces) and descriptions, both languages
}

func (d *doc) setAI(a *AIInfo) {
	d.aiTags, d.aiDesc = "", ""
	if a.ok() {
		d.aiTags = " " + fold(strings.Join(append(append([]string(nil), a.TagsDe...), a.TagsEn...), " ")) + " "
		d.aiDesc = fold(a.De + " " + a.En)
	}
}

type searchIndex struct {
	mu      sync.Mutex
	ver     string
	aiVer   int64
	noteVer int64
	ocrVer  int64
	spVer   int64 // speech
	docs    map[string]*doc
	tags    map[string]map[string]int // language -> AI tag -> photos
	facets  []facet
	builtAt time.Time
}

type facet struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	Query string `json:"query"`
	Count int    `json:"count"`
	f     string
}

func (l *Library) buildDoc(it *Item) *doc {
	t := time.Unix(it.Taken, 0).UTC()
	weekend := t.Weekday() == 0 || t.Weekday() == 6
	daykind := "werktag"
	if weekend {
		daykind = "wochenende"
	}
	date := fmt.Sprintf("%d %s %s %s %s %02d.%02d.%d %d-%02d-%02d", t.Year(), fold(monthsDE[t.Month()]), seasons[t.Month()],
		fold(weekdaysDE[t.Weekday()]), daykind, t.Day(), int(t.Month()), t.Year(), t.Year(), int(t.Month()), t.Day())
	if it.TakenSrc == "exif" || it.TakenSrc == "edit" || (it.TakenSrc == "name" && t.Hour()+t.Minute()+t.Second() != 12) {
		date += " " + timeOfDay(t.Hour())
	}
	kind := "foto bild"
	if it.Kind == Video {
		kind = "video film"
	}
	if it.LiveVideo != "" {
		kind += " live"
	}
	name := strings.ToLower(path.Base(it.Rel))
	if strings.Contains(name, "screenshot") || strings.Contains(name, "bildschirmfoto") {
		kind += " screenshot"
	}
	if it.Aspect > 2.2 || (it.Aspect > 0 && it.Aspect < 0.45) {
		kind += " panorama"
	}
	if strings.Contains(strings.ToLower(it.Lens), "front") {
		kind += " selfie"
	}
	if it.GPS {
		kind += " gps ort"
	}
	ocrText, spoken := l.OCRText(it), l.SpeechText(it)
	if it.Desc != "" || ocrText != "" {
		kind += " text"
	}
	if spoken != "" {
		kind += " gesprochen spoken"
	}
	if l.IsDocument(it) {
		kind += " dokument document"
	}
	notes := l.noteText(it)
	if notes != "" {
		kind += " notiz note notizen notes"
	}
	d := &doc{
		name:  fold(name),
		album: fold(l.opt.Roots[it.Root].Name + "/" + it.Album),
		place: fold(it.Place),
		cat:   fold(it.Category),
		date:  date,
		cam:   fold(it.Make + " " + it.Model + " " + it.Lens),
		kw:    fold(strings.Join(it.Keywords, " ")),
		desc:  fold(joinText(it.Desc, ocrText, spoken)),
		kind:  kind,
		notes: fold(notes),
	}
	d.setAI(l.AIOf(it))
	return d
}

// joinText is the text of a file: its description from the file, the
// recognized text and what was said.
func joinText(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// updateText puts newly recognized text into the index without rebuilding
// it: desc, recognized text, spoken text.
func (si *searchIndex) updateText(id, desc, ocr, spoken string) {
	si.mu.Lock()
	defer si.mu.Unlock()
	if d := si.docs[id]; d != nil {
		d.desc = fold(joinText(desc, ocr, spoken))
		if ocr != "" && !strings.Contains(d.kind, " text") {
			d.kind += " text"
		}
		if spoken != "" && !strings.Contains(d.kind, " gesprochen") {
			d.kind += " gesprochen spoken"
		}
	}
}

// updateAI puts a new description into the index without rebuilding it.
func (si *searchIndex) updateAI(id string, a *AIInfo) {
	si.mu.Lock()
	defer si.mu.Unlock()
	if d := si.docs[id]; d != nil {
		d.setAI(a)
	}
}

func (l *Library) index() *searchIndex {
	si := &l.search
	si.mu.Lock()
	defer si.mu.Unlock()
	items := l.Items()
	ver := fmt.Sprintf("%d:%p", len(items), items)
	aiVer := l.ai.ver.Load()
	noteVer := l.notes.ver.Load()
	ocrVer := l.ocr.ver.Load()
	spVer := l.speech.ver.Load()
	// new descriptions reach the docs right away (updateAI); the tag list
	// for suggestions is refreshed at most once a minute. Notes count at once.
	// new texts reach the docs right away too (updateText); forgotten ones
	// (recognize again) need a rebuild, also at most once a minute.
	if si.ver == ver && si.noteVer == noteVer && (si.aiVer == aiVer || time.Since(si.builtAt) < time.Minute) &&
		((si.ocrVer == ocrVer && si.spVer == spVer) || time.Since(si.builtAt) < time.Minute) {
		return si
	}
	docs := make(map[string]*doc, len(items))
	places, cats, cams, years := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	tags := map[string]map[string]int{"de": {}, "en": {}}
	for _, it := range items {
		docs[it.ID] = l.buildDoc(it)
		if it.LivePhoto != "" {
			continue
		}
		if a := l.AIOf(it); a != nil {
			for _, t := range a.TagsDe {
				tags["de"][t]++
			}
			for _, t := range a.TagsEn {
				tags["en"][t]++
			}
		}
		if it.Place != "" {
			places[it.Place]++
		}
		if it.Category != "" {
			cats[it.Category]++
		}
		if c := strings.TrimSpace(it.Make + " " + it.Model); c != "" {
			cams[c]++
		}
		years[strconv.Itoa(time.Unix(it.Taken, 0).UTC().Year())]++
	}
	var facets []facet
	add := func(m map[string]int, typ, prefix string) {
		for k, n := range m {
			q := k
			if strings.ContainsRune(k, ' ') {
				q = `"` + k + `"`
			}
			facets = append(facets, facet{Type: typ, Label: k, Query: prefix + q, Count: n, f: fold(k)})
		}
	}
	add(places, "ort", "ort:")
	add(cats, "kategorie", "")
	add(cams, "kamera", "kamera:")
	add(years, "jahr", "jahr:")
	sort.Slice(facets, func(a, b int) bool { return facets[a].Count > facets[b].Count })
	si.docs, si.facets, si.ver, si.builtAt, si.aiVer, si.tags = docs, facets, ver, time.Now(), aiVer, tags
	si.noteVer, si.ocrVer, si.spVer = noteVer, ocrVer, spVer
	return si
}

// Hit is one search result.
type Hit struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
	Snip  string  `json:"snip,omitempty"`
}

type term struct {
	field string
	words []string // alternatives
	neg   bool
}

func tokenize(q string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range q {
		switch {
		case r == '"':
			inQ = !inQ
		case unicode.IsSpace(r) && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var fieldAlias = map[string]string{
	"ort": "place", "place": "place", "person": "person", "wer": "person", "jahr": "year", "year": "year",
	"monat": "month", "typ": "kind", "type": "kind", "kamera": "cam", "camera": "cam", "album": "album",
	"wochentag": "date", "tag": "date", "jahreszeit": "date", "tageszeit": "date", "zeit": "date",
	"ordner": "album", "text": "desc", "datum": "date", "date": "date", "kategorie": "cat", "datei": "name",
	// English
	"who": "person", "month": "month", "kind": "kind", "folder": "album", "category": "cat", "file": "name",
	"weekday": "date", "day": "date", "season": "date", "time": "date",
	// AI descriptions and tags
	"ki": "ai", "ai": "ai", "beschreibung": "ai", "description": "ai",
	// notes by people with an account
	"notiz": "note", "notizen": "note", "note": "note", "notes": "note",
}

// enWords maps English search words to the German words the index holds
// (folded). They are added as alternatives, so "summer" also finds the
// word itself in picture texts.
var enWords = map[string]string{
	"january": "januar", "february": "februar", "march": "marz", "may": "mai", "june": "juni", "july": "juli",
	"october": "oktober", "december": "dezember",
	"spring": "fruhling", "summer": "sommer", "autumn": "herbst", "fall": "herbst",
	"monday": "montag", "tuesday": "dienstag", "wednesday": "mittwoch", "thursday": "donnerstag", "friday": "freitag",
	"saturday": "samstag", "sunday": "sonntag", "weekend": "wochenende", "weekday": "werktag", "workday": "werktag",
	"morning": "morgens", "noon": "mittags", "midday": "mittags", "afternoon": "nachmittags", "evening": "abends", "night": "nachts",
	"photo": "foto", "photos": "foto", "picture": "bild", "pictures": "bild", "videos": "video", "movie": "film",
	"favorite": "favorit", "favorites": "favorit", "favourite": "favorit", "favourites": "favorit", "location": "gps",
	"selfies": "selfie", "screenshots": "screenshot", "panoramas": "panorama",
}

func parseQuery(q string) []term {
	var terms []term
	for _, tok := range tokenize(q) {
		t := term{}
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			t.neg, tok = true, tok[1:]
		}
		if i := strings.IndexByte(tok, ':'); i > 0 {
			if f, ok := fieldAlias[strings.ToLower(tok[:i])]; ok {
				t.field, tok = f, tok[i+1:]
			}
		}
		w := fold(strings.TrimSpace(tok))
		if w == "" {
			continue
		}
		if t.field == "month" {
			if n := atoi(w); n >= 1 && n <= 12 {
				w = fold(monthsDE[n])
			}
		}
		t.words = germanVariants(w)
		if de, ok := enWords[w]; ok {
			t.words = append(t.words, de)
		}
		terms = append(terms, t)
	}
	return terms
}

// Search finds items matching q. Plain words search everything; prefixes
// (ort:, person:, jahr:, monat:, typ:, kamera:, album:, text:, datum:) narrow
// the field, a leading "-" excludes. user's favorites match typ:favorit;
// lang picks the language of snippets from AI descriptions.
func (l *Library) Search(q, user, lang string) []Hit {
	terms := parseQuery(q)
	if len(terms) == 0 {
		return nil
	}
	si := l.index()
	groups := map[string][]term{}
	var fieldOrder []string
	for _, t := range terms {
		if t.field != "" && !t.neg {
			if groups[t.field] == nil {
				fieldOrder = append(fieldOrder, t.field)
			}
			groups[t.field] = append(groups[t.field], t)
		}
	}
	// people names per item (people change without a rescan)
	people := map[string]string{}
	for _, p := range l.People() {
		if p.Name == "" {
			continue
		}
		fn := fold(p.Name)
		for _, id := range p.Items {
			people[id] += " " + fn
		}
	}
	var hits []Hit
	for _, it := range l.Items() {
		if it.LivePhoto != "" {
			continue
		}
		d := si.docs[it.ID]
		if d == nil {
			continue
		}
		score, ok := 0.0, true
		var snipWord string
		extra := ""
		if l.IsFav(user, it) {
			extra = " favorit favoriten"
		}
		match := func(t term) (float64, string) {
			if t.field == "kind" && extra != "" && contains(extra, t.words) {
				return 5, ""
			}
			return matchTerm(d, people[it.ID], it, t)
		}
		// plain words and exclusions: all must hold
		for _, t := range terms {
			if t.field != "" && !t.neg {
				continue
			}
			s, sw := match(t)
			if t.neg {
				if s > 0 {
					ok = false
					break
				}
				continue
			}
			if s == 0 {
				ok = false
				break
			}
			score += s
			if sw != "" && snipWord == "" {
				snipWord = sw
			}
		}
		// filters: one value per field must match (ort:A ort:B = A or B)
		for _, field := range fieldOrder {
			if !ok {
				break
			}
			group := groups[field]
			if len(group) == 0 {
				continue
			}
			best := 0.0
			for _, t := range group {
				s, sw := match(t)
				if s > best {
					best = s
				}
				if s > 0 && sw != "" && snipWord == "" {
					snipWord = sw
				}
			}
			if best == 0 {
				ok = false
			}
			score += best
		}
		if !ok {
			continue
		}
		h := Hit{ID: it.ID, Score: score}
		if snipWord != "" {
			h.Snip = snippet(joinText(it.Desc, l.OCRText(it), l.SpeechText(it)), snipWord)
			if h.Snip == "" && d.notes != "" {
				h.Snip = snippet(l.noteText(it), snipWord)
			}
			if h.Snip == "" && d.aiDesc != "" {
				if a := l.AIOf(it); a != nil {
					first, second := a.De, a.En
					if lang == "en" {
						first, second = second, first
					}
					if h.Snip = snippet(first, snipWord); h.Snip == "" {
						h.Snip = snippet(second, snipWord)
					}
				}
			}
		}
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	return hits
}

func contains(field string, words []string) bool {
	for _, w := range words {
		if strings.Contains(field, w) {
			return true
		}
	}
	return false
}

// wordStart rewards matches at the beginning of a word.
func wordStart(field string, words []string) bool {
	for _, w := range words {
		for i := 0; ; {
			j := strings.Index(field[i:], w)
			if j < 0 {
				break
			}
			j += i
			if j == 0 || !unicode.IsLetter(rune(field[j-1])) {
				return true
			}
			i = j + 1
		}
	}
	return false
}

func matchTerm(d *doc, people string, it *Item, t term) (float64, string) {
	w := t.words
	switch t.field {
	case "place":
		if contains(d.place, w) {
			return 10, ""
		}
		return 0, ""
	case "person":
		if contains(people, w) {
			return 10, ""
		}
		return 0, ""
	case "year":
		if strconv.Itoa(time.Unix(it.Taken, 0).UTC().Year()) == w[0] {
			return 5, ""
		}
		return 0, ""
	case "month", "date":
		if wordStart(d.date, w) {
			return 5, ""
		}
		return 0, ""
	case "kind":
		if contains(d.kind, w) {
			return 5, ""
		}
		return 0, ""
	case "cam":
		if contains(d.cam, w) {
			return 5, ""
		}
		return 0, ""
	case "album":
		if contains(d.album, w) {
			return 6, ""
		}
		return 0, ""
	case "cat":
		if contains(d.cat, w) || contains(d.kw, w) {
			return 6, ""
		}
		return 0, ""
	case "name":
		if contains(d.name, w) {
			return 5, ""
		}
		return 0, ""
	case "desc":
		if contains(d.desc, w) {
			s := 3.0
			if wordStart(d.desc, w) {
				s += 1
			}
			return s, w[0]
		}
		return 0, ""
	case "note":
		if contains(d.notes, w) {
			s := 3.0
			if wordStart(d.notes, w) {
				s++
			}
			return s, firstIn(d.notes, w)
		}
		return 0, ""
	case "ai":
		if s := aiScore(d, w); s > 0 {
			return s + 1, firstIn(d.aiDesc, w)
		}
		return 0, ""
	}
	s := 0.0
	var sw string
	if contains(d.place, w) {
		s += 10
	}
	if contains(people, w) {
		s += 10
	}
	if contains(d.cat, w) || contains(d.kw, w) {
		s += 7
	}
	if contains(d.album, w) {
		s += 4
	}
	if wordStart(d.date, w) || contains(d.kind, w) {
		s += 4
	}
	if contains(d.cam, w) {
		s += 3
	}
	if contains(d.name, w) {
		s += 3
	}
	if contains(d.desc, w) {
		s += 2
		if wordStart(d.desc, w) {
			s += 1
		}
		for _, x := range w {
			if strings.Contains(d.desc, x) {
				sw = x
				break
			}
		}
	}
	if contains(d.notes, w) {
		s += 2
		if wordStart(d.notes, w) {
			s++
		}
		if sw == "" {
			sw = firstIn(d.notes, w)
		}
	}
	// descriptions by the AI count less than what people sorted and wrote
	if a := aiScore(d, w); a > 0 {
		s += a
		if sw == "" {
			sw = firstIn(d.aiDesc, w)
		}
	}
	return s, sw
}

// aiScore: a tag (whole word or word start) or a word of the AI description.
func aiScore(d *doc, w []string) float64 {
	for _, x := range w {
		if strings.Contains(d.aiTags, " "+x+" ") {
			return 3
		}
	}
	if wordStart(d.aiTags, w) {
		return 2
	}
	if wordStart(d.aiDesc, w) {
		return 1.5
	}
	if contains(d.aiDesc, w) {
		return 1
	}
	return 0
}

func firstIn(field string, w []string) string {
	for _, x := range w {
		if strings.Contains(field, x) {
			return x
		}
	}
	return ""
}

// snippet returns ~160 characters of text around the first match, with the
// match wrapped in \x01 ... \x02.
func snippet(text, word string) string {
	f, idx := foldMap(text)
	j := strings.Index(f, word)
	if j < 0 || j >= len(idx) {
		return ""
	}
	start, end := idx[j], len(text)
	if e := j + len(word); e < len(idx) {
		end = idx[e]
	}
	from := max(0, start-70)
	to := min(len(text), end+90)
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	out := spaces.ReplaceAllString(text[from:start], " ") + "\x01" + text[start:end] + "\x02" + spaces.ReplaceAllString(text[end:to], " ")
	out = strings.TrimSpace(out)
	if from > 0 {
		out = "… " + out
	}
	if to < len(text) {
		out += " …"
	}
	return out
}

// Suggest returns places, people, categories, cameras and years starting
// with (or containing) the prefix.
// Suggest completes a search word: people, places, categories, cameras,
// years and date words. lang "en" suggests English words and syntax.
func (l *Library) Suggest(prefix, lang string) []facet {
	p := fold(strings.TrimSpace(prefix))
	if p == "" {
		return nil
	}
	si := l.index()
	var out, weak []facet
	for _, per := range l.People() {
		if per.Name == "" {
			continue
		}
		fn := fold(per.Name)
		f := facet{Type: "person", Label: per.Name, Query: "person:" + quoteIf(per.Name), Count: per.Count}
		if strings.HasPrefix(fn, p) {
			out = append(out, f)
		} else if strings.Contains(fn, p) {
			weak = append(weak, f)
		}
	}
	for _, f := range si.facets {
		if strings.HasPrefix(f.f, p) {
			out = append(out, f)
		} else if strings.Contains(f.f, " "+p) || (len(p) > 2 && strings.Contains(f.f, p)) {
			weak = append(weak, f)
		}
	}
	// AI tags in the viewer's language that several photos share
	tl := "de"
	if lang == "en" {
		tl = "en"
	}
	var tagHits []facet
	for tag, n := range si.tags[tl] {
		if n >= 2 && strings.HasPrefix(fold(tag), p) {
			tagHits = append(tagHits, facet{Type: "tag", Label: tag, Query: quoteIf(tag), Count: n})
		}
	}
	sort.Slice(tagHits, func(a, b int) bool { return tagHits[a].Count > tagHits[b].Count })
	out = append(out, tagHits[:min(len(tagHits), 4)]...)
	months, words := monthsDE, dateWords
	if lang == "en" {
		months, words = monthsEN, dateWordsEN
	}
	for m := 1; m <= 12; m++ {
		if strings.HasPrefix(fold(months[m]), p) {
			out = append(out, facet{Type: "monat", Label: months[m], Query: localize("monat:", lang) + strings.ToLower(months[m])})
		}
	}
	for _, d := range words {
		if strings.HasPrefix(fold(d.label), p) {
			out = append(out, facet{Type: d.typ, Label: d.label, Query: d.query})
		}
	}
	out = append(out, weak...)
	if len(out) > 12 {
		out = out[:12]
	}
	for i := range out {
		out[i].Query = localizeQuery(out[i].Query, lang)
	}
	return out
}

var monthsEN = []string{"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

var dateWordsEN = []struct{ typ, label, query string }{
	{"wochentag", "Monday", "weekday:monday"}, {"wochentag", "Tuesday", "weekday:tuesday"},
	{"wochentag", "Wednesday", "weekday:wednesday"}, {"wochentag", "Thursday", "weekday:thursday"},
	{"wochentag", "Friday", "weekday:friday"}, {"wochentag", "Saturday", "weekday:saturday"},
	{"wochentag", "Sunday", "weekday:sunday"}, {"wochentag", "Weekend", "day:weekend"},
	{"wochentag", "Workday", "day:workday"},
	{"jahreszeit", "Spring", "season:spring"}, {"jahreszeit", "Summer", "season:summer"},
	{"jahreszeit", "Autumn", "season:autumn"}, {"jahreszeit", "Winter", "season:winter"},
	{"tageszeit", "Morning", "time:morning"}, {"tageszeit", "Noon", "time:noon"},
	{"tageszeit", "Afternoon", "time:afternoon"}, {"tageszeit", "Evening", "time:evening"},
	{"tageszeit", "Night", "time:night"},
}

// prefixEN translates the German field prefixes of generated queries.
var prefixEN = map[string]string{"ort:": "place:", "kamera:": "camera:", "jahr:": "year:", "monat:": "month:", "kategorie:": "category:"}

func localize(prefix, lang string) string {
	if lang == "en" {
		if p, ok := prefixEN[prefix]; ok {
			return p
		}
	}
	return prefix
}

// localizeQuery rewrites a generated query ("ort:Lübbecke") for lang.
func localizeQuery(q, lang string) string {
	if lang != "en" {
		return q
	}
	if i := strings.IndexByte(q, ':'); i > 0 {
		if p, ok := prefixEN[q[:i+1]]; ok {
			return p + q[i+1:]
		}
	}
	return q
}

var dateWords = []struct{ typ, label, query string }{
	{"wochentag", "Montag", "wochentag:montag"}, {"wochentag", "Dienstag", "wochentag:dienstag"},
	{"wochentag", "Mittwoch", "wochentag:mittwoch"}, {"wochentag", "Donnerstag", "wochentag:donnerstag"},
	{"wochentag", "Freitag", "wochentag:freitag"}, {"wochentag", "Samstag", "wochentag:samstag"},
	{"wochentag", "Sonntag", "wochentag:sonntag"}, {"wochentag", "Wochenende", "tag:wochenende"},
	{"wochentag", "Werktag", "tag:werktag"},
	{"jahreszeit", "Frühling", "jahreszeit:frühling"}, {"jahreszeit", "Sommer", "jahreszeit:sommer"},
	{"jahreszeit", "Herbst", "jahreszeit:herbst"}, {"jahreszeit", "Winter", "jahreszeit:winter"},
	{"tageszeit", "Morgens", "tageszeit:morgens"}, {"tageszeit", "Mittags", "tageszeit:mittags"},
	{"tageszeit", "Nachmittags", "tageszeit:nachmittags"}, {"tageszeit", "Abends", "tageszeit:abends"},
	{"tageszeit", "Nachts", "tageszeit:nachts"},
}

func quoteIf(s string) string {
	if strings.ContainsRune(s, ' ') {
		return `"` + s + `"`
	}
	return s
}

// Facet lists for the filter panel.
type Facets struct {
	Years      []facet `json:"years"`
	Places     []facet `json:"places"`
	Cameras    []facet `json:"cameras"`
	Categories []facet `json:"categories"`
}

// Facets returns the values the filter panel offers, most items first
// (years newest first).
func (l *Library) Facets(lang string) Facets {
	si := l.index()
	var f Facets
	for _, x := range si.facets {
		x.Query = localizeQuery(x.Query, lang)
		switch x.Type {
		case "jahr":
			f.Years = append(f.Years, x)
		case "ort":
			f.Places = append(f.Places, x)
		case "kamera":
			f.Cameras = append(f.Cameras, x)
		case "kategorie":
			f.Categories = append(f.Categories, x)
		}
	}
	sort.Slice(f.Years, func(a, b int) bool { return f.Years[a].Label > f.Years[b].Label })
	if len(f.Cameras) > 20 {
		f.Cameras = f.Cameras[:20]
	}
	return f
}
