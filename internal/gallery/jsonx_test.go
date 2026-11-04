package gallery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLenientUnmarshal(t *testing.T) {
	cases := map[string]string{
		"quote in text":  `{"de": "Ein Schild mit "Halt" am Weg.", "en": "A sign saying "stop"."}`,
		"trailing comma": `{"de": "Ein Schild mit \"Halt\" am Weg.", "en": "A sign saying \"stop\".",}`,
		"single quotes":  `{'de': 'Ein Schild mit "Halt" am Weg.', 'en': 'A sign saying "stop".'}`,
		"in an array":    `[{"de": "Ein Schild mit \"Halt\" am Weg.", "en": "A sign saying \"stop\"."}]`,
		"prose, comment": "Sure:\n{\"de\": \"Ein Schild mit \\\"Halt\\\" am Weg.\", // German\n \"en\": \"A sign saying \\\"stop\\\".\"}\nDone.",
	}
	for name, in := range cases {
		var m map[string]any
		if err := lenientUnmarshal(in, &m); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if m["de"] != `Ein Schild mit "Halt" am Weg.` || m["en"] != `A sign saying "stop".` {
			t.Errorf("%s: %v", name, m)
		}
	}
	var m map[string]any
	if err := lenientUnmarshal(`{"x": True, "y": None, "z": [1, 2,]}`, &m); err != nil || m["x"] != true || m["y"] != nil || len(m["z"].([]any)) != 2 {
		t.Errorf("python literals: %v %v", m, err)
	}
	if err := lenientUnmarshal(`{"de":"a "quoted", word","en":"ok"}`, &m); err != nil || m["de"] != `a "quoted", word` {
		t.Errorf("quote and comma in a value: %v %v", m, err)
	}
	if err := lenientUnmarshal("{\"de\": \"A\" /* note */, \"en\" // key\n: \"B\"}", &m); err != nil || m["de"] != "A" || m["en"] != "B" {
		t.Errorf("comments after a value and a key: %v %v", m, err)
	}
	m = nil
	if err := lenientUnmarshal(`Example: /* {"de":"example"} */ {"de":"Ein Hund.", "en": "A dog" /* {"x":1} */}`, &m); err != nil || m["de"] != "Ein Hund." {
		t.Errorf("object in a comment: %v %v", m, err)
	}
	if err := lenientUnmarshal(`{'de': 'Ein Hund', 'en': 'A dog's toy'}`, &m); err != nil || m["en"] != "A dog's toy" {
		t.Errorf("apostrophe in a single-quoted value: %v %v", m, err)
	}
	if err := lenientUnmarshal("{\"de\": \"A\", // note\n /* more */ }", &m); err != nil || m["de"] != "A" {
		t.Errorf("trailing comma before a comment: %v %v", m, err)
	}
	for _, bad := range []string{"", `{"de": }`, `{"de": "cut off`, `{"de":"dog","detail":{"de":"cat"}`} {
		var m map[string]any
		if lenientUnmarshal(bad, &m) == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

// An answer that is no JSON even after repairs is asked for again, with
// the broken answer and the error in the conversation.
func TestAIAsksAgainForBadJSON(t *testing.T) {
	var calls atomic.Int32
	var second map[string]any
	good := `{"de":"Ein Hund.","en":"A dog.","tags_de":["hund"],"tags_en":["dog"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := `{"de": "Ein Hund", "en" "A dog"}` // missing colon
		if calls.Add(1) > 1 {
			json.NewDecoder(r.Body).Decode(&second)
			content = good
		} else {
			io.Copy(io.Discard, r.Body)
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}, "usage": map[string]any{"total_tokens": 100}})
		w.Write(b)
	}))
	defer srv.Close()
	c := newAIClient(AIOptions{URL: srv.URL + "/v1", Key: "k", Model: "vision-1"})
	a, err := c.describe(context.Background(), testPicture())
	if err != nil || a.En != "A dog." || calls.Load() != 2 || a.Tokens != 200 {
		t.Fatalf("describe: %+v %v (%d calls)", a, err, calls.Load())
	}
	msgs := second["messages"].([]any)
	if len(msgs) != 4 || msgs[2].(map[string]any)["role"] != "assistant" || !strings.Contains(msgs[3].(map[string]any)["content"].(string), "not valid JSON") {
		t.Errorf("second request: %v", msgs)
	}

	// always broken: the error after the retries
	calls.Store(-100)
	_, err = c.describe(context.Background(), testPicture())
	var ae *aiError
	if !errors.As(err, &ae) || !ae.badAnswer || calls.Load() != -100+1+aiAnswerRetries {
		t.Errorf("always broken: %v (%d calls)", err, calls.Load()+100)
	}
}

func TestCreateEmptyXMP(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/a.xmp"
	if err := createEmptyXMP(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != emptyXMP {
		t.Errorf("new: %q", b)
	}
	os.WriteFile(p, []byte("<x:xmpmeta>mine</x:xmpmeta>"), 0o644)
	if err := createEmptyXMP(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "<x:xmpmeta>mine</x:xmpmeta>" {
		t.Errorf("a sidecar with content was replaced: %q", b)
	}
}
