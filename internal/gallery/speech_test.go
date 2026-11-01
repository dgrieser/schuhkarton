package gallery

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVolume(t *testing.T) {
	out := "[Parsed_volumedetect_0 @ 0x1] mean_volume: -52.3 dB\n[Parsed_volumedetect_0 @ 0x1] max_volume: -18.0 dB\n"
	if m, x := volume(meanVolumeRe, out), volume(maxVolumeRe, out); m != -52.3 || x != -18 {
		t.Errorf("volume: %v %v", m, x)
	}
	// long and quiet, but a few loud words: not silence
	if m, x := volume(meanVolumeRe, out), volume(maxVolumeRe, out); m < speechQuiet && x < speechPeak {
		t.Error("loud peaks must keep the video")
	}
	if v := volume(meanVolumeRe, "mean_volume: -inf dB"); !math.IsInf(v, -1) {
		t.Errorf("silence: %v", v)
	}
}

func TestCleanSpeech(t *testing.T) {
	for in, want := range map[string]string{
		" Guten Morgen, Mama. ":               "Guten Morgen, Mama.",
		" Vielen Dank.":                       "",
		"Tschüss!":                            "",
		" * Musik *":                          "",
		"*Motor*  *Motor*  *Motor*":           "",
		"[Lachen] Da ist der Hund!":           "Da ist der Hund!",
		"Untertitel im Auftrag des ZDF, 2017": "",
		"Hä, hä, hä, hä, hä, hä, hä, hä, hä, hä.":            "",
		"Untertitel der Amara.org-Community":                 "",
		"Das ist die Nummer 76. Untertitelung des ZDF, 2020": "Das ist die Nummer 76.",
		"Nein, mach das nicht, Lilly. Ist das ewig? Ja.":     "Nein, mach das nicht, Lilly. Ist das ewig? Ja.",
	} {
		if got := cleanSpeech(in); got != want {
			t.Errorf("cleanSpeech(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTranscribeVideos sends the sound of a video to the model and skips a
// silent one; needs ffmpeg for the test videos.
func TestTranscribeVideos(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	root := t.TempDir()
	mk := func(name, audio string) {
		t.Helper()
		out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x48:d=1", "-f", "lavfi", "-i", audio,
			"-t", "1", "-shortest", "-c:v", "mpeg4", "-c:a", "aac", filepath.Join(root, name)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	mk("talk.mp4", "sine=frequency=440:sample_rate=16000:duration=1")
	mk("quiet.mp4", "anullsrc=r=16000:cl=mono")
	writeTestJPEG(t, filepath.Join(root, "photo.jpg"), 40, 30)
	f := &fakeAI{}
	srv := f.server(t)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, FFmpeg: ff,
		Speech: SpeechOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "whisper-large-v3-turbo", Workers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.transcribeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.speechCalls.Load(); n != 1 {
		t.Errorf("only the video with sound goes to the model: %d calls", n)
	}
	talk := byRel(l, "talk.mp4")
	s := l.speechOf(talk)
	if s == nil || s.Text != "Hallo Oma, guck mal!" || s.Lang != "de" {
		t.Fatalf("speech: %+v", s)
	}
	if q := l.speechOf(byRel(l, "quiet.mp4")); q == nil || q.Text != "" {
		t.Errorf("silent video: %+v", q)
	}
	if l.SpeechApplies(byRel(l, "photo.jpg")) {
		t.Error("a photo without Live Photo video has no sound")
	}
	hits := l.Search("oma", "", "de")
	if len(hits) != 1 || hits[0].ID != talk.ID {
		t.Errorf("search: %+v", hits)
	}
	if res := TestSpeech(ctx, SpeechOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "whisper-large-v3-turbo"}); !res.OK {
		t.Errorf("test: %+v", res)
	}
}

// TestSpeechFallback: when the first model hears nothing in loud sound, the
// second one listens; a second model the service lacks changes nothing.
func TestSpeechFallback(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	f := &fakeAI{}
	srv := f.server(t)
	for fallback, want := range map[string]string{"cohere-x": "Guck mal, Philip.", "missing-model": ""} {
		root := t.TempDir()
		out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x48:d=1", "-f", "lavfi",
			"-i", "sine=frequency=440:sample_rate=16000:duration=1", "-t", "1", "-shortest", "-c:v", "mpeg4", "-c:a", "aac",
			filepath.Join(root, "talk.mp4")).CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1, FFmpeg: ff,
			Speech: SpeechOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "whisper-silent", Fallback: fallback, Workers: 1}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := l.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if err := l.transcribeAll(ctx); err != nil {
			t.Fatal(err)
		}
		s := l.speechOf(byRel(l, "talk.mp4"))
		if s == nil || s.Text != want {
			t.Errorf("%s: %+v", fallback, s)
		} else if want != "" && s.Model != fallback {
			t.Errorf("%s: model %q", fallback, s.Model)
		}
		l.Close()
	}
}
