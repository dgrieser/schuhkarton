package server

import (
	"bytes"
	"hash/fnv"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dgrieser/schuhkarton/internal/config"
)

// logoVisits counts the visits of the gallery for ui.logo_change "visit".
// It starts somewhere at random, so a restart does not always begin with
// the same colour.
var logoVisits atomic.Uint64

func init() { logoVisits.Store(rand.Uint64() >> 1) }

// currentLogo is the colour the pages show now. With several colours they
// take turns: each round through all of them in a new order, no colour
// twice in a row, also not from one round to the next. The order follows
// from the time (or the visits), so all pages and all viewers agree
// without anything being stored. visit: this call is a new visit
// (the gallery page); other pages and the favicon show the current one.
func currentLogo(ui config.UI, visit bool) string {
	return logoAt(ui.Logo, logoPeriod(ui.LogoChange, visit, time.Now()))
}

// logoPeriod numbers the periods of ui.logo_change: hours, days or weeks
// (Monday to Sunday) of local time, or visits.
func logoPeriod(change string, visit bool, now time.Time) uint64 {
	y, m, d := now.Date()
	day := uint64(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
	switch change {
	case "visit":
		if visit {
			return logoVisits.Add(1)
		}
		return logoVisits.Load()
	case "hourly":
		return day*24 + uint64(now.Hour())
	case "weekly":
		return (day + 3) / 7 // 1 January 1970 was a Thursday
	default: // daily
		return day
	}
}

// logoAt is the colour of period p: round p/n in a shuffled order,
// position p%n in it.
func logoAt(list []string, p uint64) string {
	var ok []string
	for _, n := range list {
		if slices.Contains(config.Logos, n) {
			ok = append(ok, n)
		}
	}
	switch len(ok) {
	case 0:
		return config.Logos[0]
	case 1, 2: // two only alternate
		return ok[p%uint64(len(ok))]
	}
	n := uint64(len(ok))
	round := logoRound(ok, p/n)
	if p/n > 0 {
		// the first of a round must not be the last of the one before;
		// swapping the first two keeps the last of this round as it was
		if prev := logoRound(ok, p/n-1); round[0] == prev[n-1] {
			round[0], round[1] = round[1], round[0]
		}
	}
	return round[p%n]
}

// logoRound shuffles the colours for one round, the same way every time.
func logoRound(list []string, round uint64) []string {
	h := fnv.New64a()
	h.Write([]byte(strings.Join(list, ",")))
	out := slices.Clone(list)
	rand.New(rand.NewPCG(round, h.Sum64())).Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// withLogo points the logo and the icons of a page (written as the default,
// /assets/logo/aqua.*) to the given colour. Every colour has its own file
// names, so browsers do not keep showing the old icon.
func withLogo(page []byte, logo string) []byte {
	if logo == "" || logo == config.Logos[0] || !slices.Contains(config.Logos, logo) {
		return page
	}
	for _, sep := range []string{".", "-"} { // aqua.svg, aqua.ico, aqua-180.png
		page = bytes.ReplaceAll(page, []byte("/assets/logo/"+config.Logos[0]+sep), []byte("/assets/logo/"+logo+sep))
	}
	return page
}

// faviconHandler answers /favicon.ico, where browsers and feed readers look
// without being told, with the icon of the current colour; the pages
// themselves point to /assets/logo/.
func faviconHandler(files fs.FS, ui func() config.UI) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, files, "logo/"+currentLogo(ui(), false)+".ico")
	})
}
