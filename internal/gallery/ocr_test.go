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

func TestOCRTranslate(t *testing.T) {
	f := &fakeAI{}
	srv := f.server(t)
	root := t.TempDir()
	writeTestJPEG(t, filepath.Join(root, "Dokumente/2020/zettel.jpg"), 64, 48)
	writeTestJPEG(t, filepath.Join(root, "Dokumente/2020/nummer.jpg"), 64, 48)
	writeTestJPEG(t, filepath.Join(root, "Dokumente/2020/kaputt.jpg"), 64, 48)
	opt := Options{Roots: []Root{{Name: "B", Path: root}}, CacheDir: t.TempDir(), Workers: 1,
		OCR: OCROptions{URL: srv.URL + "/v1", Key: "test-key", Model: "GLM-OCR", Workers: 1, Scope: "documents",
			Translate: "vision-1", Languages: []string{"de", "en"}}}
	l, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	doc, num := byRel(l, "Dokumente/2020/zettel.jpg"), byRel(l, "Dokumente/2020/nummer.jpg")
	// texts read by an older version, not translated yet: only numbers, and
	// one the model gives no usable answer for
	l.setOCR(num, &OCRInfo{Text: "12345", Model: "GLM-OCR", At: 1})
	bad := byRel(l, "Dokumente/2020/kaputt.jpg")
	l.setOCR(bad, &OCRInfo{Text: "kaputt", Model: "GLM-OCR", At: 1})
	if err := l.recognizeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if ocr, tr := f.calls.Load()-f.trCalls.Load(), f.trCalls.Load(); ocr != 1 || tr != 3 {
		t.Fatalf("one picture read, three texts translated: %d, %d", ocr, tr)
	}
	if o := l.OCROf(bad); o == nil || o.TrErr == "" || o.TrAt == 0 || o.Text != "kaputt" {
		t.Fatalf("failed translation: %+v", o)
	}
	o := l.OCROf(doc)
	if o == nil || o.Lang != "de" || o.De != "" || o.En != "Shopping list\n\nMilk\nBread" || o.TrModel != "vision-1" || o.TrAt == 0 || o.TrErr != "" {
		t.Fatalf("translation: %+v", o)
	}
	if o := l.OCROf(num); o == nil || o.Lang != "" || o.De != "" || o.En != "" || o.TrAt == 0 || o.Text != "12345" {
		t.Fatalf("numbers only: %+v", o)
	}
	if st := l.Status(); st.OCRTokens != 900+3*77 || st.OCRFail != 1 {
		t.Errorf("tokens %d, failed %d", st.OCRTokens, st.OCRFail)
	}
	if hits := l.Search("shopping", "", "en"); len(hits) != 1 || hits[0].ID != doc.ID {
		t.Errorf("english search: %+v", hits)
	}
	// nothing left to do; the failed one waits for ai.retry_failed
	if err := l.recognizeAll(ctx); err != nil || f.calls.Load() != 4 {
		t.Fatalf("second pass: %v, calls %d", err, f.calls.Load())
	}
	if st := l.Status(); st.OCRFail != 1 || st.OCRDone != 3 {
		t.Errorf("recounted: failed %d, done %d", st.OCRFail, st.OCRDone)
	}
	// a translation of a text read again meanwhile is dropped
	if l.setTranslation(doc, &OCRInfo{Text: "alt", At: 1}, ocrTranslation{lang: "en", de: "falsch"}) {
		t.Fatal("stale translation stored")
	}
	// read again: recognized and translated again
	if n := l.Rerecognize([]string{doc.ID}); n != 1 {
		t.Fatalf("rerecognize: %d", n)
	}
	if err := l.recognizeAll(ctx); err != nil || f.calls.Load() != 6 || f.trCalls.Load() != 4 {
		t.Fatalf("after rerecognize: %v, calls %d, translations %d", err, f.calls.Load(), f.trCalls.Load())
	}
	// the translations survive a restart
	l.Close()
	l2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if err := l2.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if o := l2.OCROf(byRel(l2, "Dokumente/2020/zettel.jpg")); o == nil || o.En == "" || o.Lang != "de" {
		t.Fatalf("after restart: %+v", o)
	}
}

func TestParseTranslation(t *testing.T) {
	langs := []string{"de", "en"}
	tr, err := parseTranslation("```\n<<<lang>>>\nfr\n<<<de>>>\nGuten Tag\n<<<en>>>\nGood day  \n```", "Bonjour", langs)
	if err != nil || tr.lang != "fr" || tr.de != "Guten Tag" || tr.en != "Good day" {
		t.Errorf("french: %+v %v", tr, err)
	}
	// the same as the text: no translation
	if tr, err := parseTranslation("<<<lang>>>\n\"deu\"\n<<<en>>>\nOK  Computer", "ok computer", langs); err != nil || tr.lang != "de" || tr.en != "" {
		t.Errorf("same: %+v %v", tr, err)
	}
	if _, err := parseTranslation("<<<lang>>>\nde\n", "Hallo", langs); err == nil {
		t.Error("missing translation not noticed")
	}
	if _, err := parseTranslation("Hello", "Hallo", langs); err == nil {
		t.Error("answer without sections not noticed")
	}
	if tr, err := parseTranslation("<<<lang>>>\nnone", "12-34", langs); err != nil || tr.lang != "" {
		t.Errorf("none: %+v %v", tr, err)
	}
	for _, bad := range []string{"<<<lang>>>\n\n<<<en>>>\nHi", "<<<lang>>>\n-", "<<<lang>>>\nunknown language"} {
		if _, err := parseTranslation(bad, "Hallo", langs); err == nil {
			t.Errorf("no valid language not noticed: %q", bad)
		}
	}
}
