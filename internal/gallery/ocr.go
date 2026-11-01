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
}

// OCRInfo is the text recognized in one picture. It lives in ocr.json,
// keyed like favorites, so it follows moves and edits.
type OCRInfo struct {
	Text   string `json:"text,omitempty"` // empty: no text in the picture
	Model  string `json:"model,omitempty"`
	Tokens int    `json:"tokens,omitempty"`
	At     int64  `json:"at"`
	// Err is the last failure for this file; it is tried again after a week.
	Err string `json:"err,omitempty"`
}

func (o *OCRInfo) ok() bool { return o != nil && o.Err == "" }

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
			case o.ok():
				return true, false
			case o != nil && now.Sub(time.Unix(o.At, 0)) < aiRetryFailed:
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
	}
}

func (l *Library) ocrLoop(ctx context.Context) { l.jobLoop(ctx, &l.ocrJob, l.ocrTask()) }

// recognizeAll recognizes the text of every eligible picture without one.
func (l *Library) recognizeAll(ctx context.Context) error {
	return l.jobPass(ctx, &l.ocrJob, l.ocrTask())
}

func (l *Library) recognizeOne(ctx context.Context, it *Item) error {
	j := &l.ocrJob
	img, err := l.jobImage(ctx, it, cmpInt(l.opt.OCR.ImageSize, ocrImageSize), true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
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
	l.search.updateText(it.ID, it.Desc, o.Text)
	j.handled(false, o.Tokens)
	return nil
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
