package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
