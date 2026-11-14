// Package worker is the face detection server ("schuhkarton worker"). It
// runs where Python with OpenCV and the face models are available, usually
// in the schuhkarton-faces container, and answers the gallery over HTTP:
//
//	GET  /v1/info   features: detection, recognition, missing modules
//	POST /v1/faces  body: image bytes (header X-Filename for the format);
//	                answer: faces with boxes, embeddings and 200 px crops.
//	                With AuraFace in the models each face has a second
//	                embedding ("emb2"), computed alongside SFace. Header
//	                X-Extra: 1 adds the faces of a second, more generous
//	                search ("extra": true); X-Box: x,y,w,h (fractions) embeds
//	                a box drawn by hand that no search found.
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
	// AuraWorkers: processes of the second recognition system (AuraFace,
	// when its model is in Models); each holds the model, so they are few.
	// 0 or less: the model is not used.
	AuraWorkers int
}

// Server answers face requests.
type Server struct {
	opt  Options
	pool *helper.Pool
	aura *helper.Pool // nil: no second system
}

// New prepares the worker; Python starts on the first request or probe.
func New(opt Options) *Server {
	if opt.TempDir == "" {
		opt.TempDir = filepath.Join(os.TempDir(), "schuhkarton-worker")
	}
	os.MkdirAll(opt.TempDir, 0o700)
	s := &Server{opt: opt}
	flags := []string{"--no-aura"} // the main processes stay small (or it is not used)
	if opt.AuraWorkers > 0 && hasAura(opt.Models) {
		s.aura = helper.New(helper.Options{Python: opt.Python, ScriptDir: filepath.Join(opt.TempDir, "aura"), Models: opt.Models,
			Size: opt.AuraWorkers, MinScore: opt.MinScore, Flags: []string{"--only-aura"}})
	}
	s.pool = helper.New(helper.Options{Python: opt.Python, ScriptDir: opt.TempDir, Models: opt.Models, Size: opt.Workers,
		MinScore: opt.MinScore, Flags: flags})
	return s
}

// hasAura tells whether the second system's model is in one of the folders.
func hasAura(dirs []string) bool {
	for _, d := range dirs {
		for _, n := range []string{"auraface.onnx", "glintr100.onnx"} {
			if st, err := os.Stat(filepath.Join(d, n)); err == nil && !st.IsDir() {
				return true
			}
		}
	}
	return false
}

// auraOK tells whether the second system is there and loaded.
func (s *Server) auraOK() bool { return s.aura != nil && s.aura.Probe().Aura }

// Info returns what the helper can do.
func (s *Server) Info() gallery.WorkerInfo {
	i := s.pool.Probe()
	missing := i.Missing
	if s.aura != nil && !s.auraOK() {
		missing = append(append([]string(nil), missing...), s.aura.Probe().Missing...)
	}
	return gallery.WorkerInfo{Version: version.String(), Detection: i.Detection, Recognition: i.Recognition,
		Aura: i.Aura || (i.Recognition && s.auraOK()), Extra: i.Extra, Missing: missing, Error: i.Error, Workers: s.opt.Workers}
}

// Close stops the helper processes.
func (s *Server) Close() {
	s.pool.Close()
	if s.aura != nil {
		s.aura.Close()
	}
}

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
	var opts helper.FaceOpts
	opts.Extra = r.Header.Get("X-Extra") == "1"
	if b := r.Header.Get("X-Box"); b != "" {
		var box [4]float32
		if n, _ := fmt.Sscanf(b, "%g,%g,%g,%g", &box[0], &box[1], &box[2], &box[3]); n == 4 && box[2] > 0 && box[3] > 0 {
			opts.Box = &box
		}
	}
	two := s.auraOK() && s.pool.Probe().Recognition // both systems need the first one's alignment
	opts.Split = two
	found, err := s.pool.FacesWith(src, prefix, opts)
	if err == nil && two {
		err = s.embedBoth(found)
	}
	if errors.Is(err, errBusy) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	out := make([]gallery.WorkerFace, len(found))
	for i, fc := range found {
		out[i] = gallery.WorkerFace{X: fc.X, Y: fc.Y, W: fc.W, H: fc.H, Score: fc.Score, Emb: helper.EncodeEmbedding(fc.Emb),
			Emb2: helper.EncodeEmbedding(fc.Emb2), Extra: fc.Extra, Src: fc.Src}
		if b, err := os.ReadFile(fmt.Sprintf("%s_%d.jpg", prefix, i)); err == nil {
			out[i].Crop = base64.StdEncoding.EncodeToString(b)
		}
	}
	writeJSON(w, map[string]any{"faces": out})
}

// embedBoth has the found faces embedded by both systems at the same time:
// the first one in the main processes, the second in its own.
func (s *Server) embedBoth(found []helper.Face) error {
	var aligned []string
	for _, f := range found {
		aligned = append(aligned, f.Aligned)
	}
	if len(aligned) == 0 {
		return nil
	}
	var e2 [][]float32
	var err2 error
	done := make(chan struct{})
	go func() {
		defer close(done)
		e2, err2 = s.aura.Embed("aura", aligned)
	}()
	e1, err1 := s.pool.Embed("sface", aligned)
	<-done
	if err1 != nil {
		return err1
	}
	if err2 != nil {
		// asked again later (the gallery keeps what it had): faces without
		// the second embedding would not be asked for it again
		return fmt.Errorf("%w: %v", errBusy, err2)
	}
	for i := range found {
		found[i].Emb, found[i].Emb2 = e1[i], e2[i]
	}
	return nil
}

var errBusy = errors.New("zweites Erkennungsmodell nicht bereit")

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
		case i.Aura:
			log.Printf("bereit: Erkennung und Zuordnung mit SFace und AuraFace (%d Prozesse)", s.opt.Workers)
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
