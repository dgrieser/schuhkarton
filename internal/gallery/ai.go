package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AIOptions configure descriptions by a vision language model behind an
// OpenAI-compatible API (mittwald AI hosting, vLLM, Ollama). An empty Model
// switches the feature off.
type AIOptions struct {
	URL       string // API base, e.g. https://llm.aihosting.mittwald.de/v1
	Key       string // bearer token; empty for local servers
	Model     string
	Workers   int
	Languages []string // de, en
	ImageSize int      // long edge of the image sent
	Thinking  bool     // let reasoning models think first
}

// AIInfo is what the model said about one photo. It lives in ai.json,
// keyed like favorites (root name + "/" + rel), so it survives rescans,
// edits, rotation and moves.
type AIInfo struct {
	De     string   `json:"de,omitempty"`
	En     string   `json:"en,omitempty"`
	TagsDe []string `json:"tagsDe,omitempty"`
	TagsEn []string `json:"tagsEn,omitempty"`
	People int      `json:"people,omitempty"`
	Model  string   `json:"model,omitempty"`
	Tokens int      `json:"tokens,omitempty"`
	At     int64    `json:"at"`
	// From tells what the model saw of a video (video.go): "video" (a small
	// clip), "frames" (pictures from it), "frame" (one picture, the clip
	// failed); "" for photos and videos described before.
	From string `json:"from,omitempty"`
	// Tried is when the clip failed (From "frame"); tried again after a week.
	Tried int64 `json:"tried,omitempty"`
	// Err is the last failure for this file; it is tried again after ai.retry_failed.
	Err string `json:"err,omitempty"`
}

// ok reports a usable description.
func (a *AIInfo) ok() bool {
	return a != nil && a.Err == "" && (a.De != "" || a.En != "" || len(a.TagsDe)+len(a.TagsEn) > 0)
}

// DefaultRetryFailed is how long a failed description, text or speech
// recognition waits before it is tried again (ai.retry_failed);
// aiRetryFrame how long a video described from one frame waits before it is
// described from the video.
const (
	DefaultRetryFailed = 72 * time.Hour
	aiRetryFrame       = 7 * 24 * time.Hour
)

// retryFailed is Options.RetryFailed, or DefaultRetryFailed when not set.
func (l *Library) retryFailed() time.Duration {
	if l.opt.RetryFailed > 0 {
		return l.opt.RetryFailed
	}
	return DefaultRetryFailed
}

// --- storage ------------------------------------------------------------------

type aiStore struct {
	mu    sync.Mutex
	items map[string]*AIInfo
	dirty atomic.Bool
	ver   atomic.Int64 // changes with every new description (search index)
}

func (l *Library) loadAI() {
	l.ai.items = map[string]*AIInfo{}
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "ai.json"))
	if err != nil {
		return
	}
	var f struct {
		Items map[string]*AIInfo `json:"items"`
	}
	if json.Unmarshal(b, &f) == nil && f.Items != nil {
		l.ai.items = f.Items
	}
}

func (l *Library) saveAI() error {
	if !l.ai.dirty.Swap(false) {
		return nil
	}
	l.ai.mu.Lock()
	b, err := json.Marshal(map[string]any{"version": 1, "items": l.ai.items})
	l.ai.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "ai.json"), b)
}

func (l *Library) markAIDirty() {
	if l.ai.dirty.Swap(true) {
		return
	}
	time.AfterFunc(30*time.Second, func() {
		if err := l.saveAI(); err != nil {
			l.logf("ai.json: %v", err)
		}
	})
}

// AIOf returns the description of an item (nil without one).
func (l *Library) AIOf(it *Item) *AIInfo {
	l.ai.mu.Lock()
	defer l.ai.mu.Unlock()
	a := l.ai.items[l.favKey(it)]
	if !a.ok() {
		return nil
	}
	cp := *a
	return &cp
}

func (l *Library) setAI(it *Item, a *AIInfo) {
	l.ai.mu.Lock()
	l.ai.items[l.favKey(it)] = a
	l.ai.mu.Unlock()
	l.ai.ver.Add(1)
	l.markAIDirty()
}

