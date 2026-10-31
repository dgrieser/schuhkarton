package gallery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestParseAIAnswer(t *testing.T) {
	cases := map[string]string{
		"plain":  `{"de":"Ein Hund am Strand.","en":"A dog on the beach.","tags_de":["hund","strand"],"tags_en":["dog","beach"],"people":0}`,
		"fenced": "Hier:\n```json\n{\"de\":\"Ein Hund am Strand.\",\"en\":\"A dog on the beach.\",\"tags_de\":\"Hund, Strand\",\"tags_en\":[\"Dog\",\"beach\",\"dog\"],\"people\":\"0\"}\n```",
		"think":  "<think>looks like a dog</think>{\"de\":\"Ein Hund am Strand.\",\"en\":\"A dog on the beach.\",\"tags_de\":[\"hund\",\"strand\"],\"tags_en\":[\"dog\",\"beach\"]}",
		"cut":    "the dog …</think>\n{\"de\":\"Ein Hund am Strand.\",\"en\":\"A dog on the beach.\",\"tags_de\":[\"hund\",\"strand\"],\"tags_en\":[\"dog\",\"beach\"]}",
	}
	for name, in := range cases {
		a, err := parseAIAnswer(in)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if a.De != "Ein Hund am Strand." || a.En != "A dog on the beach." || strings.Join(a.TagsDe, ",") != "hund,strand" || strings.Join(a.TagsEn, ",") != "dog,beach" {
			t.Errorf("%s: %+v", name, a)
		}
	}
	for _, bad := range []string{"no json here", `{"foo":1}`, `{"de": }`} {
		if _, err := parseAIAnswer(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

// fakeAI is an OpenAI-compatible server: /models and /chat/completions.
type fakeAI struct {
	status  atomic.Int32 // answer with this HTTP status when not 0
	calls   atomic.Int32
	noKwarg bool // refuse chat_template_kwargs like a strict server
	mu      sync.Mutex
	lastReq map[string]any
}

func (f *fakeAI) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"invalid key"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"object":"list","data":[{"id":"vision-1"},{"id":"text-1"}]}`)
		case "/v1/chat/completions":
			f.calls.Add(1)
			if s := f.status.Load(); s != 0 {
				w.WriteHeader(int(s))
				io.WriteString(w, `{"error":{"message":"busy"}}`)
				return
			}
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			f.mu.Lock()
			f.lastReq = req
			f.mu.Unlock()
			if _, ok := req["chat_template_kwargs"]; ok && f.noKwarg {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Unrecognized request argument: chat_template_kwargs"}}`)
				return
			}
			if req["model"] != "vision-1" {
				w.WriteHeader(404)
				io.WriteString(w, `{"error":{"message":"The model does not exist."}}`)
				return
			}
			answer := `{"de":"Ein Hund spielt im Schnee.","en":"A dog plays in the snow.","tags_de":["hund","schnee","winter"],"tags_en":["dog","snow","winter"],"people":1}`
			b, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": "```json\n" + answer + "\n```"}}},
				"usage":   map[string]any{"total_tokens": 321},
			})
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAIClientErrors(t *testing.T) {
	f := &fakeAI{}
	srv := f.server(t)
	ctx := context.Background()
	c := newAIClient(AIOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "vision-1"})
	a, err := c.describe(ctx, testPicture())
	if err != nil || a.En != "A dog plays in the snow." || a.Tokens != 321 || a.People != 1 {
		t.Fatalf("describe: %+v %v", a, err)
	}
	content := f.lastReq["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if url := content[1].(map[string]any)["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Errorf("image not sent as data URL: %.40s", url)
	}
	if _, ok := f.lastReq["chat_template_kwargs"]; !ok {
		t.Error("thinking should be switched off")
	}
	var ae *aiError
	// wrong key: fatal
	bad := newAIClient(AIOptions{URL: srv.URL + "/v1", Key: "nope", Model: "vision-1"})
	if _, err := bad.describe(ctx, testPicture()); !errors.As(err, &ae) || !ae.fatal {
		t.Errorf("401 should be fatal: %v", err)
	}
	// unknown model: fatal
	wrong := newAIClient(AIOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "nope"})
	if _, err := wrong.describe(ctx, testPicture()); !errors.As(err, &ae) || !ae.fatal {
		t.Errorf("404 model should be fatal: %v", err)
	}
	// busy: transient
	f.status.Store(429)
	if _, err := c.describe(ctx, testPicture()); !errors.As(err, &ae) || !ae.transient {
		t.Errorf("429 should be transient: %v", err)
	}
	f.status.Store(0)
	// a server without chat_template_kwargs: retried without
	f.noKwarg = true
	if _, err := c.describe(ctx, testPicture()); err != nil {
		t.Errorf("retry without chat_template_kwargs: %v", err)
	}
	// models and the setup test
	res := TestAI(ctx, AIOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "vision-1"}, true)
	if res.Error != "" || len(res.Models) != 2 || res.Answer == nil {
		t.Errorf("TestAI: %+v", res)
	}
	if res := TestAI(ctx, AIOptions{URL: srv.URL + "/v1", Key: "nope"}, true); !res.Fatal {
		t.Errorf("TestAI with a wrong key: %+v", res)
	}
}

