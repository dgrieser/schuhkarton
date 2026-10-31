// Package worker is the face detection server ("schuhkarton worker"). It
// runs where Python with OpenCV and the face models are available, usually
// in the schuhkarton-faces container, and answers the gallery over HTTP:
//
//	GET  /v1/info   features: detection, recognition, missing modules
//	POST /v1/faces  body: image bytes (header X-Filename for the format);
//	                answer: faces with boxes, embeddings and 200 px crops
//	GET  /healthz
//
// With a token, requests need "Authorization: Bearer <token>".
package worker

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/gallery"
	"github.com/dgrieser/schuhkarton/internal/helper"
	"github.com/dgrieser/schuhkarton/internal/version"
)

// Options configure the worker.
type Options struct {
	Listen   string
	Token    string
	Python   string
	Models   []string
	Workers  int
	MinScore float64
	TempDir  string // helper script and request files
}

// Server answers face requests.
type Server struct {
	opt  Options
	pool *helper.Pool
}

// New prepares the worker; Python starts on the first request or probe.
func New(opt Options) *Server {
	if opt.TempDir == "" {
		opt.TempDir = filepath.Join(os.TempDir(), "schuhkarton-worker")
	}
	os.MkdirAll(opt.TempDir, 0o700)
	return &Server{opt: opt, pool: helper.New(helper.Options{
		Python: opt.Python, ScriptDir: opt.TempDir, Models: opt.Models, Size: opt.Workers, MinScore: opt.MinScore,
	})}
}

// Info returns what the helper can do.
func (s *Server) Info() gallery.WorkerInfo {
	i := s.pool.Probe()
	return gallery.WorkerInfo{Version: version.String(), Detection: i.Detection, Recognition: i.Recognition,
		Missing: i.Missing, Error: i.Error, Workers: s.opt.Workers}
}

// Close stops the helper processes.
func (s *Server) Close() { s.pool.Close() }

// Handler returns the HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /v1/info", s.auth(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.Info()) }))
	mux.HandleFunc("POST /v1/faces", s.auth(s.faces))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opt.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.opt.Token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

var safeExt = regexp.MustCompile(`^\.[a-zA-Z0-9]{1,5}$`)

func (s *Server) faces(w http.ResponseWriter, r *http.Request) {
	ext := strings.ToLower(filepath.Ext(r.Header.Get("X-Filename")))
	if !safeExt.MatchString(ext) {
		ext = ".jpg"
	}
	dir, err := os.MkdirTemp(s.opt.TempDir, "req-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "image"+ext)
	f, err := os.Create(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, io.LimitReader(r.Body, 256<<20))
	f.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prefix := filepath.Join(dir, "crop")
	found, err := s.pool.Faces(src, prefix)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	out := make([]gallery.WorkerFace, len(found))
	for i, fc := range found {
		out[i] = gallery.WorkerFace{X: fc.X, Y: fc.Y, W: fc.W, H: fc.H, Score: fc.Score, Emb: helper.EncodeEmbedding(fc.Emb)}
		if b, err := os.ReadFile(fmt.Sprintf("%s_%d.jpg", prefix, i)); err == nil {
			out[i].Crop = base64.StdEncoding.EncodeToString(b)
		}
	}
	writeJSON(w, map[string]any{"faces": out})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Run serves until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Addr: s.opt.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("Gesichtserkennung lauscht auf %s (%s)", s.opt.Listen, version.String())
	go func() {
		i := s.Info()
		switch {
		case i.Error != "":
			log.Printf("Achtung: %s", i.Error)
		case !i.Detection:
			log.Printf("Achtung: keine Gesichtserkennung möglich, es fehlt: %s", strings.Join(i.Missing, ", "))
		case !i.Recognition:
			log.Printf("Achtung: nur Erkennung, keine Zuordnung zu Personen, es fehlt: %s", strings.Join(i.Missing, ", "))
		default:
			log.Printf("bereit: Erkennung und Zuordnung (%d Prozesse)", s.opt.Workers)
		}
	}()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
		s.Close()
		return nil
	case err := <-errc:
		s.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