// renameAI moves descriptions to new keys ("" drops them).
func (l *Library) renameAI(pairs map[string]string) {
	l.ai.mu.Lock()
	changed := false
	for from, to := range pairs {
		if a, ok := l.ai.items[from]; ok {
			delete(l.ai.items, from)
			if to != "" {
				l.ai.items[to] = a
			}
			changed = true
		}
	}
	l.ai.mu.Unlock()
	if changed {
		l.ai.ver.Add(1)
		l.markAIDirty()
	}
}

// Redescribe forgets the descriptions of ids, so they are made again.
func (l *Library) Redescribe(ids []string) int {
	n := 0
	l.ai.mu.Lock()
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			delete(l.ai.items, l.favKey(it))
			n++
		}
	}
	l.ai.mu.Unlock()
	l.ai.ver.Add(1)
	l.markAIDirty()
	l.kickAI()
	l.kickXMP() // the old result leaves the files too
	return n
}

// --- background job -----------------------------------------------------------

func (l *Library) kickAI() { l.aiJob.kick() }

// aiEligible: photos and videos of their own (not the video half of a Live Photo).
func aiEligible(it *Item) bool { return it.Kind == Photo || (it.Kind == Video && it.LivePhoto == "") }

// aiTask is the description job: every eligible file without a description.
func (l *Library) aiTask() jobTask {
	return jobTask{
		workers:  l.opt.AI.Workers,
		eligible: aiEligible,
		state: func(it *Item, now time.Time) (handled, failed bool) {
			l.ai.mu.Lock()
			a := l.ai.items[l.favKey(it)]
			l.ai.mu.Unlock()
			switch {
			case a.ok() && it.Kind == Video && l.media.ffmpeg != "" &&
				(a.From == "" || a.From == "frame" && now.Sub(time.Unix(a.Tried, 0)) >= aiRetryFrame):
				return false, false // described from one frame: now from the video
			case a.ok():
				return true, false
			case a != nil && a.Err != "" && now.Sub(time.Unix(a.At, 0)) < l.retryFailed():
				return true, true // failed recently: tried again later (ai.retry_failed)
			}
			return false, false
		},
		one: l.describeOne,
		save: func() {
			if err := l.saveAI(); err != nil {
				l.logf("ai.json: %v", err)
			}
		},
		// photos with a large preview here first: their picture for the
		// model comes from it in a moment, not from the original on a
		// network share (only this cache folder is looked at: cheap)
		first: l.havePreview,
	}
}

// aiLoop describes photos in the background until ctx ends.
func (l *Library) aiLoop(ctx context.Context) { l.jobLoop(ctx, &l.aiJob, l.aiTask()) }

// describeAll describes every eligible item without a description, newest
// first. It returns early on errors that concern the service, not a photo.
func (l *Library) describeAll(ctx context.Context) error { return l.jobPass(ctx, &l.aiJob, l.aiTask()) }

// describeOne describes one item. It returns an error only when the whole
// job has to stop (service problems); problems with this file are recorded.
func (l *Library) describeOne(ctx context.Context, it *Item) error {
	j := &l.aiJob
	if it.Kind == Video && l.media.ffmpeg != "" {
		if l.speechPending(it) {
			return nil // waits for the spoken words; the speech job kicks again
		}
		if done, err := l.describeVideo(ctx, it); done || err != nil {
			return err
		}
	}
	img, err := l.jobImage(ctx, it, l.opt.AI.ImageSize, false)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.logf("describe %s: %v", it.Rel, err)
		l.setAI(it, &AIInfo{Err: err.Error(), At: time.Now().Unix()})
		j.handled(true, 0)
		return nil
	}
	info, err := j.client.describe(ctx, img)
	if err != nil {
		if stop := jobStop(ctx, err); stop != nil {
			return stop
		}
		l.logf("describe %s: %v", it.Rel, err)
		l.setAI(it, &AIInfo{Err: err.Error(), At: time.Now().Unix(), Model: j.client.opt.Model})
		j.handled(true, 0)
		return nil
	}
	if it.Kind == Video && l.media.ffmpeg != "" {
		info.From, info.Tried = "frame", time.Now().Unix() // the clip could not be made
	}
	l.setAI(it, info)
	l.search.updateAI(it.ID, info)
	j.handled(false, info.Tokens)
	l.kickXMP()
	return nil
}

