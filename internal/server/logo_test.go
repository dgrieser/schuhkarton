package server

import (
	"io"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/web"
)

func TestWithLogo(t *testing.T) {
	page := []byte(`<link rel="icon" href="/assets/logo/aqua.svg"><link rel="apple-touch-icon" href="/assets/logo/aqua-180.png"><img src="/assets/logo/aqua.svg">`)
	got := string(withLogo(page, "rose"))
	if strings.Contains(got, "aqua") || strings.Count(got, "/assets/logo/rose.") != 2 || !strings.Contains(got, "/assets/logo/rose-180.png") {
		t.Errorf("rose: %s", got)
	}
	for _, l := range []string{"", "aqua", "../secret"} {
		if got := string(withLogo(page, l)); got != string(page) {
			t.Errorf("%q changed the page: %s", l, got)
		}
	}
}

// every name in config.Logos has its files, and every page names the default
func TestLogoFiles(t *testing.T) {
	for _, n := range config.Logos {
		for _, f := range []string{n + ".svg", n + ".ico", n + "-180.png"} {
			if _, err := fs.Stat(web.FS, "logo/"+f); err != nil {
				t.Errorf("logo/%s: %v", f, err)
			}
		}
	}
	for _, p := range []string{"index.html", "login.html", "setup.html"} {
		b, _ := fs.ReadFile(web.FS, p)
		if !strings.Contains(string(b), "/assets/logo/"+config.Logos[0]+".svg") {
			t.Errorf("%s does not use the logo", p)
		}
	}
}

func TestFavicon(t *testing.T) {
	logo := "mono"
	h := faviconHandler(web.FS, func() string { return logo })
	want, _ := fs.ReadFile(web.FS, "logo/mono.ico")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.ico", nil))
	if b, _ := io.ReadAll(rec.Body); rec.Code != 200 || string(b) != string(want) {
		t.Errorf("mono: %d, %d bytes", rec.Code, len(b))
	}
	logo = "nope" // not a variant: the default
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.ico", nil))
	if rec.Code != 200 {
		t.Errorf("unknown logo: %d", rec.Code)
	}
}
