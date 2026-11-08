package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// OCROptions configure text recognition by a model made for it (GLM-OCR at
// mittwald AI hosting, or any vision model) behind an OpenAI-compatible
// API. An empty Model or Scope switches it off.
type OCROptions struct {
	URL       string // API base, e.g. https://llm.aihosting.mittwald.de/v1
	Key       string
	Model     string // e.g. GLM-OCR
	Workers   int
	Scope     string // "documents": files in documents folders; "all": every photo
	ImageSize int    // long edge of the image sent (0: 2048)
	// Translate is the model that translates the text into Languages (the
	// one of the descriptions); empty: the text is not translated.
	Translate string
	Languages []string // de, en
}

// OCRInfo is the text recognized in one picture. It lives in ocr.json,
// keyed like favorites, so it follows moves and edits.
type OCRInfo struct {
	Text   string `json:"text,omitempty"` // empty: no text in the picture
	Model  string `json:"model,omitempty"`
	Tokens int    `json:"tokens,omitempty"`
	At     int64  `json:"at"`
	// Err is the last failure for this file; it is tried again after ai.retry_failed.
	Err string `json:"err,omitempty"`
	// Lang is the language Text is written in (ISO 639-1; "" when it has
	// no words of one). De and En are Text translated; the one in Lang is
	// empty (Text is it). TrAt is when it was translated (0: not yet),
	// TrErr the last failure, tried again after ai.retry_failed.
	Lang     string `json:"lang,omitempty"`
	De       string `json:"de,omitempty"`
	En       string `json:"en,omitempty"`
	TrModel  string `json:"trModel,omitempty"`
	TrTokens int    `json:"trTokens,omitempty"`
	TrAt     int64  `json:"trAt,omitempty"`
	TrErr    string `json:"trErr,omitempty"`
}

func (o *OCRInfo) ok() bool { return o != nil && o.Err == "" }

// searchText is the text with its translations, for the search.
func (o *OCRInfo) searchText() string { return joinText(o.Text, o.De, o.En) }

const ocrImageSize = 2048

// --- storage ------------------------------------------------------------------

type ocrStore struct {
	mu    sync.Mutex
	items map[string]*OCRInfo
	dirty atomic.Bool
	ver   atomic.Int64 // changes with every new text (search index)
}

func (l *Library) loadOCR() {
	l.ocr.items = map[string]*OCRInfo{}
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "ocr.json"))
	if err != nil {
		return
	}
	var f struct {
		Items map[string]*OCRInfo `json:"items"`
	}
	if json.Unmarshal(b, &f) == nil && f.Items != nil {
		l.ocr.items = f.Items
	}
}

func (l *Library) saveOCR() error {
	if !l.ocr.dirty.Swap(false) {
		return nil
	}
	l.ocr.mu.Lock()
	b, err := json.Marshal(map[string]any{"version": 1, "items": l.ocr.items})
	l.ocr.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "ocr.json"), b)
}

func (l *Library) markOCRDirty() {
	if l.ocr.dirty.Swap(true) {
		return
	}
	time.AfterFunc(30*time.Second, func() {
		if err := l.saveOCR(); err != nil {
			l.logf("ocr.json: %v", err)
		}
	})
}

// OCRText returns the text recognized in a picture ("" without one).
func (l *Library) OCRText(it *Item) string {
	o := l.ocrOf(it)
	if o == nil {
		return ""
	}
	return o.Text
}

// OCROf returns the text recognized in a picture with its translations
// (nil without one).
func (l *Library) OCROf(it *Item) *OCRInfo { return l.ocrOf(it) }

// ocrSearchText is the recognized text with its translations ("" without one).
func (l *Library) ocrSearchText(it *Item) string {
	o := l.ocrOf(it)
	if o == nil {
		return ""
	}
	return o.searchText()
}

func (l *Library) ocrOf(it *Item) *OCRInfo {
	l.ocr.mu.Lock()
	defer l.ocr.mu.Unlock()
	o := l.ocr.items[l.favKey(it)]
	if !o.ok() {
		return nil
	}
	cp := *o
	return &cp
}