// jobImage returns a JPEG of what the viewer shows, at most n pixels on the
// long edge (0: 1024). original reads photos from the file even when a large
// preview exists (text recognition wants every pixel up to n).
func (l *Library) jobImage(ctx context.Context, it *Item, n int, original bool) ([]byte, error) {
	return l.renderJPEG(ctx, it, cmpInt(n, 1024), original, 82, true)
}

// --- client -------------------------------------------------------------------

type aiClient struct {
	opt      AIOptions
	http     *http.Client
	noKwargs atomic.Bool // the server refuses chat_template_kwargs
	noVideo  atomic.Bool // the service takes no videos: frames instead (video.go)
}

func newAIClient(o AIOptions) *aiClient {
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if len(o.Languages) == 0 {
		o.Languages = []string{"de", "en"}
	}
	o.URL = strings.TrimRight(o.URL, "/")
	return &aiClient{opt: o, http: &http.Client{Timeout: 3 * time.Minute}}
}

// aiError is a failed API call. fatal: settings are wrong (key, model,
// address); transient: try again later (rate limit, server busy, network).
type aiError struct {
	status           int
	msg              string
	fatal, transient bool
	badAnswer        bool // the answer was no usable JSON: asking again may help
}

func (e *aiError) Error() string {
	if e.status > 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.msg, e.status)
	}
	return e.msg
}

func (c *aiClient) do(ctx context.Context, method, path string, body any, out any) error {
	if body == nil {
		return c.send(ctx, method, path, "", nil, out)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.send(ctx, method, path, "application/json", b, out)
}

// send makes one API call with a ready body and decodes the JSON answer.
// It keeps to the account's requests per minute (ratelimit.go): it waits
// while none are left, and after a 429 it sends the same request again in
// the next minute; only repeated 429 come back (the job pauses then).
func (c *aiClient) send(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	b := budgetFor(c.opt.URL, c.opt.Key)
	for try := 0; ; try++ {
		tk, err := b.wait(ctx)
		if err != nil {
			return err
		}
		h, err := c.sendOnce(ctx, method, path, contentType, body, out)
		limited := rateLimited(err)
		b.seen(h, limited, tk)
		if !limited || try+1 >= rateRetries {
			return err
		}
	}
}

func rateLimited(err error) bool {
	var ae *aiError
	return errors.As(err, &ae) && ae.status == 429
}

func (c *aiClient) sendOnce(ctx context.Context, method, path, contentType string, body []byte, out any) (http.Header, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.opt.URL+path, rd)
	if err != nil {
		return nil, &aiError{msg: "Adresse ungültig: " + err.Error(), fatal: true}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.opt.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.opt.Key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &aiError{msg: "Dienst nicht erreichbar: " + err.Error(), transient: true}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return resp.Header, classify(resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return resp.Header, &aiError{msg: "unerwartete Antwort: " + err.Error()}
	}
	return resp.Header, nil
}

func classify(status int, body []byte) *aiError {
	msg := strings.TrimSpace(string(body))
	var e struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				msg = m
			}
		}
		if e.Message != "" {
			msg = e.Message
		}
		if e.Detail != "" {
			msg = e.Detail
		}
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	low := strings.ToLower(msg)
	switch {
	case status == 401 || status == 403:
		return &aiError{status: status, msg: "API-Schlüssel abgelehnt", fatal: true}
	case status == 404:
		return &aiError{status: status, msg: "Modell oder Adresse nicht gefunden: " + msg, fatal: true}
	case status == 429 || status >= 500:
		return &aiError{status: status, msg: "Dienst ausgelastet: " + msg, transient: true}
	case strings.Contains(low, "model") && (strings.Contains(low, "not exist") || strings.Contains(low, "not found") || strings.Contains(low, "unknown")):
		return &aiError{status: status, msg: "Modell nicht gefunden: " + msg, fatal: true}
	case strings.Contains(low, "multimodal") || strings.Contains(low, "image input") || strings.Contains(low, "vision") || strings.Contains(low, "image_url"):
		return &aiError{status: status, msg: "Das Modell versteht keine Bilder: " + msg, fatal: true}
	}
	return &aiError{status: status, msg: msg}
}

