package gallery

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed helper.py
var helperScript []byte

// FindPython returns a python interpreter or "".
func FindPython(hint string) string {
	cands := []string{hint, filepath.Join(os.Getenv("HOME"), ".local/share/picture-organizer/venv/bin/python"), "python3"}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

type helperProc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// helperPool runs a few python helper processes on demand.
type helperPool struct {
	python, script string
	models         []string
	size           int

	mu       sync.Mutex
	idle     chan *helperProc
	started  int
	failed   atomic.Bool
	hasFaces atomic.Bool
	hasRecog atomic.Bool
	probed   atomic.Bool
	probeMux sync.Mutex
}

func newHelperPool(opt Options) *helperPool {
	p := &helperPool{python: opt.Python, size: max(1, opt.Workers), idle: make(chan *helperProc, 64)}
	if p.python == "" {
		p.failed.Store(true)
		return p
	}
	p.script = filepath.Join(opt.CacheDir, "helper.py")
	if err := os.WriteFile(p.script, helperScript, 0o644); err != nil {
		p.failed.Store(true)
	}
	p.models = []string{opt.ModelsDir, filepath.Join(opt.CacheDir, "models"),
		filepath.Join(os.Getenv("HOME"), ".cache/picture-organizer")}
	return p
}

func (p *helperPool) available() bool { return !p.failed.Load() }

func (p *helperPool) probe() {
	p.probeMux.Lock()
	defer p.probeMux.Unlock()
	if p.probed.Load() || p.failed.Load() {
		return
	}
	if h, err := p.get(); err == nil {
		p.put(h)
	}
	p.probed.Store(true)
}

func (p *helperPool) facesAvailable() bool       { p.probe(); return p.hasFaces.Load() }
func (p *helperPool) recognitionAvailable() bool { p.probe(); return p.hasRecog.Load() }

func (p *helperPool) start() (*helperProc, error) {
	args := append([]string{"-u", p.script}, p.models...)
	cmd := exec.Command(p.python, args...)
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=1", "OPENBLAS_NUM_THREADS=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	h := &helperProc{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<16)}
	line, err := h.out.ReadBytes('\n')
	if err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("helper did not start: %w", err)
	}
	var hello struct {
		Faces       bool `json:"faces"`
		Recognition bool `json:"recognition"`
	}
	json.Unmarshal(line, &hello)
	p.hasFaces.Store(hello.Faces)
	p.hasRecog.Store(hello.Recognition)
	return h, nil
}

func (p *helperPool) get() (*helperProc, error) {
	if p.failed.Load() {
		return nil, errors.New("helper unavailable")
	}
	select {
	case h := <-p.idle:
		return h, nil
	default:
	}
	p.mu.Lock()
	if p.started < p.size {
		p.started++
		p.mu.Unlock()
		h, err := p.start()
		if err != nil {
			p.mu.Lock()
			p.started--
			p.mu.Unlock()
			p.failed.Store(true)
			return nil, err
		}
		return h, nil
	}
	p.mu.Unlock()
	return <-p.idle, nil
}

func (p *helperPool) put(h *helperProc) { p.idle <- h }

func (p *helperPool) discard(h *helperProc) {
	h.cmd.Process.Kill()
	h.cmd.Wait()
	p.mu.Lock()
	p.started--
	p.mu.Unlock()
}

// call sends one request and decodes the answer into res.
func (p *helperPool) call(req any, res any) error {
	h, err := p.get()
	if err != nil {
		return err
	}
	b, _ := json.Marshal(req)
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		if _, err := h.in.Write(append(b, '\n')); err != nil {
			ch <- result{nil, err}
			return
		}
		line, err := h.out.ReadBytes('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			p.discard(h)
			return r.err
		}
		p.put(h)
		var status struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		json.Unmarshal(r.line, &status)
		if !status.OK {
			return errors.New(status.Error)
		}
		return json.Unmarshal(r.line, res)
	case <-time.After(2 * time.Minute):
		p.discard(h)
		return errors.New("helper timeout")
	}
}

func (p *helperPool) close() {
	for {
		select {
		case h := <-p.idle:
			h.in.Close()
			h.cmd.Process.Kill()
			h.cmd.Wait()
		default:
			return
		}
	}
}

type thumbSpec struct {
	Path    string `json:"path"`
	Short   int    `json:"short,omitempty"`
	MaxLong int    `json:"maxlong,omitempty"`
	Long    int    `json:"long,omitempty"`
	Q       int    `json:"q"`
}

func (p *helperPool) thumb(src string, outs []thumbSpec) (w, h int, err error) {
	var res struct{ W, H int }
	err = p.call(map[string]any{"op": "thumb", "src": src, "outs": outs}, &res)
	return res.W, res.H, err
}

type helperFace struct {
	X, Y, W, H, Score float32
	Emb               string
}

func (p *helperPool) faces(src, cropPrefix string) ([]Face, error) {
	var res struct{ Faces []helperFace }
	if err := p.call(map[string]any{"op": "faces", "src": src, "crops": cropPrefix}, &res); err != nil {
		return nil, err
	}
	out := make([]Face, 0, len(res.Faces))
	for _, f := range res.Faces {
		face := Face{X: f.X, Y: f.Y, W: f.W, H: f.H, Score: f.Score}
		if b, err := base64.StdEncoding.DecodeString(f.Emb); err == nil && len(b) >= 4 {
			face.Emb = make([]float32, len(b)/4)
			for i := range face.Emb {
				face.Emb[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
			}
		}
		out = append(out, face)
	}
	return out, nil
}
