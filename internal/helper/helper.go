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
	"strings"
	"sync"
	"sync/atomic"
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
	Flags     []string // more arguments for helper.py ("--no-aura", "--only-aura")
}

// Info is what a started helper reports.
type Info struct {
	Python      string            `json:"python"`
	Detection   bool              `json:"detection"`
	Recognition bool              `json:"recognition"`
	Aura        bool              `json:"aura"`  // a second embedding per face (AuraFace)
	Extra       bool              `json:"extra"` // the second, more generous search
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
	live    map[*proc]bool // running processes (for Stats)

	// counters for Stats
	starts, startErrs, crashes, timeouts, calls, callErrs atomic.Int64

	infoMu sync.Mutex
	info   *Info
	infoAt time.Time
}

// New prepares a pool; processes start on first use.
func New(opt Options) *Pool {
	p := &Pool{opt: opt, idle: make(chan *proc, 256), live: map[*proc]bool{}}
	p.opt.Size = max(1, p.opt.Size)
	if opt.Python != "" {
		// without the script written here nothing is run: never a file of
		// that name that was there before
		if path, err := writeScript(opt.ScriptDir); err != nil {
			p.setInfo(&Info{Python: opt.Python, Error: err.Error()})
		} else {
			p.script = path
		}
	} else {
		p.setInfo(&Info{Error: "kein Python gefunden"})
	}
	return p
}

// writeScript puts helper.py into dir, replacing what is there (also a
// link: the file is written beside it and renamed, never through it).
func writeScript(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("kein Ordner für helper.py")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".helper-*.py")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(script); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "helper.py")
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func (p *Pool) setInfo(i *Info) {
	p.infoMu.Lock()
	p.info, p.infoAt = i, time.Now()
	p.infoMu.Unlock()
}

// Probe starts one process (if none ran yet) and reports its features. A
// failed start is tried again after a minute (memory may be free again).
func (p *Pool) Probe() Info {
	p.infoMu.Lock()
	known, at := p.info, p.infoAt
	p.infoMu.Unlock()
	if known != nil && (known.Error == "" || p.opt.Python == "" || time.Since(at) < time.Minute) {
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
	if p.opt.Python == "" {
		return nil, errors.New("kein Python gefunden")
	}
	if p.script == "" {
		return nil, errors.New("helper.py ließ sich nicht schreiben")
	}
	args := []string{"-u", p.script, "--min-score", strconv.FormatFloat(p.opt.MinScore, 'f', 3, 64)}
	args = append(args, p.opt.Flags...)
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
	p.starts.Add(1)
	p.mu.Lock()
	p.live[h] = true
	p.mu.Unlock()
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
			p.startErrs.Add(1)
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
	delete(p.live, h)
	p.mu.Unlock()
}

// Stats is what a pool did, for debugging.
type Stats struct {
	Size      int     `json:"size"`
	Running   int     `json:"running"`
	Busy      int     `json:"busy"`
	Starts    int64   `json:"starts"`
	StartErrs int64   `json:"startErrors"`
	Crashes   int64   `json:"crashes"`  // a process died or broke the protocol
	Timeouts  int64   `json:"timeouts"` // a request took over 2 minutes: the process was killed
	Calls     int64   `json:"calls"`
	CallErrs  int64   `json:"callErrors"` // answered with an error (the process goes on)
	RSSMB     []int64 `json:"rssMB"`      // resident memory of each running process
}

// Stats reports the pool's processes and counters.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	st := Stats{Size: p.opt.Size, Running: len(p.live), Busy: max(0, len(p.live)-len(p.idle))}
	for h := range p.live {
		st.RSSMB = append(st.RSSMB, rssMB(h.cmd.Process.Pid))
	}
	p.mu.Unlock()
	st.Starts, st.StartErrs, st.Crashes, st.Timeouts = p.starts.Load(), p.startErrs.Load(), p.crashes.Load(), p.timeouts.Load()
	st.Calls, st.CallErrs = p.calls.Load(), p.callErrs.Load()
	return st
}

// rssMB reads the resident memory of a process (Linux; -1 elsewhere).
func rssMB(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb >> 10
		}
	}
	return -1
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
	p.calls.Add(1)
	select {
	case r := <-ch:
		if r.err != nil {
			p.crashes.Add(1)
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
			p.callErrs.Add(1)
			return errors.New(status.Error)
		}
		return json.Unmarshal(r.line, res)
	case <-time.After(2 * time.Minute):
		p.timeouts.Add(1)
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
	Emb2       []float32 // normalized AuraFace embedding, nil without that model
	// Extra: found only by the second search (or the box asked for), Src
	// tells by what: "yunet+retina", "retina", "yunet-low", "box"
	Extra bool
	Src   string
	// Aligned: with FaceOpts.Split, the 112x112 aligned face (raw BGR,
	// base64) to be embedded by Embed instead
	Aligned string
}

// FaceOpts asks for more than the main search.
type FaceOpts struct {
	Extra bool        // the second, more generous search
	Box   *[4]float32 // a face drawn by hand (x, y, w, h as fractions): embedded itself when no search finds it
	Split bool        // no embeddings: the aligned faces instead (Face.Aligned), for Embed
}

type rawFace struct {
	X, Y, W, H, Score float32
	Emb, Emb2         string
	Extra             bool
	Src               string
	Aligned           string
}

// Faces finds faces in src and writes square crops to <cropPrefix>_<n>.jpg.
func (p *Pool) Faces(src, cropPrefix string) ([]Face, error) {
	return p.FacesWith(src, cropPrefix, FaceOpts{})
}

// FacesWith is Faces with the second search or a box asked for.
func (p *Pool) FacesWith(src, cropPrefix string, o FaceOpts) ([]Face, error) {
	req := map[string]any{"op": "faces", "src": src, "crops": cropPrefix}
	if o.Extra {
		req["extra"] = true
	}
	if o.Box != nil {
		req["box"] = o.Box[:]
	}
	if o.Split {
		req["split"] = true
	}
	var res struct{ Faces []rawFace }
	if err := p.call(req, &res); err != nil {
		return nil, err
	}
	out := make([]Face, 0, len(res.Faces))
	for _, f := range res.Faces {
		out = append(out, Face{X: f.X, Y: f.Y, W: f.W, H: f.H, Score: f.Score, Emb: DecodeEmbedding(f.Emb), Emb2: DecodeEmbedding(f.Emb2),
			Extra: f.Extra, Src: f.Src, Aligned: f.Aligned})
	}
	return out, nil
}

// Embed embeds aligned faces (Face.Aligned) with one system: "sface" or
// "aura" (a pool started with --only-aura).
func (p *Pool) Embed(model string, aligned []string) ([][]float32, error) {
	if len(aligned) == 0 {
		return nil, nil
	}
	var res struct{ Embs []string }
	if err := p.call(map[string]any{"op": "embed", "model": model, "crops": aligned}, &res); err != nil {
		return nil, err
	}
	if len(res.Embs) != len(aligned) {
		return nil, errors.New("helper: embeddings missing")
	}
	out := make([][]float32, len(res.Embs))
	for i, e := range res.Embs {
		out[i] = DecodeEmbedding(e)
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
