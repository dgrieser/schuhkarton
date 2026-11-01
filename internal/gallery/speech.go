package gallery

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// SpeechOptions configure speech recognition for videos and Live Photos by
// a transcription model (whisper-large-v3-turbo at mittwald AI hosting, or
// any OpenAI-compatible /audio/transcriptions). An empty Model is off.
type SpeechOptions struct {
	URL, Key string
	Model    string
	// Fallback is asked when Model hears nothing in sound loud enough for
	// speech (e.g. cohere-transcribe-03-2026 after whisper); "" none.
	Fallback string
	Workers  int
	// MaxSeconds limits how much of a long video is sent (0: 30 minutes).
	MaxSeconds int
}

// SpeechInfo is what was said in a video or Live Photo. It lives in
// speech.json, keyed like favorites (a Live Photo under its photo).
type SpeechInfo struct {
	Text  string `json:"text,omitempty"` // empty: nothing understood
	Lang  string `json:"lang,omitempty"`
	Model string `json:"model,omitempty"`
	At    int64  `json:"at"`
	// Err is the last failure for this file; it is tried again after a week.
	Err string `json:"err,omitempty"`
}

func (s *SpeechInfo) ok() bool { return s != nil && s.Err == "" }

const speechMaxSeconds = 30 * 60

// Audio quieter than speechQuiet on average (dB) and never louder than
// speechPeak holds no speech; models invent phrases for it. The peak keeps a
// few loud words in a long quiet video.
const (
	speechQuiet = -45.0
	speechPeak  = -25.0
	// speechLoud: the fallback model only gets sound at least this loud on
	// average; it writes fluent sentences into quiet noise.
	speechLoud = -40.0
)

// --- storage ------------------------------------------------------------------

type speechStore struct {
	mu    sync.Mutex
	items map[string]*SpeechInfo
	dirty atomic.Bool
	ver   atomic.Int64
}

func (l *Library) loadSpeech() {
	l.speech.items = map[string]*SpeechInfo{}
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "speech.json"))
	if err != nil {
		return
	}
	var f struct {
		Items map[string]*SpeechInfo `json:"items"`
	}
	if json.Unmarshal(b, &f) == nil && f.Items != nil {
		l.speech.items = f.Items
	}
}

func (l *Library) saveSpeech() error {
	if !l.speech.dirty.Swap(false) {
		return nil
	}
	l.speech.mu.Lock()
	b, err := json.Marshal(map[string]any{"version": 1, "items": l.speech.items})
	l.speech.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "speech.json"), b)
}

func (l *Library) markSpeechDirty() {
	if l.speech.dirty.Swap(true) {
		return
	}
	time.AfterFunc(30*time.Second, func() {
		if err := l.saveSpeech(); err != nil {
			l.logf("speech.json: %v", err)
		}
	})
}

// SpeechText returns what was said in a video or Live Photo ("" without).
func (l *Library) SpeechText(it *Item) string {
	if s := l.speechOf(it); s != nil {
		return s.Text
	}
	return ""
}

func (l *Library) speechOf(it *Item) *SpeechInfo {
	l.speech.mu.Lock()
	defer l.speech.mu.Unlock()
	s := l.speech.items[l.favKey(it)]
	if !s.ok() {
		return nil
	}
	cp := *s
	return &cp
}

func (l *Library) setSpeech(it *Item, s *SpeechInfo) {
	l.speech.mu.Lock()
	l.speech.items[l.favKey(it)] = s
	l.speech.mu.Unlock()
	l.speech.ver.Add(1)
	l.markSpeechDirty()
}

// renameSpeech moves texts to new keys ("" drops them).
func (l *Library) renameSpeech(pairs map[string]string) {
	l.speech.mu.Lock()
	changed := false
	for from, to := range pairs {
		if s, ok := l.speech.items[from]; ok {
			delete(l.speech.items, from)
			if to != "" {
				l.speech.items[to] = s
			}
			changed = true
		}
	}
	l.speech.mu.Unlock()
	if changed {
		l.speech.ver.Add(1)
		l.markSpeechDirty()
	}
}