// Models lists the models the service offers.
func (c *aiClient) Models(ctx context.Context) ([]string, error) {
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/models", nil, &r); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range r.Data {
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out, nil
}

const aiSystem = "You write search metadata for photos and videos in a private family photo library. Reply with one JSON object only, without markdown."

// prompt asks for the description of a photo; subject "video" asks for a
// video, extra adds rules about what the model gets.
func (c *aiClient) prompt(subject string, extra ...string) string {
	de, en := false, false
	for _, l := range c.opt.Languages {
		de = de || l == "de"
		en = en || l == "en"
	}
	var keys, rules []string
	if de {
		keys = append(keys, `"de": "…"`)
		rules = append(rules, "- de: what the "+subject+" shows, 1–2 sentences in German.")
	}
	if en {
		keys = append(keys, `"en": "…"`)
		rules = append(rules, "- en: what the "+subject+" shows, 1–2 sentences in English.")
	}
	if de {
		keys = append(keys, `"tags_de": ["…"]`)
	}
	if en {
		keys = append(keys, `"tags_en": ["…"]`)
	}
	keys = append(keys, `"people": 0`)
	var tagKeys []string
	if de {
		tagKeys = append(tagKeys, "tags_de (German)")
	}
	if en {
		tagKeys = append(tagKeys, "tags_en (English)")
	}
	langs := strings.Join(tagKeys, " and ")
	people := "- people: how many people are visible (0 if none).\n"
	names := "- Never name or identify people"
	if subject == "video" {
		people = "- people: the most people visible at the same time (0 if none).\n"
		names = "- Never identify people by their looks (names only from spoken words)"
	}
	return "Describe this " + subject + " for a search index.\n" + strings.Join(extra, "") +
		"Return JSON: {" + strings.Join(keys, ", ") + "}\n" +
		strings.Join(rules, "\n") + "\n" +
		"- " + langs + ": 8–20 short lowercase search words each, mostly nouns: visible objects, the kind of place or scene " +
		"(beach, kitchen, playground, forest), activity or occasion (birthday, hiking, swimming), season, weather, time of day, " +
		"notable colours, animals, plants, vehicles, food, and the kind of picture (screenshot, document, receipt, drawing, map, " +
		"text) when it is not an ordinary photo.\n" +
		people +
		names + " and never guess the age of adults or anybody's ethnicity; say child, baby, woman, man, group.\n" +
		"- If there is readable text, say what kind of text it is instead of copying it."
}

// describe sends one JPEG and parses the answer.
func (c *aiClient) describe(ctx context.Context, img []byte) (*AIInfo, error) {
	return c.ask(ctx, c.prompt("photo"), imagePart(img))
}

func imagePart(jpg []byte) any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpg)}}
}

