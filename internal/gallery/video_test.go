package gallery

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDescribeVideos: a video is described from a small clip with its spoken
// words, after the speech job heard them; a description from one frame is
// replaced; a service without video input gets frames instead.
func TestDescribeVideos(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	et := FindExifTool("")
	if err != nil || et == "" {
		t.Skip("ffmpeg or exiftool not installed")
	}
	for _, noVideo := range []bool{false, true} {
		f := &fakeAI{noVideo: noVideo}
		srv := f.server(t)
		root := t.TempDir()
		out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=s=640x360:r=25:d=6", "-f", "lavfi",
			"-i", "sine=frequency=440:sample_rate=16000:duration=6", "-shortest", "-c:v", "mpeg4", "-c:a", "aac",
			filepath.Join(root, "talk.mp4")).CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, FFmpeg: ff, ExifTool: et,
			AI:     AIOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "vision-1", Workers: 1},
			Speech: SpeechOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "whisper-large-v3-turbo", Workers: 1}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := l.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		v := byRel(l, "talk.mp4")
		if v.Duration < 5 {
			t.Fatalf("duration %v", v.Duration)
		}
		l.setAI(v, &AIInfo{De: "Ein Standbild.", En: "A still.", Model: "vision-1", At: 1}) // from one frame, before
		// the description waits for the words
		if err := l.describeAll(ctx); err != nil {
			t.Fatal(err)
		}
		if n := f.calls.Load(); n != 0 {
			t.Fatalf("described before the words came: %d calls", n)
		}
		if err := l.transcribeAll(ctx); err != nil {
			t.Fatal(err)
		}
		if err := l.describeAll(ctx); err != nil {
			t.Fatal(err)
		}
		videos, pictures, prompt := f.media()
		a := l.AIOf(v)
		switch {
		case a == nil || a.De != "Ein Hund spielt im Schnee.":
			t.Errorf("noVideo %v: description %+v", noVideo, a)
		case !noVideo && (a.From != "video" || videos != 1 || pictures != 0 || f.calls.Load() != 1):
			t.Errorf("clip: from %q, %d videos, %d pictures, %d calls", a.From, videos, pictures, f.calls.Load())
		case noVideo && (a.From != "frames" || videos != 0 || pictures < 4 || pictures > clipPics || f.calls.Load() != 2):
			t.Errorf("frames: from %q, %d videos, %d pictures, %d calls", a.From, videos, pictures, f.calls.Load())
		}
		if !strings.Contains(prompt, "«Hallo Oma, guck mal!»") || !strings.Contains(prompt, "6 seconds") ||
			!strings.Contains(prompt, "Describe this video") {
			t.Errorf("prompt: %s", prompt)
		}
		if noVideo && !l.aiJob.client.noVideo.Load() {
			t.Error("the refusal is not remembered")
		}
		// new words make a new description, which waits for them
		if !noVideo {
			l.Retranscribe([]string{v.ID})
			if a := l.AIOf(v); a == nil || a.From != "" {
				t.Errorf("after retranscribing: %+v", a)
			}
			calls := f.calls.Load()
			if err := l.describeAll(ctx); err != nil || f.calls.Load() != calls {
				t.Errorf("described before the new words: %v", err)
			}
			if err := l.transcribeAll(ctx); err != nil {
				t.Fatal(err)
			}
			if err := l.describeAll(ctx); err != nil || f.calls.Load() != calls+1 || l.AIOf(v).From != "video" {
				t.Errorf("not described again: %v %d", err, f.calls.Load()-calls)
			}
		}
		// a clip that failed is tried again after a week
		state := l.aiTask().state
		for tried, want := range map[int64]bool{time.Now().Unix(): true, time.Now().Add(-8 * 24 * time.Hour).Unix(): false} {
			l.setAI(v, &AIInfo{De: "Ein Standbild.", Model: "vision-1", At: 1, From: "frame", Tried: tried})
			if done, _ := state(v, time.Now()); done != want {
				t.Errorf("frame tried %v: handled %v", time.Unix(tried, 0), done)
			}
		}
		l.setAI(v, a)
		// nothing left to do
		before := f.calls.Load()
		if err := l.describeAll(ctx); err != nil || f.calls.Load() != before {
			t.Errorf("again: %v, %d calls", err, f.calls.Load()-before)
		}
		l.Close()
	}
}

func TestVideoClipLong(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	et := FindExifTool("")
	if err != nil || et == "" {
		t.Skip("ffmpeg or exiftool not installed")
	}
	root := t.TempDir()
	// key frames only every 30 seconds: the frames between them must show too
	out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=s=1280x720:r=10:d=90", "-g", "300",
		"-c:v", "mpeg4", filepath.Join(root, "long.mp4")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, FFmpeg: ff, ExifTool: et})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := byRel(l, "long.mp4")
	clip, frames, err := l.videoClip(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	cnt, err := exec.Command(filepath.Join(filepath.Dir(ff), "ffprobe"), "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames,width,height", "-of", "csv=p=0", clip).Output()
	if err == nil {
		// 480 px wide, 32 frames spread over the 90 seconds
		var w, h, n int
		fmt.Sscanf(strings.TrimSpace(string(cnt)), "%d,%d,%d", &w, &h, &n)
		if w != 480 || h != 270 || n != clipFrames {
			t.Errorf("clip: %s", cnt)
		}
		// all different (testsrc shows a running clock)
		sums, _ := exec.Command(ff, "-v", "error", "-i", clip, "-f", "framemd5", "-").Output()
		seen := map[string]bool{}
		for _, ln := range strings.Split(string(sums), "\n") {
			if f := strings.Split(ln, ","); len(f) == 6 && !strings.HasPrefix(ln, "#") {
				seen[strings.TrimSpace(f[5])] = true
			}
		}
		if len(seen) < clipFrames-1 {
			t.Errorf("only %d different frames", len(seen))
		}
	}
	if frames != clipFrames || it.Duration != 90 {
		t.Errorf("frames %d of %vs", frames, it.Duration)
	}
	pics, err := l.clipPictures(context.Background(), clip, frames, clipPics)
	if err != nil || len(pics) != clipPics {
		t.Errorf("pictures: %d %v", len(pics), err)
	}
	distinct := map[string]bool{}
	for _, p := range pics {
		distinct[string(p)] = true
	}
	if len(distinct) != len(pics) {
		t.Errorf("%d of %d pictures differ: copies instead of frames spread over the video", len(distinct), len(pics))
	}
}