// Retranscribe forgets what was heard in ids, so it is recognized again.
func (l *Library) Retranscribe(ids []string) int {
	n := 0
	l.speech.mu.Lock()
	for _, id := range ids {
		if it := l.Get(id); it != nil && l.SpeechApplies(it) {
			delete(l.speech.items, l.favKey(it))
			n++
		}
	}
	l.speech.mu.Unlock()
	l.speech.ver.Add(1)
	l.markSpeechDirty()
	l.speechJob.kick()
	l.kickXMP() // the old result leaves the files too
	return n
}

// --- background job -----------------------------------------------------------

// SpeechApplies reports whether speech recognition is on and listens to it.
func (l *Library) SpeechApplies(it *Item) bool {
	return l.speechJob.client != nil && speechEligible(it)
}

// speechEligible: videos of their own and Live Photos (the photo stands for
// its video).
func speechEligible(it *Item) bool {
	return (it.Kind == Video && it.LivePhoto == "") || (it.Kind == Photo && it.LiveVideo != "")
}

// speechSource returns the file with the sound.
func (l *Library) speechSource(it *Item) (string, error) {
	if it.Kind == Video {
		return l.Path(it), nil
	}
	v := l.Get(it.LiveVideo)
	if v == nil {
		return "", errors.New("Live-Photo-Video fehlt")
	}
	return l.Path(v), nil
}

func (l *Library) speechTask() jobTask {
	return jobTask{
		workers:  l.opt.Speech.Workers,
		eligible: speechEligible,
		state: func(it *Item, now time.Time) (handled, failed bool) {
			l.speech.mu.Lock()
			s := l.speech.items[l.favKey(it)]
			l.speech.mu.Unlock()
			switch {
			case s.ok():
				return true, false
			case s != nil && now.Sub(time.Unix(s.At, 0)) < aiRetryFailed:
				return true, true
			}
			return false, false
		},
		one: l.transcribeOne,
		save: func() {
			if err := l.saveSpeech(); err != nil {
				l.logf("speech.json: %v", err)
			}
		},
	}
}

func (l *Library) speechLoop(ctx context.Context) { l.jobLoop(ctx, &l.speechJob, l.speechTask()) }

func (l *Library) transcribeAll(ctx context.Context) error {
	return l.jobPass(ctx, &l.speechJob, l.speechTask())
}

func (l *Library) transcribeOne(ctx context.Context, it *Item) error {
	j := &l.speechJob
	fail := func(err error) error {
		l.logf("speech %s: %v", it.Rel, err)
		l.setSpeech(it, &SpeechInfo{Err: err.Error(), At: time.Now().Unix(), Model: j.client.opt.Model})
		j.handled(true, 0)
		return nil
	}
	src, err := l.speechSource(it)
	if err != nil {
		return fail(err)
	}
	audio, lvl, err := l.extractAudio(ctx, src, cmpInt(l.opt.Speech.MaxSeconds, speechMaxSeconds))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fail(err)
	}
	if audio == nil || lvl.quiet() {
		// no sound, or too quiet for speech: done without asking the model
		l.setSpeech(it, &SpeechInfo{Model: j.client.opt.Model, At: time.Now().Unix()})
		j.handled(false, 0)
		l.kickXMP()
		return nil
	}
	s, err := j.client.transcribe(ctx, audio, "audio.mp3", j.client.opt.Model)
	if fb := l.opt.Speech.Fallback; err == nil && s.Text == "" && fb != "" && fb != j.client.opt.Model && lvl.mean >= speechLoud {
		// nothing heard in loud sound: a second model listens. Only a busy
		// service makes the file wait; other errors (a model the service
		// does not have) leave the first answer.
		s2, err2 := j.client.transcribe(ctx, audio, "audio.mp3", fb)
		var ae *aiError
		switch {
		case err2 == nil && s2.Text != "":
			s = s2
		case err2 != nil && (ctx.Err() != nil || (errors.As(err2, &ae) && ae.transient)):
			err = err2
		case err2 != nil:
			l.logf("speech %s: %s: %v", it.Rel, fb, err2)
		}
	}
	if err != nil {
		if stop := jobStop(ctx, err); stop != nil {
			return stop
		}
		return fail(err)
	}
	l.setSpeech(it, s)
	l.search.updateText(it.ID, it.Desc, l.OCRText(it), s.Text)
	j.handled(false, 0)
	l.kickXMP()
	return nil
}

