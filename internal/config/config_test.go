package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadSaveEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.NeedsSetup() || c.Found() {
		t.Fatalf("fresh config should need setup")
	}
	c.Library.Folders = []Folder{{Path: "/srv/Bilder"}}
	c.Faces.Mode = "remote"
	c.Faces.URL = "http://faces:8089"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	t.Setenv("SCHUHKARTON_SERVER_LISTEN", "0.0.0.0:9000")
	t.Setenv("SCHUHKARTON_LIBRARY_FOLDERS", "Urlaub=/srv/urlaub,/srv/x")
	c2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Server.Listen != "0.0.0.0:9000" || c2.Locked("server.listen") != "SCHUHKARTON_SERVER_LISTEN" {
		t.Errorf("env listen: %q locked %q", c2.Server.Listen, c2.Locked("server.listen"))
	}
	if len(c2.Library.Folders) != 2 || c2.Library.Folders[0].Name != "Urlaub" || c2.Library.Folders[1].Name != "x" {
		t.Errorf("folders: %+v", c2.Library.Folders)
	}
	if c2.Faces.URL != "http://faces:8089" || !c2.Found() {
		t.Errorf("file values lost: %+v", c2.Faces)
	}
}

func TestValidate(t *testing.T) {
	c := Defaults()
	c.Faces.Mode = "remote"
	if c.Validate() == nil {
		t.Error("remote without url must fail")
	}
	c.Faces.Mode = "off"
	c.Library.Rescan = "soon"
	if c.Validate() == nil {
		t.Error("bad rescan must fail")
	}
	c.Library.Rescan = ""
	for _, bad := range []string{"3 days", "10m"} {
		c.AI.RetryFailed = bad
		if c.Validate() == nil {
			t.Errorf("ai.retry_failed %q must fail", bad)
		}
	}
	c.AI.RetryFailed = "24h"
	if err := c.Validate(); err != nil || c.RetryFailed() != 24*time.Hour {
		t.Errorf("ai.retry_failed 24h: %v %v", err, c.RetryFailed())
	}
	c.Faces.SamePhotoThreshold = 1.2
	if c.Validate() == nil {
		t.Error("a similarity above 1 must fail")
	}
	c.Faces.SamePhotoThreshold = 0.75
	for _, bad := range []func(*Config){
		func(c *Config) { c.Media.JPEGQuality = 150 },
		func(c *Config) { c.Media.ThumbSize = 10 },
		func(c *Config) { c.AI.ImageSize = 99999 },
		func(c *Config) { c.Server.TLSCert = "/nonexistent/cert.pem" }, // without a key
		func(c *Config) { c.Server.TLSCert, c.Server.TLSKey = "/nonexistent/c.pem", "/nonexistent/k.pem" },
		func(c *Config) { c.UI.MapTiles = "tiles.example/{z}/{x}/{y}.png" },
	} {
		d := Defaults()
		bad(d)
		if d.Validate() == nil {
			t.Errorf("must fail: %+v %+v %+v %+v", d.Media, d.AI.ImageSize, d.Server, d.UI.MapTiles)
		}
	}
}

func TestAIKey(t *testing.T) {
	t.Setenv("SK_TEST_AI_KEY", "secret-from-env")
	c := Defaults()
	if c.AI.Mode != "off" || c.AI.URL != MittwaldAIURL || c.AI.Key() != "" {
		t.Fatalf("defaults: %+v", c.AI)
	}
	c.AI.APIKeyEnv = "SK_TEST_AI_KEY"
	if c.AI.Key() != "secret-from-env" {
		t.Fatal("key from the environment")
	}
	c.AI.APIKey = "direct"
	if c.AI.Key() != "direct" || c.Masked().AI.APIKey != "********" {
		t.Fatal("direct key wins and is masked")
	}
	c.AI.Mode, c.AI.URL = "on", "ftp://x"
	if c.Validate() == nil {
		t.Fatal("ai.url must be http(s)")
	}
}

func TestRulesInFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("library:\n  rules:\n    list:\n      - {name: Fotos, path: \"{jahr}/{ort}\"}\n      - {name: Leer, path: \" \"}\n"), 0o600)
	c, err := Load(p)
	if err == nil {
		err = c.Validate()
	}
	if err == nil || !strings.Contains(err.Error(), "Leer") {
		t.Fatalf("a rule without path must be reported, got %v", err)
	}
	c = Defaults()
	c.Library.Rules.List = []Rule{{Path: "{jahr}"}}
	c.Library.Rules.Locale = "xx"
	if c.Validate() == nil {
		t.Error("unknown locale must fail")
	}
}

func TestImportFolder(t *testing.T) {
	lib := t.TempDir()
	c := Defaults()
	c.Library.Folders = []Folder{{Name: "Bilder", Path: lib}}
	c.Library.Import.Dir = filepath.Join(lib, "Import")
	if c.Validate() == nil {
		t.Error("import folder inside the library must fail")
	}
	c.Library.Import.Dir = filepath.Join(t.TempDir(), "Import")
	if err := c.Validate(); err != nil {
		t.Errorf("import folder beside the library: %v", err)
	}
	c.Library.Import.Folder = "Fehlt"
	if c.Validate() == nil {
		t.Error("unknown library folder must fail")
	}
}

func TestImportFolderLink(t *testing.T) {
	lib := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(lib, link); err != nil {
		t.Skip(err)
	}
	c := Defaults()
	c.Library.Folders = []Folder{{Name: "Bilder", Path: lib}}
	c.Library.Import.Dir = filepath.Join(link, "Import")
	if c.Validate() == nil {
		t.Error("import folder in the library through a link must fail")
	}
}

func TestImportFolderLinkToCache(t *testing.T) {
	cache := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(cache, link); err != nil {
		t.Skip(err)
	}
	c := Defaults()
	c.Library.Folders = []Folder{{Name: "Bilder", Path: t.TempDir()}}
	c.Cache.Dir = cache
	c.Library.Import.Dir = link
	if c.Validate() == nil {
		t.Error("import folder that is the cache through a link must fail")
	}
}

func TestImportFolderByPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	a, b := t.TempDir(), t.TempDir()
	c := Defaults()
	c.SetPath(p)
	c.Library.Folders = []Folder{{Name: "Erster", Path: a}, {Name: "", Path: b}} // the second without name
	c.Library.Import = Import{Dir: filepath.Join(t.TempDir(), "Import"), Folder: b}
	if err := c.Validate(); err != nil { // the settings page checks before saving
		t.Fatalf("import folder by path refused: %v", err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(b); c.Library.Import.Folder != want || c.Library.Folders[1].Name != want {
		t.Fatalf("import folder %q, folders %+v", c.Library.Import.Folder, c.Library.Folders)
	}
}

// One data folder holds config, accounts and cache, and can move as a whole.
func TestDataFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCHUHKARTON_HOME", home)
	t.Setenv("SCHUHKARTON_CONFIG", "")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Path() != filepath.Join(home, "config.yaml") || c.Cache.Dir != filepath.Join(home, "cache") {
		t.Fatalf("paths: %s %s", c.Path(), c.Cache.Dir)
	}
	c.Library.Folders = []Folder{{Name: "Bilder", Path: t.TempDir()}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(c.Path())
	if !strings.Contains(string(raw), "dir: cache\n") {
		t.Fatalf("cache not written relative:\n%s", raw)
	}
	// moved elsewhere: the cache comes along
	moved := filepath.Join(t.TempDir(), "neu")
	os.Rename(home, moved)
	t.Setenv("SCHUHKARTON_HOME", moved)
	c, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Cache.Dir != filepath.Join(moved, "cache") {
		t.Fatalf("cache after the move: %s", c.Cache.Dir)
	}
}

// A relative data folder is taken from the working directory, once.
func TestDataFolderRelative(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("SCHUHKARTON_HOME", "data")
	t.Setenv("SCHUHKARTON_CONFIG", "")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "data", "cache")
	if c.Cache.Dir != want {
		t.Fatalf("cache %s, want %s", c.Cache.Dir, want)
	}
	c.Library.Folders = []Folder{{Name: "Bilder", Path: t.TempDir()}}
	c.Save()
	if c, _ = Load(""); c.Cache.Dir != want {
		t.Fatalf("after saving: %s", c.Cache.Dir)
	}
}