func (l *Library) setOCR(it *Item, o *OCRInfo) {
	l.ocr.mu.Lock()
	l.ocr.items[l.favKey(it)] = o
	l.ocr.mu.Unlock()
	l.ocr.ver.Add(1)
	l.markOCRDirty()
}

// renameOCR moves texts to new keys ("" drops them).
func (l *Library) renameOCR(pairs map[string]string) {
	l.ocr.mu.Lock()
	changed := false
	for from, to := range pairs {
		if o, ok := l.ocr.items[from]; ok {
			delete(l.ocr.items, from)
			if to != "" {
				l.ocr.items[to] = o
			}
			changed = true
		}
	}
	l.ocr.mu.Unlock()
	if changed {
		l.ocr.ver.Add(1)
		l.markOCRDirty()
	}
}

// Rerecognize forgets the texts of ids, so they are recognized again. Files
// outside the job's scope (a photo moved out of the documents folders) keep
// theirs: the job would not read them again.
func (l *Library) Rerecognize(ids []string) int {
	n := 0
	l.ocr.mu.Lock()
	for _, id := range ids {
		if it := l.Get(id); it != nil && l.OCRApplies(it) {
			delete(l.ocr.items, l.favKey(it))
			n++
		}
	}
	l.ocr.mu.Unlock()
	l.ocr.ver.Add(1)
	l.markOCRDirty()
	l.ocrJob.kick()
	l.kickXMP() // the old result leaves the files too
	return n
}

// --- background job -----------------------------------------------------------

// OCRApplies reports whether text recognition is on and reads this file.
func (l *Library) OCRApplies(it *Item) bool { return l.ocrJob.client != nil && l.ocrEligible(it) }

// ocrEligible: photos in documents folders, or every photo.
func (l *Library) ocrEligible(it *Item) bool {
	if it.Kind != Photo {
		return false
	}
	return l.opt.OCR.Scope == "all" || l.IsDocument(it)
}

func (l *Library) ocrTask() jobTask {
	return jobTask{
		workers:  l.opt.OCR.Workers,
		eligible: l.ocrEligible,
		state: func(it *Item, now time.Time) (handled, failed bool) {
			l.ocr.mu.Lock()
			o := l.ocr.items[l.favKey(it)]
			l.ocr.mu.Unlock()
			switch {
			case o.ok() && l.untranslated(o, now):
				return false, false
			case o.ok(): // a failed translation counts as a failure until it is tried again
				return true, l.ocrTr != nil && o.Text != "" && o.TrErr != ""
			case o != nil && now.Sub(time.Unix(o.At, 0)) < l.retryFailed():
				return true, true
			}
			return false, false
		},
		one: l.recognizeOne,
		save: func() {
			if err := l.saveOCR(); err != nil {
				l.logf("ocr.json: %v", err)
			}
		},
		// texts read before only wait for their translation: no picture
		// to read, so they go first and a file the share hangs on is no
		// reason to leave them out
		first: func() func(*Item) bool {
			return func(it *Item) bool { return l.ocrOf(it) != nil }
		},
		avoid: func(it *Item) bool { return l.ocrOf(it) == nil && l.unreadable(it) != nil },
	}
}

// untranslated tells whether a recognized text waits for its translation
// (or for another try after a failure).
func (l *Library) untranslated(o *OCRInfo, now time.Time) bool {
	return l.ocrTr != nil && o.ok() && o.Text != "" &&
		(o.TrAt == 0 || o.TrErr != "" && now.Sub(time.Unix(o.TrAt, 0)) >= l.retryFailed())
}

func (l *Library) ocrLoop(ctx context.Context) { l.jobLoop(ctx, &l.ocrJob, l.ocrTask()) }

// recognizeAll recognizes the text of every eligible picture without one.
func (l *Library) recognizeAll(ctx context.Context) error {
	return l.jobPass(ctx, &l.ocrJob, l.ocrTask())
}

