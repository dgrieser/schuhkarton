package gallery

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Videos are described from what moves in them, not from one frame: ffmpeg
// makes a small silent clip (H.264, at most 32 frames and 480 pixels on the
// long edge, spread over the whole video) that goes to the model as a
// video_url, together with the words spoken in the video. A service that
// takes no videos gets 8 frames of the clip as pictures instead.

const (
	clipFrames = 32  // frames of the clip at most
	clipEdge   = 480 // long edge of the clip
	clipFPS    = 2.0 // frames per second of short videos
	clipPics   = 8   // frames sent as pictures when the service takes no videos
	// the model refuses a clip of one frame ("Got bad request", HTTP 400)
	clipMinFrames = 2
	clipShort     = 4.0 // frames of a video shorter than one second
	spokenChars   = 3000
	// longer videos are sampled by jumping to each frame; shorter ones are
	// decoded whole, which reads the file once
	clipSeekAbove = 60.0
)

// videoClip makes the clip of a video in the cache folder and returns its
// path (the caller removes it) and how many frames it has at most.
func (l *Library) videoClip(ctx context.Context, it *Item) (string, int, error) {
	f, err := os.CreateTemp(l.opt.CacheDir, "clip-*.mp4")
	if err != nil {
		return "", 0, err
	}
	f.Close()
	scale := fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2", clipEdge, clipEdge)
	var in []string
	frames := clipFrames
	if d := it.Duration; d > clipSeekAbove {
		// long videos: one jump per frame reads only those places of the
		// file, instead of decoding all of it
		dir, err := os.MkdirTemp(l.opt.CacheDir, "seek-*")
		if err != nil {
			os.Remove(f.Name())
			return "", 0, err
		}
		defer os.RemoveAll(dir)
		if frames, err = l.seekFrames(ctx, it, dir, scale); err != nil {
			os.Remove(f.Name())
			return "", 0, err
		}
		in = []string{"-framerate", fmt.Sprintf("%.5f", float64(frames)/d), "-i", filepath.Join(dir, "f%02d.jpg")}
	} else {
		fps := clipFPS
		vf := ""
		switch {
		case d <= 0: // unknown: one frame a second from the start
			fps = 1
		case d*clipFPS > clipFrames: // 32 frames spread over all of it
			fps = clipFrames / d
		case d*clipFPS < clipMinFrames:
			// a fraction of a second: at 2 fps one frame or none would be
			// left, and the model takes no clip below two frames. A few
			// frames over all of it, and the last one once more for a video
			// of a single frame.
			fps = min(30, clipShort/d)
			vf = fmt.Sprintf("fps=%.5f,tpad=stop_mode=clone:stop=1,%s", fps, scale)
			frames = int(math.Ceil(d*fps)) + 1
		default:
			frames = max(clipMinFrames, int(math.Ceil(d*clipFPS)))
		}
		if vf == "" {
			vf = fmt.Sprintf("fps=%.5f,%s", fps, scale)
		}
		in = []string{"-i", l.Path(it), "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", vf}
	}
	run := func(codec ...string) error {
		args := append([]string{"-v", "error", "-nostdin", "-y"}, in...)
		args = append(append(args, "-frames:v", strconv.Itoa(clipFrames), "-pix_fmt", "yuv420p"), codec...)
		out, err := exec.CommandContext(ctx, l.media.ffmpeg, append(args, "-movflags", "+faststart", "-f", "mp4", f.Name())...).CombinedOutput()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ffmpeg: %v %s", err, lastLine(string(out)))
		}
		return nil
	}
	err = run("-c:v", "libx264", "-preset", "veryfast", "-crf", "28")
	if err != nil && ctx.Err() == nil && strings.Contains(err.Error(), "libx264") { // an ffmpeg without x264
		err = run("-c:v", "mpeg4", "-q:v", "5")
	}
	if err == nil {
		if st, serr := os.Stat(f.Name()); serr != nil || st.Size() == 0 {
			err = errors.New("ffmpeg: kein Bild im Video")
		}
	}
	if err != nil {
		os.Remove(f.Name())
		return "", 0, err
	}
	return f.Name(), frames, nil
}

