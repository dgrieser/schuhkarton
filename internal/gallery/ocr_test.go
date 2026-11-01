package gallery

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanOCR(t *testing.T) {
	cases := map[string]string{
		"```markdown\nZeile 1\n\n\n\nZeile 2   \n```": "Zeile 1\n\nZeile 2",
		"\n\n  Nur Text \r\nmit CRLF\n":               "Nur Text\nmit CRLF",
		"":                                            "",
	}
	for in, want := range cases {
		if got := cleanOCR(in); got != want {
			t.Errorf("cleanOCR(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOCRDocumentsSearchAndMove(t *testing.T) {
	f := &fakeAI{}
	srv := f.server(t)
	root := t.TempDir()
	cache := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "Dokumente/2020/zettel.jpg"), 64, 48)
	writeTestJPEG(t, filepath.Join(root, "2020/strand.jpg"), 64, 48)
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: cache, Workers: 1,
		OCR: OCROptions{URL: srv.URL + "/v1", Key: "test-key", Model: "GLM-OCR", Workers: 2, Scope: "documents"}}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.recognizeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("only the document should be read: %d calls", n)
	}
	content := f.lastReq["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if p := content[1].(map[string]any)["text"]; p != "Text Recognition:" {
		t.Errorf("GLM-OCR prompt: %v", p)
	}
	doc := byRel(l, "Dokumente/2020/zettel.jpg")
	if got := l.OCRText(doc); got != "Einkaufszettel\n\nMilch\nBrot" {
		t.Fatalf("text: %q", got)
	}
	if st := l.Status(); st.OCRMode != "documents" || st.OCRDone != 1 || st.OCRTodo != 1 || st.OCRTokens != 900 {
		t.Errorf("status: %+v", st)
	}
	for _, q := range []string{"milch", "text:einkaufszettel", "typ:text"} {
		hits := l.Search(q, "", "de")
		if len(hits) != 1 || hits[0].ID != doc.ID {
			t.Errorf("%s: %+v", q, hits)
		}
	}
	if hits := l.Search("milch", "", "de"); len(hits) == 1 && !strings.Contains(hits[0].Snip, "\x01Milch\x02") {
		t.Errorf("snippet: %q", hits[0].Snip)
	}
	// a second pass has nothing to do
	if err := l.recognizeAll(ctx); err != nil || f.calls.Load() != 1 {
		t.Fatalf("second pass: %v, calls %d", err, f.calls.Load())
	}
	// moving keeps the text
	if res, err := l.Move([]string{doc.ID}, "B", "Dokumente/Anderswo"); err != nil || res.Changed != 1 {
		t.Fatalf("move: %+v %v", res, err)
	}
	moved := byRel(l, "Dokumente/Anderswo/zettel.jpg")
	if moved == nil || l.OCRText(moved) == "" {
		t.Fatalf("text lost on move: %+v", moved)
	}
	// a picture outside the scope keeps its text: the job would not read it again
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err := l.Move([]string{moved.ID}, "B", "2021"); err != nil || res.Changed != 1 {
		t.Fatalf("move out: %+v %v", res, err)
	}
	out := byRel(l, "2021/zettel.jpg")
	if n := l.Rerecognize([]string{out.ID}); n != 0 || l.OCRText(out) == "" {
		t.Fatalf("out of scope: %d, text %q", n, l.OCRText(out))
	}
	if res, err := l.Move([]string{out.ID}, "B", "Dokumente/Anderswo"); err != nil || res.Changed != 1 {
		t.Fatalf("move back: %+v %v", res, err)
	}
	moved = byRel(l, "Dokumente/Anderswo/zettel.jpg")
	// recognize again, and the text survives a restart
	if n := l.Rerecognize([]string{moved.ID}); n != 1 || l.OCRText(moved) != "" {
		t.Fatalf("rerecognize: %d", n)
	}
	if err := l.recognizeAll(ctx); err != nil || f.calls.Load() != 2 {
		t.Fatalf("after rerecognize: %v, calls %d", err, f.calls.Load())
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
	if m := byRel(l2, "Dokumente/Anderswo/zettel.jpg"); m == nil || l2.OCRText(m) == "" {
		t.Fatal("text not kept across a restart")
	}
}

func TestOCRScopeAll(t *testing.T) {
	f := &fakeAI{}
	srv := f.server(t)
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "Dokumente/zettel.jpg"), 64, 48)
	writeTestJPEG(t, filepath.Join(root, "2020/strand.jpg"), 64, 48)
	l, err := Open(Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1,
		OCR: OCROptions{URL: srv.URL + "/v1", Key: "test-key", Model: "GLM-OCR", Scope: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.recognizeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("every photo should be read: %d calls", n)
	}
}

func TestTestOCR(t *testing.T) {
	f := &fakeAI{}
	srv := f.server(t)
	ctx := context.Background()
	if r := TestOCR(ctx, OCROptions{URL: srv.URL + "/v1", Key: "test-key", Model: "GLM-OCR"}); r.Error != "" || !strings.Contains(r.Text, "Milch") {
		t.Errorf("TestOCR: %+v", r)
	}
	if r := TestOCR(ctx, OCROptions{URL: srv.URL + "/v1", Key: "nope", Model: "GLM-OCR"}); !r.Fatal {
		t.Errorf("wrong key should be fatal: %+v", r)
	}
	if len(ocrTestPicture()) < 1000 {
		t.Error("test picture too small")
	}
}