func (l *Library) recognizeOne(ctx context.Context, it *Item) error {
	j := &l.ocrJob
	if o := l.ocrOf(it); o != nil { // read before: only the translation waits
		tokens, failed, err := l.translateOCR(ctx, it, o)
		if err != nil {
			return err
		}
		j.handled(failed, tokens)
		return nil
	}
	img, err := l.jobImage(ctx, it, cmpInt(l.opt.OCR.ImageSize, ocrImageSize), true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isUnreadable(err) { // not a failure of its own: tried once the share reads it
			j.handled(true, 0)
			return nil
		}
		l.logf("ocr %s: %v", it.Rel, err)
		l.setOCR(it, &OCRInfo{Err: err.Error(), At: time.Now().Unix()})
		j.handled(true, 0)
		return nil
	}
	o, err := j.client.recognize(ctx, img)
	if err != nil {
		if stop := jobStop(ctx, err); stop != nil {
			return stop
		}
		l.logf("ocr %s: %v", it.Rel, err)
		l.setOCR(it, &OCRInfo{Err: err.Error(), At: time.Now().Unix(), Model: j.client.opt.Model})
		j.handled(true, 0)
		return nil
	}
	l.setOCR(it, o)
	l.search.updateText(it.ID, it.Desc, o.Text, l.SpeechText(it))
	l.kickXMP()
	tokens, failed, err := l.translateOCR(ctx, it, o)
	if err != nil {
		j.tokens.Add(int64(o.Tokens)) // the text is kept; its translation waits for the next pass
		return err
	}
	j.handled(failed, o.Tokens+tokens)
	return nil
}

// translateOCR translates the text read from a picture (o) when it waits
// for that. It returns the tokens used, whether the translation failed, and
// an error only when the pass has to stop (service problems); a failure for
// this text is recorded and tried again after ai.retry_failed.
func (l *Library) translateOCR(ctx context.Context, it *Item, o *OCRInfo) (tokens int, failed bool, err error) {
	if !l.untranslated(o, time.Now()) {
		return 0, false, nil
	}
	tr, err := l.ocrTr.translate(ctx, o.Text)
	if err != nil {
		if stop := jobStop(ctx, err); stop != nil {
			return tr.tokens, false, stop
		}
		l.logf("translate %s: %v", it.Rel, err)
		tr = ocrTranslation{model: l.ocrTr.opt.Model, err: err.Error(), tokens: tr.tokens}
	}
	if l.setTranslation(it, o, tr) && tr.err == "" {
		l.search.updateText(it.ID, it.Desc, l.ocrSearchText(it), l.SpeechText(it))
	}
	return tr.tokens, tr.err != "", nil
}

// setTranslation stores the translation of base, unless the picture was
// read again or its text forgotten meanwhile.
func (l *Library) setTranslation(it *Item, base *OCRInfo, tr ocrTranslation) bool {
	key := l.favKey(it)
	l.ocr.mu.Lock()
	cur := l.ocr.items[key]
	ok := cur.ok() && cur.At == base.At && cur.Text == base.Text
	if ok {
		n := *cur
		n.Lang, n.De, n.En = tr.lang, tr.de, tr.en
		n.TrModel, n.TrTokens, n.TrAt, n.TrErr = tr.model, tr.tokens, time.Now().Unix(), tr.err
		l.ocr.items[key] = &n
	}
	l.ocr.mu.Unlock()
	if ok {
		l.ocr.ver.Add(1)
		l.markOCRDirty()
	}
	return ok
}

func cmpInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// --- client -------------------------------------------------------------------

// glmOCR: models trained for the "Text Recognition:" task prompt.
var glmOCR = regexp.MustCompile(`(?i)glm.?ocr`)

const ocrPrompt = "Gib den gesamten Text im Bild genau so wieder, wie er dort steht, Zeile für Zeile, " +
	"ohne Übersetzung, Erklärung oder Formatierung. Ist kein Text zu sehen, antworte mit einer leeren Nachricht."

