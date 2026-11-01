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