var (
	meanVolumeRe = regexp.MustCompile(`mean_volume:\s*(-?[0-9.]+|-inf) dB`)
	maxVolumeRe  = regexp.MustCompile(`max_volume:\s*(-?[0-9.]+|-inf) dB`)
)

// volume reads a volumedetect value (-inf: silence; missing: loud).
func volume(re *regexp.Regexp, text string) float64 {
	m := re.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	if m[1] == "-inf" {
		return math.Inf(-1)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	return v
}

// audioLevel is the mean and peak volume of a sound in dB.
type audioLevel struct{ mean, max float64 }

// quiet: too quiet to hold speech.
func (a audioLevel) quiet() bool { return a.mean < speechQuiet && a.max < speechPeak }

// extractAudio returns the sound of a video as small mono MP3 (nil: the
// video has none) and how loud it is.
func (l *Library) extractAudio(ctx context.Context, src string, maxSec int) ([]byte, audioLevel, error) {
	var lvl audioLevel
	if l.media.ffmpeg == "" {
		return nil, lvl, errors.New("ffmpeg fehlt")
	}
	tmp, err := os.CreateTemp(l.opt.CacheDir, "speech-*.mp3")
	if err != nil {
		return nil, lvl, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.CommandContext(ctx, l.media.ffmpeg, "-v", "info", "-nostats", "-y", "-i", src, "-vn", "-sn", "-dn",
		"-t", strconv.Itoa(maxSec), "-ac", "1", "-ar", "16000", "-af", "volumedetect",
		"-c:a", "libmp3lame", "-b:a", "32k", "-f", "mp3", tmp.Name())
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		if ctx.Err() != nil {
			return nil, lvl, ctx.Err()
		}
		if strings.Contains(text, "does not contain any stream") || strings.Contains(text, "Output file is empty") ||
			strings.Contains(text, "matches no streams") {
			return nil, lvl, nil
		}
		return nil, lvl, fmt.Errorf("ffmpeg: %v %s", err, lastLine(text))
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, lvl, err
	}
	if len(b) == 0 {
		return nil, lvl, nil
	}
	lvl = audioLevel{mean: volume(meanVolumeRe, text), max: volume(maxVolumeRe, text)}
	return b, lvl, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// --- client -------------------------------------------------------------------

// transcribe sends audio to /audio/transcriptions.
func (c *aiClient) transcribe(ctx context.Context, audio []byte, name, model string) (*SpeechInfo, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("model", model)
	w.WriteField("response_format", "verbose_json")
	w.WriteField("temperature", "0")
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	ct := "audio/mpeg"
	if strings.HasSuffix(name, ".wav") {
		ct = "audio/wav"
	}
	h.Set("Content-Type", ct)
	fw, _ := w.CreatePart(h)
	fw.Write(audio)
	w.Close()
	var resp struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Segments []struct {
			Text       string   `json:"text"`
			AvgLogprob *float64 `json:"avg_logprob"`
			NoSpeech   *float64 `json:"no_speech_prob"`
		} `json:"segments"`
	}
	if err := c.send(ctx, http.MethodPost, "/audio/transcriptions", w.FormDataContentType(), body.Bytes(), &resp); err != nil {
		return nil, err
	}
	text := resp.Text
	if len(resp.Segments) > 0 {
		// drop segments the model was unsure of: sounds it turned into words
		var keep []string
		for _, s := range resp.Segments {
			if s.NoSpeech != nil && *s.NoSpeech > 0.6 {
				continue
			}
			if s.AvgLogprob != nil && *s.AvgLogprob < -1.0 {
				continue
			}
			keep = append(keep, strings.TrimSpace(s.Text))
		}
		text = strings.Join(keep, " ")
	}
	return &SpeechInfo{Text: cleanSpeech(text), Lang: langCode(resp.Language), Model: model, At: time.Now().Unix()}, nil
}

var (
	// sounds the models write down: *Musik*, [Lachen], (Applaus), ♪
	speechNoiseRe = regexp.MustCompile(`\*[^*]{0,40}\*|\[[^\]]{0,40}\]|\([^)]{0,40}\)|[♪♫]+`)
	// what the models invent for silence or noise
	speechPhantoms = map[string]bool{
		"vielen dank": true, "danke": true, "danke schön": true, "dankeschön": true, "tschüss": true,
		"vielen dank fürs zuschauen": true, "vielen dank für ihre aufmerksamkeit": true, "bis zum nächsten mal": true,
		"untertitel im auftrag des zdf": true, "untertitel im auftrag des zdf für funk": true,
		"untertitelung des zdf": true, "untertitel von stephanie geiges": true, "copyright wdr": true,
		"thank you": true, "thanks for watching": true, "thank you for watching": true, "bye": true, "you": true,
		"amen": true, "untertitel der amara.org-community": true, "untertitelung aufgrund der amara.org-community": true,
	}
	phantomTrimRe = regexp.MustCompile(`[\s.,!?…"'„“]+`)
	phantomYearRe = regexp.MustCompile(`[,\s]*\d{4}$`)
	// credits of subtitled TV the models learned from, wherever they appear
	phantomCreditRe = regexp.MustCompile(`(?i)[^.!?]*(untertitel|amara\.org|copyright)[^.!?]*[.!?]?`)
)

// cleanSpeech removes sound annotations, phrases models make up for
// silence, and endless repetitions.
func cleanSpeech(s string) string {
	s = speechNoiseRe.ReplaceAllString(s, " ")
	s = phantomCreditRe.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	norm := strings.ToLower(strings.Trim(phantomTrimRe.ReplaceAllString(s, " "), " "))
	norm = strings.TrimSpace(phantomYearRe.ReplaceAllString(norm, ""))
	if norm == "" || speechPhantoms[norm] || strings.Contains(norm, "amara.org") || strings.HasPrefix(norm, "untertitel") {
		return ""
	}
	words := strings.FieldsFunc(norm, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) >= 6 {
		uniq := map[string]bool{}
		for _, w := range words {
			uniq[w] = true
		}
		if float64(len(uniq))/float64(len(words)) < 0.3 {
			return "" // "hä, hä, hä, …"
		}
	}
	return s
}

// langCode turns "german" or "de" into "de".
func langCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "german", "deutsch":
		return "de"
	case "english":
		return "en"
	}
	if len(s) > 3 {
		return ""
	}
	return s
}

// SpeechTest is the result of the connection test on the settings page.
type SpeechTest struct {
	OK    bool   `json:"ok,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
	Error string `json:"error,omitempty"`
	Fatal bool   `json:"fatal,omitempty"`
}

// TestSpeech sends a second of silence: the model answers without text.
func TestSpeech(ctx context.Context, o SpeechOptions) SpeechTest {
	c := newAIClient(AIOptions{URL: o.URL, Key: o.Key, Model: o.Model})
	t0 := time.Now()
	if _, err := c.transcribe(ctx, silentWAV(), "test.wav", o.Model); err != nil {
		var ae *aiError
		return SpeechTest{Error: err.Error(), Fatal: errors.As(err, &ae) && ae.fatal}
	}
	return SpeechTest{OK: true, Ms: time.Since(t0).Milliseconds()}
}

// silentWAV is one second of silence, 16 kHz mono.
func silentWAV() []byte {
	const rate, n = 16000, 16000
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+n*2))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(n*2))
	b.Write(make([]byte, n*2))
	return b.Bytes()
}
