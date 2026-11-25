package server

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// slowFirstByte is how long a request may take until its answer starts
// before it is logged: a slow answer is the evidence when the gallery
// seems to hang (a share that stopped answering, decodes queueing).
const slowFirstByte = 3 * time.Second

// slowLog logs requests whose answer started late, with what they waited
// for. Long downloads and videos are fine: only the time to the first byte
// counts.
func slowLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &firstByteWriter{ResponseWriter: w, start: time.Now()}
		next.ServeHTTP(sw, r)
		sw.mark(http.StatusOK)
		reqTimes.add(r.Method+" "+routeOf(r.URL.Path), sw.first.Sub(sw.start))
		if d := sw.first.Sub(sw.start); d >= slowFirstByte {
			log.Printf("slow request: %s %s %d after %s", r.Method, r.URL.Path, sw.status, d.Round(100*time.Millisecond))
		}
	})
}

type firstByteWriter struct {
	http.ResponseWriter
	start, first time.Time
	status       int
	once         sync.Once
}

func (w *firstByteWriter) mark(status int) {
	w.once.Do(func() { w.first, w.status = time.Now(), status })
}

func (w *firstByteWriter) WriteHeader(code int) {
	w.mark(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *firstByteWriter) Write(b []byte) (int, error) {
	w.mark(http.StatusOK)
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach flushing and deadlines.
func (w *firstByteWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush keeps streamed answers streaming.
func (w *firstByteWriter) Flush() {
	w.mark(http.StatusOK)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// reqTimes collects how long the answers took to start, by route; every
// timesEvery the log gets a line per route with requests (count, median,
// 95th percentile, longest): the measure of how fluently the page runs.
var reqTimes = &routeTimes{by: map[string][]time.Duration{}}

const (
	timesEvery = 10 * time.Minute
	maxRoutes  = 100
)

type routeTimes struct {
	mu    sync.Mutex
	by    map[string][]time.Duration
	since time.Time
}

func (t *routeTimes) add(route string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.since.IsZero() {
		t.since = time.Now()
	}
	if _, ok := t.by[route]; !ok && len(t.by) >= maxRoutes {
		route = "other" // made-up paths must not grow it without end
	}
	if len(t.by[route]) < 10000 {
		t.by[route] = append(t.by[route], d)
	}
	if time.Since(t.since) >= timesEvery {
		by := t.by
		t.by, t.since = map[string][]time.Duration{}, time.Time{}
		go logTimes(by)
	}
}

func logTimes(by map[string][]time.Duration) {
	routes := make([]string, 0, len(by))
	for r := range by {
		routes = append(routes, r)
	}
	slices.Sort(routes)
	var b strings.Builder
	for _, r := range routes {
		ds := by[r]
		slices.Sort(ds)
		q := func(f float64) time.Duration { return ds[min(len(ds)-1, int(f*float64(len(ds))))] }
		ms := func(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond)) }
		fmt.Fprintf(&b, "; %s n=%d p50=%s p95=%s max=%s", r, len(ds), ms(q(0.5)), ms(q(0.95)), ms(ds[len(ds)-1]))
	}
	if b.Len() > 0 {
		log.Printf("request times (ms)%s", b.String())
	}
}

// routeOf is a path without what names one item, person or file:
// /api/faces/quiz stays, /t/0a1b2c/s.jpg is /t, /api/face/0a1b/2 is /api/face.
func routeOf(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if parts[0] == "api" && len(parts) > 1 {
		keep := 2 // the words after /api/ up to the first id
		for keep < len(parts) && isWord(parts[keep]) && isWord(parts[keep-1]) {
			keep++
		}
		return "/" + strings.Join(parts[:keep], "/")
	}
	return "/" + parts[0]
}

// isWord: a route part, not an id (ids have digits or dashes).
func isWord(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
