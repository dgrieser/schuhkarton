package server

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/web"
)

// The settings page sends weekly and one-time windows; they reach the file.
func TestMergeBackgroundWindows(t *testing.T) {
	cur := config.Defaults()
	cur.Background = config.Background{From: "23:00", To: "05:00"}
	in := config.Defaults()
	in.Background = config.Background{
		Windows: []config.BackgroundWindow{{Days: "mon-fri", From: "23:00", To: "08:00"}, {Days: "sat,sun", From: "00:00", To: "07:00"}},
		Once:    []config.BackgroundOnce{{From: "2999-11-03 09:00", To: "13:00"}},
	}
	in.Cache.MaxSize, in.Cache.OverflowDir, in.Media.SlowDecodes = "2GB", "/mnt/nas/cache", 2
	n := merge(cur, in)
	if n.Background.From != "" || len(n.Background.Windows) != 2 || len(n.Background.Once) != 1 {
		t.Errorf("background %+v", n.Background)
	}
	if w := n.Window(); len(w.Rules) != 2 || len(w.Extra) != 1 {
		t.Errorf("window %+v", w)
	}
	if n.Cache.MaxSize != "2GB" || n.Cache.OverflowDir != "/mnt/nas/cache" || n.Media.SlowDecodes != 2 {
		t.Errorf("cache %+v media %d", n.Cache, n.Media.SlowDecodes)
	}
}

// The face thresholds of the settings page reach the file; a form without
// them keeps the ones in use.
func TestMergeFaceThresholds(t *testing.T) {
	cur := config.Defaults()
	in := config.Defaults()
	in.Faces.MinScore, in.Faces.ClusterThreshold, in.Faces.NamedThreshold, in.Faces.SamePhotoThreshold = 0.9, 0.5, 0.45, 0.8
	n := merge(cur, in)
	if f := n.Faces; f.MinScore != 0.9 || f.ClusterThreshold != 0.5 || f.NamedThreshold != 0.45 || f.SamePhotoThreshold != 0.8 {
		t.Errorf("faces %+v", f)
	}
	in = config.Defaults()
	in.Faces.SamePhotoThreshold = 0
	if n = merge(n, in); n.Faces.SamePhotoThreshold != 0.8 {
		t.Errorf("missing value replaced the one in use: %v", n.Faces.SamePhotoThreshold)
	}
}

// Every setting of the page reaches the file: previews, tools, local
// faces, the worker, AI details, HTTPS and the map.
func TestMergeAllSettings(t *testing.T) {
	cur := config.Defaults()
	in := config.Defaults()
	in.Media.ThumbSize, in.Media.PreviewSize, in.Media.JPEGQuality, in.Media.VideoPreviewSize = 400, 2560, 88, 320
	in.Media.ExifTool, in.Media.FFmpeg = " /opt/exiftool ", "/opt/ffmpeg"
	in.Faces.Python, in.Faces.ModelsDir = "/usr/bin/python3", "/srv/models"
	in.Worker.Listen, in.Worker.Token, in.Worker.Workers = "0.0.0.0:9000", "wtok", 3
	in.AI.ImageSize, in.AI.Thinking, in.AI.RetryFailed = 1536, true, "24h"
	in.Server.TLSCert, in.Server.TLSKey = "/etc/c.pem", "/etc/k.pem"
	in.UI.MapTiles, in.UI.MapAttribution = "https://tiles.example/{z}/{x}/{y}.png", "© Example"
	in.Dupes.SimilarMinutes = 30
	n := merge(cur, in)
	if m := n.Media; m.ThumbSize != 400 || m.PreviewSize != 2560 || m.JPEGQuality != 88 || m.VideoPreviewSize != 320 || m.ExifTool != "/opt/exiftool" || m.FFmpeg != "/opt/ffmpeg" {
		t.Errorf("media %+v", m)
	}
	if f, w := n.Faces, n.Worker; f.Python != "/usr/bin/python3" || f.ModelsDir != "/srv/models" || w.Listen != "0.0.0.0:9000" || w.Token != "wtok" || w.Workers != 3 {
		t.Errorf("faces %+v worker %+v", f, w)
	}
	if a := n.AI; a.ImageSize != 1536 || !a.Thinking || a.RetryFailed != "24h" {
		t.Errorf("ai %+v", a)
	}
	if s, u := n.Server, n.UI; s.TLSCert != "/etc/c.pem" || s.TLSKey != "/etc/k.pem" || u.MapTiles != in.UI.MapTiles || u.MapAttribution != "© Example" {
		t.Errorf("server %+v ui %+v", s, u)
	}
	if n.Dupes.SimilarMinutes != 30 {
		t.Errorf("dupes %+v", n.Dupes)
	}
	// numbers the form did not send keep their value; empty map tiles mean the default map
	in = config.Defaults()
	in.Media.ThumbSize, in.UI.MapTiles = 0, ""
	if n = merge(n, in); n.Media.ThumbSize != 400 || n.UI.MapTiles != config.Defaults().UI.MapTiles {
		t.Errorf("thumb %d tiles %q", n.Media.ThumbSize, n.UI.MapTiles)
	}
}

// A running instance sends the settings page right away, not the
// first-start welcome; every replacement still finds its text.
func TestSettingsPage(t *testing.T) {
	b, err := fs.ReadFile(web.FS, "setup.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range settingsReplacements("/x/") {
		if !bytes.Contains(b, []byte(r[0])) {
			t.Errorf("setup.html lacks %q", r[0])
		}
	}
	out := string(settingsPage(b, "/schuhkarton/"))
	if strings.Contains(out, "Schuhkarton einrichten") || strings.Contains(out, "In wenigen Schritten") || !strings.Contains(out, `href="/schuhkarton/"`) {
		t.Errorf("settings page still shows the welcome or the wrong link")
	}
}