// seekFrames writes 32 frames spread evenly over a long video into dir as
// f01.jpg, f02.jpg … (without gaps) and returns how many there are. Each is
// decoded from the key frame before it, so it shows that very moment.
func (l *Library) seekFrames(ctx context.Context, it *Item, dir, scale string) (int, error) {
	ok := make([]bool, clipFrames)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range clipFrames {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			at := (float64(i) + 0.5) * it.Duration / clipFrames
			out, err := exec.CommandContext(ctx, l.media.ffmpeg, "-v", "error", "-nostdin", "-y",
				"-ss", fmt.Sprintf("%.3f", at), "-i", l.Path(it), "-map", "0:v:0", "-frames:v", "1",
				"-vf", scale, "-q:v", "3", filepath.Join(dir, fmt.Sprintf("s%02d.jpg", i))).CombinedOutput()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("ffmpeg: %v %s", err, lastLine(string(out)))
				}
				return
			}
			ok[i] = exists(filepath.Join(dir, fmt.Sprintf("s%02d.jpg", i)))
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	n := 0
	for i, good := range ok {
		if good {
			n++
			if err := os.Rename(filepath.Join(dir, fmt.Sprintf("s%02d.jpg", i)), filepath.Join(dir, fmt.Sprintf("f%02d.jpg", n))); err != nil {
				return 0, err
			}
		}
	}
	if n == 0 {
		if firstErr == nil {
			firstErr = errors.New("ffmpeg: kein Bild im Video")
		}
		return 0, firstErr
	}
	return n, nil
}

// clipPictures returns n frames spread over the clip as JPEGs.
func (l *Library) clipPictures(ctx context.Context, clip string, frames, n int) ([][]byte, error) {
	dir, err := os.MkdirTemp(l.opt.CacheDir, "frames-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	every := max(1, int(math.Ceil(float64(frames)/float64(n))))
	out, err := exec.CommandContext(ctx, l.media.ffmpeg, "-v", "error", "-nostdin", "-y", "-i", clip,
		// new times without gaps, or the output would fill them with copies
		"-vf", fmt.Sprintf("select='not(mod(n\\,%d))',setpts=N/FRAME_RATE/TB", every),
		"-frames:v", strconv.Itoa(n), "-q:v", "4", filepath.Join(dir, "f%02d.jpg")).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg: %v %s", err, lastLine(string(out)))
	}
	var pics [][]byte
	for i := 1; i <= n; i++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("f%02d.jpg", i)))
		if err != nil {
			break
		}
		pics = append(pics, b)
	}
	if len(pics) == 0 {
		return nil, errors.New("ffmpeg: keine Bilder aus dem Video")
	}
	return pics, nil
}

// speechPending reports a video whose sound is still to be transcribed: its
// description waits for the words, unless speech recognition has stopped
// (wrong settings).
func (l *Library) speechPending(it *Item) bool {
	j := &l.speechJob
	if !l.SpeechApplies(it) {
		return false
	}
	if e, _ := j.err.Load().(string); e != "" && j.paused.Load() == 0 {
		return false
	}
	l.speech.mu.Lock()
	s := l.speech.items[l.favKey(it)]
	l.speech.mu.Unlock()
	return s == nil
}

// kickAISoon starts the descriptions a little later: videos that waited for
// their words, transcribed one after the other, go in one pass.
func (l *Library) kickAISoon() {
	if l.aiJob.client == nil || l.aiSoon.Swap(true) {
		return
	}
	time.AfterFunc(2*time.Minute, func() {
		l.aiSoon.Store(false)
		l.kickAI()
	})
}

