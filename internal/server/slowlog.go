package server

import (
	"log"
	"net/http"
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