func TestOCRSettings(t *testing.T) {
	c := Defaults()
	c.Library.Folders = []Folder{{Path: "/tmp"}}
	c.normalize()
	if c.AI.OCR != "off" || c.AI.OCRModel != "GLM-OCR" || c.AI.OCRWorkers != 2 || c.AI.Used() {
		t.Fatalf("defaults: %+v", c.AI)
	}
	c.AI.OCR = "Documents"
	c.normalize()
	if c.AI.OCR != "documents" || !c.AI.Used() || c.Validate() != nil {
		t.Fatalf("documents: %q used=%v err=%v", c.AI.OCR, c.AI.Used(), c.Validate())
	}
	c.AI.URL = "ftp://x"
	if c.Validate() == nil {
		t.Error("text recognition needs an http(s) address")
	}
	c.AI.URL, c.AI.OCR = MittwaldAIURL, "sometimes"
	if c.Validate() == nil {
		t.Error("unknown ai.ocr should be refused")
	}
	if err := c.applyEnv(func(k string) (string, bool) {
		if k == "SCHUHKARTON_AI_OCR" {
			return "all", true
		}
		return "", false
	}); err != nil || c.AI.OCR != "all" || c.Locked("ai.ocr") == "" {
		t.Errorf("env: %q %v", c.AI.OCR, err)
	}
}

func TestSpeechAndMetadataSettings(t *testing.T) {
	c := Defaults()
	c.Library.Folders = []Folder{{Path: "/tmp"}}
	c.normalize()
	if c.AI.Speech != "off" || c.AI.SpeechModel != "whisper-large-v3-turbo" || c.AI.SpeechFallback != "cohere-transcribe-03-2026" ||
		c.AI.SpeechWorkers != 2 || c.AI.Used() {
		t.Fatalf("defaults: %+v", c.AI)
	}
	if !c.Library.WriteMetadata {
		t.Error("writing into the files is on by default")
	}
	c.AI.Speech = "ON"
	c.normalize()
	if c.AI.Speech != "on" || !c.AI.Used() || c.Validate() != nil {
		t.Fatalf("on: %q used=%v err=%v", c.AI.Speech, c.AI.Used(), c.Validate())
	}
	c.AI.Speech = "maybe"
	if c.Validate() == nil {
		t.Error("unknown ai.speech should be refused")
	}
	c.AI.Speech = "off"
	if err := c.applyEnv(func(k string) (string, bool) {
		switch k {
		case "SCHUHKARTON_AI_SPEECH":
			return "on", true
		case "SCHUHKARTON_LIBRARY_WRITE_METADATA":
			return "false", true
		}
		return "", false
	}); err != nil || c.AI.Speech != "on" || c.Library.WriteMetadata || c.Locked("library.write_metadata") == "" {
		t.Errorf("env: %q %v %v", c.AI.Speech, c.Library.WriteMetadata, err)
	}
}

func TestSlowDecodes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("library:\n  folders: [{path: /srv/Bilder}]\nmedia:\n  slow_decodes: 0\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Media.SlowDecodes != 1 {
		t.Errorf("default %d", c.Media.SlowDecodes)
	}
	t.Setenv("SCHUHKARTON_MEDIA_SLOW_DECODES", "3")
	if c, _ = Load(p); c.Media.SlowDecodes != 3 || c.Locked("media.slow_decodes") == "" {
		t.Errorf("env %d", c.Media.SlowDecodes)
	}
}