func TestAIDescriptionsSearchAndMove(t *testing.T) {
	f := &fakeAI{}
	srv := f.server(t)
	root := t.TempDir()
	cache := t.TempDir()
	for _, p := range []string{"2020/01 Januar Lübbecke/a.jpg", "2020/01 Januar Lübbecke/b.jpg"} {
		writeTestJPEG(t, filepath.Join(root, p), 64, 48)
	}
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: cache, Workers: 1,
		AI: AIOptions{URL: srv.URL + "/v1", Key: "test-key", Model: "vision-1", Workers: 2}}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.describeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("calls: %d", n)
	}
	st := l.Status()
	if st.AIMode != "on" || st.AIDone != 2 || st.AITodo != 2 || st.AITokens != 642 {
		t.Fatalf("status: %+v", st)
	}
	ids := func(q, lang string) []string {
		var out []string
		for _, h := range l.Search(q, "", lang) {
			out = append(out, h.ID)
		}
		sort.Strings(out)
		return out
	}
	a := byRel(l, "2020/01 Januar Lübbecke/a.jpg")
	if got := ids("schnee", "de"); len(got) != 2 {
		t.Errorf("German tag: %v", got)
	}
	if got := ids("dog", "en"); len(got) != 2 {
		t.Errorf("English tag: %v", got)
	}
	if got := ids("ki:spielt", "de"); len(got) != 2 {
		t.Errorf("ai field: %v", got)
	}
	if hits := l.Search("plays", "", "en"); len(hits) == 0 || !strings.Contains(hits[0].Snip, "\x01plays\x02") {
		t.Errorf("snippet from the English description: %+v", hits)
	}
	// a second pass has nothing to do
	if err := l.describeAll(ctx); err != nil || f.calls.Load() != 2 {
		t.Fatalf("second pass: %v, calls %d", err, f.calls.Load())
	}
	// moving keeps the description
	res, err := l.Move([]string{a.ID}, "B", "Anderswo")
	if err != nil || res.Changed != 1 {
		t.Fatalf("move: %+v %v", res, err)
	}
	moved := byRel(l, "Anderswo/a.jpg")
	if moved == nil || l.AIOf(moved) == nil {
		t.Fatalf("description lost on move: %+v", moved)
	}
	// redescribe and persistence across a restart
	if n := l.Redescribe([]string{moved.ID}); n != 1 || l.AIOf(moved) != nil {
		t.Fatalf("redescribe: %d", n)
	}
	l.Close()
	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if err := l2.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if b := byRel(l2, "2020/01 Januar Lübbecke/b.jpg"); b == nil || l2.AIOf(b) == nil {
		t.Fatal("description not kept across a restart")
	}
	if m := byRel(l2, "Anderswo/a.jpg"); m == nil || l2.AIOf(m) != nil {
		t.Fatal("redescribed item should wait for a new description")
	}
	// a fatal error stops the pass and is reported
	l2.aiJob.client.opt.Key = "nope"
	if err := l2.describeAll(ctx); err == nil {
		t.Fatal("expected the pass to stop on a wrong key")
	}
}