// ask sends the prompt with pictures or a video and parses the answer.
func (c *aiClient) ask(ctx context.Context, prompt string, media ...any) (*AIInfo, error) {
	req := map[string]any{
		"model":       c.opt.Model,
		"temperature": 0.2,
		"max_tokens":  800,
		"messages": []any{
			map[string]any{"role": "system", "content": aiSystem},
			map[string]any{"role": "user", "content": append([]any{map[string]any{"type": "text", "text": prompt}}, media...)},
		},
	}
	if c.opt.Thinking {
		req["max_tokens"] = 6000
	} else if !c.noKwargs.Load() {
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
	err := c.do(ctx, http.MethodPost, "/chat/completions", req, &resp)
	var ae *aiError
	if errors.As(err, &ae) && ae.status == 400 && strings.Contains(ae.msg, "chat_template_kwargs") && !c.noKwargs.Load() {
		c.noKwargs.Store(true)
		delete(req, "chat_template_kwargs")
		err = c.do(ctx, http.MethodPost, "/chat/completions", req, &resp)
	}
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, &aiError{msg: "leere Antwort"}
	}
	content := resp.Choices[0].Message.Content
	tokens := resp.Usage.Total
	info, err := parseAIAnswer(content)
	for try := 0; try < aiAnswerRetries && errors.As(err, &ae) && ae.badAnswer; try++ {
		// ask again with the broken answer and what is wrong with it
		debugAI("answer is no JSON (%v), asking again: %.400s", err, content)
		msgs := append(req["messages"].([]any)[:2:2],
			map[string]any{"role": "assistant", "content": content},
			map[string]any{"role": "user", "content": "Your answer is not valid JSON (" + ae.msg +
				"). Send the same answer again as one valid JSON object only: no text around it, " +
				"double quotes inside values escaped as \\\"."})
		retry := maps.Clone(req)
		retry["messages"] = msgs
		resp.Choices, resp.Usage.Total = nil, 0
		if err := c.do(ctx, http.MethodPost, "/chat/completions", retry, &resp); err != nil {
			return nil, err
		}
		if len(resp.Choices) == 0 {
			return nil, &aiError{msg: "leere Antwort"}
		}
		content = resp.Choices[0].Message.Content
		tokens += resp.Usage.Total
		info, err = parseAIAnswer(content)
	}
	if err != nil {
		return nil, err
	}
	if len(info.TagsDe)+len(info.TagsEn) == 0 {
		debugAI("answer without tags (finish %s): %.400s", resp.Choices[0].FinishReason, content)
	}
	info.Model, info.Tokens, info.At = c.opt.Model, tokens, time.Now().Unix()
	return info, nil
}

// aiAnswerRetries is how often a model is asked again for an answer that
// is no usable JSON.
const aiAnswerRetries = 2

// debugAI logs odd answers when SCHUHKARTON_AI_DEBUG is set.
func debugAI(format string, args ...any) {
	if os.Getenv("SCHUHKARTON_AI_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "ai: "+format+"\n", args...)
	}
}

var (
	reThink = regexp.MustCompile(`(?s)<think>.*?</think>`)
	reFence = regexp.MustCompile("(?s)```(?:json)?")
)