func TestCacheCap(t *testing.T) {
	for in, want := range map[string]int64{"4GB": 4 << 30, "500 MB": 500 << 20, "2g": 2 << 30, "1048576": 1 << 20, "1.5GiB": 3 << 29, "": 0} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"viel", "10000000000000000000", "1e30T", "Inf"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	c := Defaults()
	c.Cache.Dir = "/var/cache/sk"
	c.Library.Folders = []Folder{{Path: "/srv/Bilder"}}
	if c.Cache.MaxBytes() != -1 {
		t.Error("no cap")
	}
	c.Cache.MaxSize = "4GB"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "overflow_dir") {
		t.Errorf("cap without overflow: %v", err)
	}
	for _, bad := range []string{"nas/cache", "/var/cache/sk/over", "/srv/Bilder/cache", "/srv"} {
		c.Cache.OverflowDir = bad
		if err := c.Validate(); err == nil {
			t.Errorf("overflow %q accepted", bad)
		}
	}
	t.Setenv("HOME", "/home/x")
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("library:\n  folders: [{path: /srv/Bilder}]\ncache:\n  max_size: 4GB\n  overflow_dir: ~/nas-cache\n"), 0o600)
	if l, err := Load(p); err != nil || l.Cache.OverflowDir != "/home/x/nas-cache" || l.Validate() != nil {
		t.Errorf("~ in overflow_dir: %v %q", err, l.Cache.OverflowDir)
	}
	// a symbolic link into a photo folder
	photos, links := t.TempDir(), t.TempDir()
	os.Symlink(photos, filepath.Join(links, "cache"))
	c.Library.Folders = []Folder{{Path: photos}}
	c.Cache.OverflowDir = filepath.Join(links, "cache", "sub")
	if err := c.Validate(); err == nil {
		t.Error("overflow through a link into the photos accepted")
	}
	c.Library.Folders = []Folder{{Path: "/srv/Bilder"}}
	c.Cache.OverflowDir = "/mnt/nas/cache"
	if err := c.Validate(); err != nil || c.Cache.MaxBytes() != 4<<30 {
		t.Errorf("valid: %v %d", err, c.Cache.MaxBytes())
	}
}

func TestBackgroundWindows(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte(`library:
  folders: [{path: /srv/Bilder}]
background:
  windows:
    - {days: mon-fri, from: "23:00", to: "08:00"}
    - {days: "sat,sun", from: "00:00", to: "07:00"}
  once:
    - {from: "2000-01-01 09:00", to: "13:00"}
    - {from: "2999-01-01 09:00", to: "13:00", jobs: [dupes, backup]}
`), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Background.Once) != 1 || !c.PrunedOnce() { // the one in 2000 is over
		t.Errorf("once %+v", c.Background.Once)
	}
	w := c.Window()
	if len(w.Rules) != 2 || len(w.Extra) != 1 || w.Rules[0].From != 23*60 || len(w.Extra[0].Jobs) != 2 {
		t.Errorf("window %+v", w)
	}
	c.Background.Once[0].Jobs = []string{"thumbnails"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "unknown kind of work") {
		t.Errorf("unknown kind: %v", err)
	}
	c.Background.Once[0].Jobs = []string{"dupes"}
	c.Background.Windows = append(c.Background.Windows, BackgroundWindow{Days: "feiertags", From: "10:00", To: "11:00"})
	c.Background.Once = append(c.Background.Once, BackgroundOnce{From: "morgen", To: "13:00"})
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "background.windows 3") || !strings.Contains(err.Error(), "background.once 2") {
		t.Errorf("bad windows: %v", err)
	}
	// the older form still works
	c2 := Defaults()
	c2.Background = Background{From: "23:00", To: "05:00"}
	if w := c2.Window(); len(w.Rules) != 1 || w.Rules[0].Days != 127 {
		t.Errorf("older form %+v", w)
	}
}
