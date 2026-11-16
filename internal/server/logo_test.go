package server

import (
	"io"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		for _, f := range []string{n + ".svg", n + "-loading.svg", n + ".ico", n + "-180.png"} {
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
	ui := config.UI{Logo: config.LogoList{"mono"}, LogoChange: "daily"}
	h := faviconHandler(web.FS, func() config.UI { return ui })
	want, _ := fs.ReadFile(web.FS, "logo/mono.ico")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.ico", nil))
	if b, _ := io.ReadAll(rec.Body); rec.Code != 200 || string(b) != string(want) {
		t.Errorf("mono: %d, %d bytes", rec.Code, len(b))
	}
	ui.Logo = config.LogoList{"nope"} // not a variant: the default
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.ico", nil))
	if rec.Code != 200 {
		t.Errorf("unknown logo: %d", rec.Code)
	}
}

// several colours: every one in each round, never the same twice in a row
func TestLogoTurns(t *testing.T) {
	for _, list := range [][]string{{"aqua", "mono"}, {"aqua", "peach", "rose"}, config.Logos} {
		n := uint64(len(list))
		var prev string
		for p := uint64(0); p < 200*n; p++ {
			got := logoAt(list, p)
			if got == prev {
				t.Fatalf("%v: %q twice in a row at %d", list, got, p)
			}
			prev = got
			if p%n == n-1 { // a round is done: all of them
				seen := map[string]bool{}
				for q := p + 1 - n; q <= p; q++ {
					seen[logoAt(list, q)] = true
				}
				if uint64(len(seen)) != n {
					t.Fatalf("%v: round to %d has %d colours", list, p, len(seen))
				}
			}
		}
	}
	// the order is not simply the list
	same := true
	for p := uint64(0); p < 30; p++ {
		if logoAt(config.Logos, p) != config.Logos[p%uint64(len(config.Logos))] {
			same = false
		}
	}
	if same {
		t.Error("not shuffled")
	}
	if got := logoAt([]string{"rose"}, 12345); got != "rose" {
		t.Errorf("one colour: %q", got)
	}
}

func TestLogoPeriod(t *testing.T) {
	mon := time.Date(2026, 11, 16, 10, 0, 0, 0, time.Local) // a Monday
	sun := time.Date(2026, 11, 22, 23, 0, 0, 0, time.Local)
	nextMon := time.Date(2026, 11, 23, 0, 30, 0, 0, time.Local)
	if logoPeriod("weekly", false, mon) != logoPeriod("weekly", false, sun) || logoPeriod("weekly", false, sun) == logoPeriod("weekly", false, nextMon) {
		t.Error("weeks do not run Monday to Sunday")
	}
	if logoPeriod("daily", false, sun) == logoPeriod("daily", false, nextMon) || logoPeriod("hourly", false, mon) == logoPeriod("hourly", false, mon.Add(time.Hour)) {
		t.Error("days or hours do not change")
	}
	a := logoPeriod("visit", true, mon)
	if b := logoPeriod("visit", false, mon); b != a {
		t.Error("looking is a visit")
	}
	if c := logoPeriod("visit", true, mon); c != a+1 {
		t.Error("a visit does not count")
	}
}