// parseAIAnswer reads the JSON object of an answer, also when the model
// wrapped it in markdown or thoughts, or used other key names and types.
func parseAIAnswer(s string) (*AIInfo, error) {
	s = reThink.ReplaceAllString(s, "")
	if i := strings.Index(s, "</think>"); i >= 0 { // opening tag cut off by the template
		s = s[i+len("</think>"):]
	}
	s = reFence.ReplaceAllString(s, "")
	if !strings.Contains(s, "{") {
		return nil, &aiError{msg: "Antwort ohne JSON", badAnswer: true}
	}
	var m map[string]any
	if err := lenientUnmarshal(s, &m); err != nil {
		return nil, &aiError{msg: "Antwort ist kein gültiges JSON: " + err.Error(), badAnswer: true}
	}
	get := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				return v
			}
		}
		return nil
	}
	text := func(v any) string {
		switch x := v.(type) {
		case string:
			return strings.TrimSpace(x)
		case []any:
			var parts []string
			for _, p := range x {
				if s, ok := p.(string); ok {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			return strings.Join(parts, " ")
		}
		return ""
	}
	info := &AIInfo{
		De:     text(get("de", "description_de", "beschreibung", "german")),
		En:     text(get("en", "description_en", "description", "english")),
		TagsDe: cleanTags(get("tags_de", "tagsDe", "schlagworte", "stichwörter")),
		TagsEn: cleanTags(get("tags_en", "tagsEn", "tags", "keywords")),
	}
	// other shapes: "tags": {"de": […], "en": […]}, or keys like "keywords_de"
	for k, v := range m {
		lk := strings.ToLower(k)
		if !strings.Contains(lk, "tag") && !strings.Contains(lk, "keyword") && !strings.Contains(lk, "schlag") && !strings.Contains(lk, "stich") {
			continue
		}
		if sub, ok := v.(map[string]any); ok {
			if len(info.TagsDe) == 0 {
				info.TagsDe = cleanTags(sub["de"])
			}
			if len(info.TagsEn) == 0 {
				info.TagsEn = cleanTags(sub["en"])
			}
			continue
		}
		switch {
		case len(info.TagsDe) == 0 && (strings.HasSuffix(lk, "de") || strings.Contains(lk, "german") || strings.Contains(lk, "deutsch")):
			info.TagsDe = cleanTags(v)
		case len(info.TagsEn) == 0 && (strings.HasSuffix(lk, "en") || strings.Contains(lk, "english")):
			info.TagsEn = cleanTags(v)
		}
	}
	switch p := get("people", "persons", "personen").(type) {
	case float64:
		info.People = int(p)
	case string:
		info.People, _ = strconv.Atoi(strings.TrimSpace(p))
	}
	if info.People < 0 || info.People > 1000 {
		info.People = 0
	}
	if !info.ok() {
		return nil, &aiError{msg: "Antwort ohne Beschreibung"}
	}
	return info, nil
}

func cleanTags(v any) []string {
	var raw []string
	switch x := v.(type) {
	case string:
		raw = strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	case []any:
		for _, p := range x {
			if s, ok := p.(string); ok {
				raw = append(raw, s)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range raw {
		t = strings.ToLower(strings.Trim(strings.TrimSpace(t), `"'#.`))
		if t == "" || len(t) > 40 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == 25 {
			break
		}
	}
	return out
}

// --- setup test ---------------------------------------------------------------

// AITest is the result of trying the AI settings.
type AITest struct {
	Models []string `json:"models,omitempty"`
	Answer *AIInfo  `json:"answer,omitempty"`
	Ms     int64    `json:"ms,omitempty"`
	Error  string   `json:"error,omitempty"`
	Fatal  bool     `json:"fatal,omitempty"`
}

// TestAI lists the models of a service and, with describe, describes a
// small drawn test picture (no photo leaves the computer).
func TestAI(ctx context.Context, o AIOptions, describe bool) AITest {
	c := newAIClient(o)
	var res AITest
	models, err := c.Models(ctx)
	if err != nil {
		res.Error = err.Error()
		var ae *aiError
		res.Fatal = errors.As(err, &ae) && ae.fatal
		if !describe || res.Fatal {
			return res
		}
	}
	res.Models = models
	if !describe || o.Model == "" {
		return res
	}
	t0 := time.Now()
	info, err := c.describe(ctx, testPicture())
	res.Ms = time.Since(t0).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Error, res.Answer = "", info
	return res
}

// testPicture draws a tiny landscape: sky, sun, meadow, a house.
func testPicture() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	fill := func(r image.Rectangle, c color.RGBA) { draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src) }
	fill(img.Bounds(), color.RGBA{110, 170, 235, 255})
	fill(image.Rect(0, 160, 320, 240), color.RGBA{70, 160, 60, 255})
	for y := 20; y < 80; y++ { // sun
		for x := 230; x < 290; x++ {
			if (x-260)*(x-260)+(y-50)*(y-50) < 28*28 {
				img.Set(x, y, color.RGBA{250, 215, 40, 255})
			}
		}
	}
	fill(image.Rect(60, 110, 160, 190), color.RGBA{200, 50, 40, 255}) // house
	for y := 70; y < 110; y++ {                                       // roof
		w := (y - 70) * 60 / 40
		fill(image.Rect(110-w, y, 110+w, y+1), color.RGBA{110, 60, 30, 255})
	}
	fill(image.Rect(95, 150, 125, 190), color.RGBA{90, 50, 20, 255}) // door
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}