// describeVideo describes a video from its clip. done is false when no
// clip could be made and the video has no description yet: then the poster
// frame is described instead. The error stops the job (service problems).
func (l *Library) describeVideo(ctx context.Context, it *Item) (done bool, err error) {
	if l.unreadable(it) != nil {
		return false, nil // no clip: the picture's turn, which reports it
	}
	j := &l.aiJob
	old := l.AIOf(it)
	clip, frames, err := l.videoClip(ctx, it)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		l.logf("describe %s: %v", it.Rel, err)
		if old == nil {
			return false, nil
		}
		old.From, old.Tried = "frame", time.Now().Unix() // keep the description from one frame, try again later
		l.setAI(it, old)
		j.handled(false, 0)
		return true, nil
	}
	defer os.Remove(clip)
	info, err := j.client.describeVideo(ctx, clip, it.Duration, l.SpeechText(it),
		func() ([][]byte, error) { return l.clipPictures(ctx, clip, frames, clipPics) })
	if err != nil {
		if stop := jobStop(ctx, err); stop != nil {
			return true, stop
		}
		l.logf("describe %s: %v", it.Rel, err)
		if old != nil {
			old.From, old.Tried = "frame", time.Now().Unix()
			l.setAI(it, old)
			j.handled(false, 0)
		} else {
			l.setAI(it, &AIInfo{Err: err.Error(), At: time.Now().Unix(), Model: j.client.opt.Model})
			j.handled(true, 0)
		}
		return true, nil
	}
	l.setAI(it, info)
	l.search.updateAI(it.ID, info)
	j.handled(false, info.Tokens)
	l.kickXMP()
	return true, nil
}

// describeVideo sends the clip with the spoken words; pictures returns its
// frames for services that take no videos.
func (c *aiClient) describeVideo(ctx context.Context, clip string, seconds float64, spoken string, pictures func() ([][]byte, error)) (*AIInfo, error) {
	if !c.noVideo.Load() {
		b, err := os.ReadFile(clip)
		if err != nil {
			return nil, err
		}
		part := map[string]any{"type": "video_url", "video_url": map[string]any{"url": "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(b)}}
		info, err := c.ask(ctx, c.videoPrompt(seconds, spoken, 0), part)
		if err == nil {
			info.From = "video"
			return info, nil
		}
		var ae *aiError
		if !errors.As(err, &ae) || !videoRefused(ae) {
			return nil, err
		}
		if ae.status != 413 { // too large: only this one
			c.noVideo.Store(true)
		}
	}
	pics, err := pictures()
	if err != nil {
		return nil, err
	}
	parts := make([]any, len(pics))
	for i, p := range pics {
		parts[i] = imagePart(p)
	}
	info, err := c.ask(ctx, c.videoPrompt(seconds, spoken, len(pics)), parts...)
	if err != nil {
		return nil, err
	}
	info.From = "frames"
	return info, nil
}

// videoRefused: the service does not take this video or none at all.
func videoRefused(ae *aiError) bool {
	switch ae.status {
	case 413:
		return true
	case 400, 415, 422:
		return strings.Contains(strings.ToLower(ae.msg), "video")
	}
	return false
}

// videoPrompt asks for the description of a video given as a clip (pics 0)
// or as pics frames, with the words spoken in it.
func (c *aiClient) videoPrompt(seconds float64, spoken string, pics int) string {
	var b strings.Builder
	if pics > 0 {
		fmt.Fprintf(&b, "You get %d frames spread evenly over the video, in order.\n", pics)
	}
	if seconds > 0 {
		fmt.Fprintf(&b, "The video is %s long.\n", videoLength(seconds))
	}
	b.WriteString("Describe what happens in the whole video, not a single moment.\n")
	if spoken = strings.TrimSpace(spoken); spoken != "" {
		if len(spoken) > spokenChars {
			cut := spokenChars
			for cut > 0 && !utf8.RuneStart(spoken[cut]) {
				cut--
			}
			spoken = spoken[:cut] + " …"
		}
		b.WriteString("Words spoken in the video (automatic transcript, may contain mistakes): «" + spoken + "»\n" +
			"Use them to understand the situation, but do not copy them.\n")
	}
	return c.prompt("video", b.String())
}

func videoLength(s float64) string {
	if s < 90 {
		return fmt.Sprintf("%.0f seconds", math.Max(1, math.Round(s)))
	}
	return fmt.Sprintf("%.0f minutes", math.Round(s/60))
}
