// Package helper runs helper.py, a small Python program that finds faces
// with OpenCV (YuNet detection, SFace recognition) and decodes images with
// Pillow. The face container ("schuhkarton worker") uses it; the gallery
// itself only does in mode "local".
package helper

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
	"strconv"
	"sync"
	"time"
)

//go:embed helper.py
var script []byte

// Find returns a python interpreter or "".
func Find(hint string) string {
	cands := []string{hint, "python3", "python"}
	if home, err := os.UserHomeDir(); err == nil {
		cands = append(cands[:1], append([]string{filepath.Join(home, ".local/share/picture-organizer/venv/bin/python")}, cands[1:]...)...)
	}
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

// Options configure a Pool.
type Options struct {
	Python    string   // interpreter; "" disables the pool
	ScriptDir string   // where helper.py is written
	Models    []string // folders searched for the ONNX models
	Size      int      // processes
	MinScore  float64  // face detection score threshold
}

// Info is what a started helper reports.
type Info struct {
	Python      string            `json:"python"`
	Detection   bool              `json:"detection"`
	Recognition bool              `json:"recognition"`
	Heic        bool              `json:"heic"`
	Missing     []string          `json:"missing,omitempty"`
	Versions    map[string]string `json:"versions,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type proc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// Pool runs helper processes on demand.
type Pool struct {
	opt    Options
	script string

	mu      sync.Mutex
	idle    chan *proc
	started int

	infoMu sync.Mutex
	info   *Info
}

// New prepares a pool; processes start on first use.
func New(opt Options) *Pool {
	p := &Pool{opt: opt, idle: make(chan *proc, 256)}
	p.opt.Size = max(1, p.opt.Size)
	if opt.Python != "" {
		p.script = filepath.Join(opt.ScriptDir, "helper.py")
		if err := os.MkdirAll(opt.ScriptDir, 0o755); err == nil {
			if err := os.WriteFile(p.script, script, 0o644); err != nil {
				p.setInfo(&Info{Python: opt.Python, Error: err.Error()})
			}
		}
	} else {
		p.setInfo(&Info{Error: "kein Python gefunden"})
	}
	return p
}

func (p *Pool) setInfo(i *Info) {
	p.infoMu.Lock()
	p.info = i
	p.infoMu.Unlock()
}

// Probe starts one process (if none ran yet) and reports its features.
func (p *Pool) Probe() Info {
	p.infoMu.Lock()
	known := p.info
	p.infoMu.Unlock()
	if known != nil {
		return *known
	}
	h, err := p.get()
	if err != nil {
		i := Info{Python: p.opt.Python, Error: err.Error()}
		p.setInfo(&i)
		return i
	}
	p.put(h)
	p.infoMu.Lock()
	defer p.infoMu.Unlock()
	return *p.info
}

func (p *Pool) start() (*proc, error) {
	if p.opt.Python == "" || p.script == "" {
		return nil, errors.New("kein Python gefunden")
	}
	args := []string{"-u", p.script, "--min-score", strconv.FormatFloat(p.opt.MinScore, 'f', 3, 64)}
	args = append(args, p.opt.Models...)
	cmd := exec.Command(p.opt.Python, args...)
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
	h := &proc{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<16)}
	line, err := h.out.ReadBytes('\n')
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("helper did not start: %w", err)
	}
	var info Info
	if err := json.Unmarshal(line, &info); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("helper: %w", err)
	}
	info.Python = p.opt.Python
	p.setInfo(&info)
	return h, nil
}

func (p *Pool) get() (*proc, error) {
	select {
	case h := <-p.idle:
		return h, nil
	default:
	}
	p.mu.Lock()
	if p.started < p.opt.Size {
		p.started++
		p.mu.Unlock()
		h, err := p.start()
		if err != nil {
			p.mu.Lock()
			p.started--
			p.mu.Unlock()
			return nil, err
		}
		return h, nil
	}
	p.mu.Unlock()
	return <-p.idle, nil
}

func (p *Pool) put(h *proc) { p.idle <- h }

func (p *Pool) discard(h *proc) {
	h.cmd.Process.Kill()
	h.cmd.Wait()
	p.mu.Lock()
	p.started--
	p.mu.Unlock()
}

func (p *Pool) call(req any, res any) error {
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

// Close stops all idle processes.
func (p *Pool) Close() {
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

// ThumbSpec is one output of Thumb: Short (with MaxLong) or Long edge.
type ThumbSpec struct {
	Path    string `json:"path"`
	Short   int    `json:"short,omitempty"`
	MaxLong int    `json:"maxlong,omitempty"`
	Long    int    `json:"long,omitempty"`
	Q       int    `json:"q"`
}

// Thumb decodes src (any format Pillow reads) and writes the outputs.
// It returns the oriented size of the decoded image.
func (p *Pool) Thumb(src string, outs []ThumbSpec) (w, h int, err error) {
	var res struct{ W, H int }
	err = p.call(map[string]any{"op": "thumb", "src": src, "outs": outs}, &res)
	return res.W, res.H, err
}

// Face is one detected face; coordinates are relative to the oriented image.
type Face struct {
	X, Y, W, H float32
	Score      float32
	Emb        []float32 // normalized SFace embedding, nil without recognition
}

type rawFace struct {
	X, Y, W, H, Score float32
	Emb               string
}

// Faces finds faces in src and writes square crops to <cropPrefix>_<n>.jpg.
func (p *Pool) Faces(src, cropPrefix string) ([]Face, error) {
	var res struct{ Faces []rawFace }
	if err := p.call(map[string]any{"op": "faces", "src": src, "crops": cropPrefix}, &res); err != nil {
		return nil, err
	}
	out := make([]Face, 0, len(res.Faces))
	for _, f := range res.Faces {
		out = append(out, Face{X: f.X, Y: f.Y, W: f.W, H: f.H, Score: f.Score, Emb: DecodeEmbedding(f.Emb)})
	}
	return out, nil
}

// DecodeEmbedding turns base64 little-endian float32s into a vector.
func DecodeEmbedding(s string) []float32 {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) < 4 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// EncodeEmbedding is the inverse of DecodeEmbedding.
func EncodeEmbedding(v []float32) string {
	if v == nil {
		return ""
	}
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(b)
}