func (c *aiClient) recognize(ctx context.Context, img []byte) (*OCRInfo, error) {
	prompt := ocrPrompt
	if glmOCR.MatchString(c.opt.Model) {
		prompt = "Text Recognition:"
	}
	req := map[string]any{
		"model":       c.opt.Model,
		"temperature": 0,
		"max_tokens":  4096,
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img)}},
				map[string]any{"type": "text", "text": prompt},
			}},
		},
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := c.do(ctx, http.MethodPost, "/chat/completions", req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, &aiError{msg: "leere Antwort"}
	}
	return &OCRInfo{Text: cleanOCR(resp.Choices[0].Message.Content), Model: c.opt.Model,
		Tokens: resp.Usage.Total, At: time.Now().Unix()}, nil
}

// ocrTranslation is what the model said about a recognized text: its
// language and translations (empty for the language of the text itself), or
// why that failed.
type ocrTranslation struct {
	lang, de, en string
	model, err   string
	tokens       int
}

const trSystem = "You translate text that was read from pictures in a private family photo library."

var trNames = map[string]string{"de": "German", "en": "English"}

// trPrompt asks for the language of a text and its translation into langs.
func trPrompt(text string, langs []string) string {
	var names, form []string
	for _, l := range langs {
		names = append(names, trNames[l])
		form = append(form, "<<<"+l+">>>\nthe "+trNames[l]+" translation")
	}
	return "The text below was read from a picture by text recognition: a letter, note, receipt, sign or the like, " +
		"maybe with recognition errors and the line breaks of its layout.\n" +
		"1. Name the language it is written in as a two-letter ISO 639-1 code, or none when it has no words of " +
		"any language (only numbers, codes or names).\n" +
		"2. Translate it into " + strings.Join(names, " and ") + ". Leave out the language it is written in, " +
		"and every translation when the language is none. Keep the line breaks, names, numbers, amounts and dates; " +
		"translate what is there, also when it is incomplete, and add no notes or explanations.\n" +
		"Answer in exactly this form, nothing else:\n<<<lang>>>\nxx\n" + strings.Join(form, "\n") +
		"\n\n<<<text>>>\n" + text
}

