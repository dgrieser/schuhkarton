package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgrieser/schuhkarton/internal/auth"
	"github.com/dgrieser/schuhkarton/internal/config"
)

// The AI key never leaves the server, "********" keeps it, and $VARIABLE
// stores only the variable's name.
func TestSetupAIKey(t *testing.T) {
	dir := t.TempDir()
	users, _ := auth.Open(filepath.Join(dir, "users.json"))
	users.Create("Admin", "admin-pass", auth.RoleAdmin, false)
	cfg := config.Defaults()
	cfg.SetPath(filepath.Join(dir, "config.yaml"))
	cfg.Library.Folders = []config.Folder{{Name: "B", Path: t.TempDir()}}
	cfg.AI.Mode, cfg.AI.APIKey = "on", "very-secret-key"
	var saved *config.Config
	applied := make(chan bool, 4)
	h := NewSetup(SetupOptions{Install: true, Token: "tok", Users: users, Config: func() *config.Config { return cfg },
		Listen: func() string { return "127.0.0.1:1" },
		Apply: func(ctx context.Context, p Plan, progress func(Progress)) (string, error) {
			saved = p.Config
			applied <- true
			return "/", nil
		}}).Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: setupCookie, Value: "tok"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	st := do("GET", "/api/setup/state", "")
	if strings.Contains(st.Body.String(), "very-secret-key") {
		t.Fatal("state leaks the AI key")
	}
	var state struct {
		Config config.Config `json:"config"`
	}
	json.Unmarshal(st.Body.Bytes(), &state)
	if state.Config.AI.APIKey != "********" {
		t.Fatalf("key not masked: %q", state.Config.AI.APIKey)
	}
	save := func(key string) *config.Config {
		c := state.Config
		c.AI.APIKey = key
		b, _ := json.Marshal(map[string]any{"config": c, "tokenMode": "keep"})
		w := do("POST", "/api/setup/save", string(b))
		if !strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatalf("save: %s", w.Body.String())
		}
		<-applied
		for range 100 { // the progress box resets after Apply
			if p := do("GET", "/api/setup/progress", "").Body.String(); strings.Contains(p, `"finished":true`) {
				break
			}
		}
		return saved
	}
	if c := save("********"); c.AI.APIKey != "very-secret-key" {
		t.Fatalf("masked value must keep the key: %q", c.AI.APIKey)
	}
	if c := save("$MITTWALD_LLM_API_KEY"); c.AI.APIKey != "" || c.AI.APIKeyEnv != "MITTWALD_LLM_API_KEY" {
		t.Fatalf("variable: key %q env %q", c.AI.APIKey, c.AI.APIKeyEnv)
	}
	// text recognition settings are saved too
	state.Config.AI.OCR, state.Config.AI.OCRModel, state.Config.AI.OCRWorkers = "documents", "GLM-OCR", 3
	if c := save("********"); c.AI.OCR != "documents" || c.AI.OCRModel != "GLM-OCR" || c.AI.OCRWorkers != 3 {
		t.Fatalf("ocr settings: %q %q %d", c.AI.OCR, c.AI.OCRModel, c.AI.OCRWorkers)
	}
}
