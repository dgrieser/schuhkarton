package gallery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// The expensive picture work (decoding a photo, HEIC and RAW previews
// included, scaling and writing JPEGs) runs in a child process,
// "schuhkarton convert": its memory goes back to the system when it ends,
// a photo that needs too much memory takes only the child down, and
// background work runs with a lower CPU priority than what people look at.
// ConvertCommand is that command; empty (tests, other programs) converts
// in this process.
var ConvertCommand []string

// ConvertRequest is one photo for the child.
type ConvertRequest struct {
	Src      string          `json:"src"`
	ExifTool string          `json:"exiftool,omitempty"` // RAW: the embedded JPEG through exiftool
	Raw      bool            `json:"raw,omitempty"`
	Orient   int             `json:"orient,omitempty"` // EXIF orientation applied to the outputs
	Slow     bool            `json:"slow,omitempty"`   // the slow HEIC/AVIF path is allowed
	Nice     int             `json:"nice,omitempty"`   // lower CPU priority (background work)
	Outputs  []ConvertOutput `json:"outputs"`
}

// ConvertOutput is one JPEG made from the photo. Long fits it into Long x
// Long; Short scales the short edge (the long one to at most MaxLong); none
// keeps the size. FromPrev scales the previous output instead of the photo
// (the thumbnail from the lightbox preview: cheaper, the same picture).
type ConvertOutput struct {
	Path     string `json:"path"`
	Long     int    `json:"long,omitempty"`
	Short    int    `json:"short,omitempty"`
	MaxLong  int    `json:"maxLong,omitempty"`
	Quality  int    `json:"q"`
	FromPrev bool   `json:"fromPrev,omitempty"`
}

// ConvertResult is the size of the decoded photo (before turning).
type ConvertResult struct {
	W        int    `json:"w"`
	H        int    `json:"h"`
	Err      string `json:"err,omitempty"`
	NeedSlow bool   `json:"needSlow,omitempty"` // only the slow path could decode it
}

var errNeedSlow = errors.New("needs the slow decoder")

// RunConvert is the child: one request on in, the result on out.
func RunConvert(in io.Reader, out io.Writer) error {
	var req ConvertRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return err
	}
	if req.Nice > 0 {
		setNice(req.Nice)
	}
	res := convertNow(req)
	return json.NewEncoder(out).Encode(res)
}

// convertNow does the work of a request in this process.
func convertNow(req ConvertRequest) ConvertResult {
	var img image.Image
	var err error
	if req.Raw {
		var b []byte
		if b, err = extractPreview(req.ExifTool, req.Src); err == nil {
			img, _, err = image.Decode(bytes.NewReader(b))
		}
	} else {
		img, err = decodeForConvert(req.Src, req.Slow)
	}
	if errors.Is(err, errNeedSlow) {
		return ConvertResult{NeedSlow: true, Err: err.Error()}
	}
	if err != nil {
		return ConvertResult{Err: err.Error()}
	}
	b := img.Bounds()
	res := ConvertResult{W: b.Dx(), H: b.Dy()}
	prev := img
	for _, o := range req.Outputs {
		src := img
		if o.FromPrev {
			src = prev
		}
		var scaled image.Image
		switch {
		case o.Long > 0:
			scaled = resizeFit(src, o.Long, o.Long)
		case o.Short > 0:
			scaled = resizeShort(src, o.Short, max(o.MaxLong, o.Short))
		default:
			scaled = src
		}
		if err := writeJPEG(o.Path, orient(scaled, req.Orient), o.Quality); err != nil {
			return ConvertResult{Err: err.Error()}
		}
		prev = scaled
	}
	return res
}

// decodeForConvert is decodeImage for the child: no slots (the parent
// holds them); the slow path only when allowed.
func decodeForConvert(p string, slow bool) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !wasmCompiled && isHEIF(f) {
		if heifTool() != "" {
			if img, err := decodeHEIFTool(p); err == nil {
				return img, nil
			}
		}
		if !slow {
			return nil, errNeedSlow
		}
	}
	img, _, err := image.Decode(f)
	if err != nil && wasmCompiled && FindHEIFTool() != "" && rewind(f) && isHEIF(f) {
		if img2, err2 := decodeHEIFToolWith(FindHEIFTool(), p); err2 == nil {
			return img2, nil
		}
	}
	return img, err
}

// convert runs a request in a child process (or here without one). The
// caller holds no decode slot: convert takes one, and on the slow path
// the slow slot instead.
func (l *Library) convert(ctx context.Context, req ConvertRequest, background bool) (ConvertResult, error) {
	if background {
		req.Nice = 10
	}
	if req.ExifTool == "" {
		req.ExifTool = l.opt.ExifTool
	}
	run := func(r ConvertRequest) (ConvertResult, error) {
		release, err := decodeSlot(ctx, background)
		if err != nil {
			return ConvertResult{}, err
		}
		defer release()
		return l.runConvert(ctx, r)
	}
	res, err := run(req)
	if err == nil && res.NeedSlow {
		defer slowSlot()()
		req.Slow = true
		res, err = l.runConvert(ctx, req)
	}
	if err == nil && res.Err != "" {
		err = errors.New(res.Err)
	}
	return res, err
}

var convertMu sync.Mutex // ConvertCommand is set once at start; tests change it

func (l *Library) runConvert(ctx context.Context, req ConvertRequest) (ConvertResult, error) {
	convertMu.Lock()
	cmdline := ConvertCommand
	convertMu.Unlock()
	if len(cmdline) == 0 {
		return convertNow(req), nil
	}
	body, err := json.Marshal(req)
	if err != nil {
		return ConvertResult{}, err
	}
	cmd := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	cmd.Stdin = bytes.NewReader(body)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ConvertResult{}, ctx.Err()
		}
		// killed (out of memory) or crashed: tell what it said last
		return ConvertResult{}, fmt.Errorf("convert: %v %s", err, lastLine(stderr.String()))
	}
	var res ConvertResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		return ConvertResult{}, fmt.Errorf("convert: %v", err)
	}
	return res, nil
}

// originalJPEG returns a photo as a JPEG at most n pixels on the long edge
// (0: full size), turned like in the viewer, made by the child.
func (l *Library) originalJPEG(ctx context.Context, it *Item, n, quality int, background bool) ([]byte, error) {
	if err := l.unreadable(it); err != nil { // the share hangs on it
		return nil, err
	}
	tmp, err := os.CreateTemp(l.opt.CacheDir, "conv-*.jpg")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	out := ConvertOutput{Path: tmp.Name(), Long: n, Quality: quality}
	_, err = l.convert(ctx, ConvertRequest{Src: l.Path(it), Raw: rawExt[it.Ext()], Orient: l.displayOrient(it), Outputs: []ConvertOutput{out}}, background)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(tmp.Name())
}

// ConvertSelfCommand is the command line of this program's convert
// subcommand ("" when the program cannot be found).
func ConvertSelfCommand() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if strings.HasSuffix(exe, ".test") { // a test binary has no convert command
		return nil
	}
	return []string{exe, "convert"}
}