// trLangs are the languages texts are translated into.
func (c *aiClient) trLangs() []string {
	var out []string
	for _, l := range c.opt.Languages {
		if trNames[l] != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// translate asks the model for the language of text and its translations.
func (c *aiClient) translate(ctx context.Context, text string) (ocrTranslation, error) {
	langs := c.trLangs()
	req := map[string]any{
		"model":       c.opt.Model,
		"temperature": 0,
		// two translations of a long page: about as many tokens as the
		// text has bytes
		"max_tokens": min(1024+len(text), 16384),
		"messages": []any{
			map[string]any{"role": "system", "content": trSystem},
			map[string]any{"role": "user", "content": trPrompt(text, langs)},
		},
	}
	if !c.noKwargs.Load() {
		req["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := c.chat(ctx, req, &resp); err != nil {
		return ocrTranslation{}, err
	}
	if len(resp.Choices) == 0 {
		return ocrTranslation{}, &aiError{msg: "leere Antwort"}
	}
	if resp.Choices[0].FinishReason == "length" {
		return ocrTranslation{tokens: resp.Usage.Total}, &aiError{msg: "Übersetzung abgeschnitten"}
	}
	tr, err := parseTranslation(resp.Choices[0].Message.Content, text, langs)
	if err != nil {
		debugAI("translation unusable (%v): %.400s", err, resp.Choices[0].Message.Content)
		return ocrTranslation{tokens: resp.Usage.Total}, err
	}
	tr.model, tr.tokens = c.opt.Model, resp.Usage.Total
	return tr, nil
}

var (
	trMarkRe = regexp.MustCompile(`(?m)^[ \t]*<<<[ \t]*([A-Za-z]+)[ \t]*>>>[ \t]*$`)
	langRe   = regexp.MustCompile(`^[a-z]{2,3}$`)
	// lines of a code fence around the answer or a part of it
	trFenceRe = regexp.MustCompile("(?m)^[ \t]*```[A-Za-z]*[ \t]*$")
)

// parseTranslation reads the sections of an answer to trPrompt. A
// translation that says the same as the text is left out.
func parseTranslation(s, text string, langs []string) (ocrTranslation, error) {
	s = reThink.ReplaceAllString(s, "")
	if i := strings.Index(s, "</think>"); i >= 0 { // opening tag cut off by the template
		s = s[i+len("</think>"):]
	}
	s = trFenceRe.ReplaceAllString(s, "")
	sec := map[string]string{}
	ms := trMarkRe.FindAllStringSubmatchIndex(s, -1)
	for i, m := range ms {
		end := len(s)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		name := strings.ToLower(s[m[2]:m[3]])
		if _, seen := sec[name]; !seen {
			sec[name] = cleanOCR(s[m[1]:end])
		}
	}
	raw, ok := sec["lang"]
	if !ok {
		return ocrTranslation{}, &aiError{msg: "Antwort ohne Sprache"}
	}
	var lang string
	if f := strings.Fields(raw); len(f) > 0 {
		lang = langCode(strings.Trim(f[0], "\"'.`*"))
	}
	switch lang {
	case "deu", "ger":
		lang = "de"
	case "eng":
		lang = "en"
	}
	if f := strings.Fields(strings.ToLower(raw)); len(f) > 0 && strings.Trim(f[0], "\"'.`*") == "none" {
		lang = "" // no words to translate
	} else if !langRe.MatchString(lang) {
		return ocrTranslation{}, &aiError{msg: "Antwort ohne gültige Sprache: " + raw}
	}
	var tr ocrTranslation
	tr.lang = lang
	same := func(a, b string) bool {
		return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
	}
	for _, l := range langs {
		if lang == "" || l == lang {
			continue
		}
		t := sec[l]
		if t == "" {
			return ocrTranslation{}, &aiError{msg: "Antwort ohne Übersetzung (" + l + ")"}
		}
		if same(t, text) {
			continue
		}
		switch l {
		case "de":
			tr.de = t
		case "en":
			tr.en = t
		}
	}
	return tr, nil
}

var fenceRe = regexp.MustCompile("(?s)^```[a-zA-Z]*\\n?(.*?)\\n?```$")

// cleanOCR trims the answer: code fences, blank lines at the ends, trailing
// spaces, more than one empty line in a row.
func cleanOCR(s string) string {
	s = strings.TrimSpace(s)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// OCRTest is the result of the connection test on the settings page.
type OCRTest struct {
	Text  string `json:"text,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
	Error string `json:"error,omitempty"`
	Fatal bool   `json:"fatal,omitempty"`
}

// TestOCR lets the model read a small drawn picture with text (no photo).
func TestOCR(ctx context.Context, o OCROptions) OCRTest {
	c := newAIClient(AIOptions{URL: o.URL, Key: o.Key, Model: o.Model})
	t0 := time.Now()
	r, err := c.recognize(ctx, ocrTestPicture())
	if err != nil {
		var ae *aiError
		return OCRTest{Error: err.Error(), Fatal: errors.As(err, &ae) && ae.fatal}
	}
	return OCRTest{Text: r.Text, Ms: time.Since(t0).Milliseconds()}
}

// ocrTestPicture: a note with two lines of text, drawn large enough to read.
func ocrTestPicture() []byte {
	const scale = 4
	small := image.NewRGBA(image.Rect(0, 0, 150, 50))
	draw.Draw(small, small.Bounds(), &image.Uniform{color.RGBA{250, 248, 240, 255}}, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: small, Src: image.NewUniform(color.RGBA{20, 20, 30, 255}), Face: basicfont.Face7x13}
	for i, line := range []string{"Schuhkarton", "Einkauf 2026"} {
		d.Dot = fixed.P(10, 20+i*18)
		d.DrawString(line)
	}
	big := image.NewRGBA(image.Rect(0, 0, 150*scale, 50*scale))
	for y := range 50 * scale {
		for x := range 150 * scale {
			big.Set(x, y, small.At(x/scale, y/scale))
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, big, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}
