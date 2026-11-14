package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dgrieser/schuhkarton/internal/helper"
)

// FaceInfo describes the face backend.
type FaceInfo struct {
	Mode        string   `json:"mode"`
	Detection   bool     `json:"detection"`
	Recognition bool     `json:"recognition"`
	Aura        bool     `json:"aura,omitempty"`  // the second system (AuraFace) gives a second embedding
	Extra       bool     `json:"extra,omitempty"` // the second, more generous search
	Error       string   `json:"error,omitempty"`
	Missing     []string `json:"missing,omitempty"`
	Version     string   `json:"version,omitempty"`
}

// faceBackend finds faces in one item and stores their crops.
type faceBackend interface {
	probe() FaceInfo
	// detect stops when ctx ends (the local helper finishes the photo it
	// has). extra: the faces of the second search too (Src set).
	detect(ctx context.Context, l *Library, it *Item, cropPrefix string, extra bool) ([]Face, error)
	// detectData finds faces in a JPEG (no crops kept); box: a face drawn
	// by hand there, embedded as it is when no search finds it.
	detectData(ctx context.Context, l *Library, data []byte, box *[4]float32) ([]Face, error)
}

// errTransient marks failures worth retrying later (container down).
var errTransient = errors.New("face service unreachable")

func isTransient(err error) bool { return errors.Is(err, errTransient) }

// --- local: Python helper on this machine ------------------------------------

type localFaces struct{ pool *helper.Pool }

func (f *localFaces) probe() FaceInfo {
	i := f.pool.Probe()
	return FaceInfo{Mode: "local", Detection: i.Detection, Recognition: i.Recognition, Aura: i.Aura, Extra: i.Extra, Error: i.Error, Missing: i.Missing}
}

func (f *localFaces) detect(ctx context.Context, l *Library, it *Item, prefix string, extra bool) ([]Face, error) {
	hf, err := f.pool.FacesWith(l.Path(it), prefix, helper.FaceOpts{Extra: extra})
	if err != nil {
		return nil, err
	}
	return fromHelper(hf), nil
}

func (f *localFaces) detectData(ctx context.Context, l *Library, data []byte, box *[4]float32) ([]Face, error) {
	tmp, err := os.CreateTemp(l.opt.CacheDir, "face-*.jpg")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	prefix := strings.TrimSuffix(tmp.Name(), ".jpg") + "-crop"
	hf, err := f.pool.FacesWith(tmp.Name(), prefix, helper.FaceOpts{Box: box})
	if matches, _ := filepath.Glob(prefix + "_*.jpg"); len(matches) > 0 {
		for _, m := range matches {
			os.Remove(m)
		}
	}
	if err != nil {
		return nil, err
	}
	return fromHelper(hf), nil
}

func fromHelper(hf []helper.Face) []Face {
	out := make([]Face, len(hf))
	for i, f := range hf {
		out[i] = Face{X: f.X, Y: f.Y, W: f.W, H: f.H, Score: f.Score, Emb: f.Emb, Emb2: quant2(f.Emb2), Src: f.Src}
	}
	return out
}

// --- remote: the face container ("schuhkarton worker") -------------------------

type remoteFaces struct {
	url, token string
	client     *http.Client

	mu         sync.Mutex
	info       FaceInfo
	infoAt     time.Time
	known      bool // info was asked for once
	refreshing bool
	onUp       func() // the background check found detection or recognition back
}

func newRemoteFaces(url, token string) *remoteFaces {
	return &remoteFaces{url: strings.TrimRight(url, "/"), token: token, client: &http.Client{Timeout: 3 * time.Minute}}
}

// WorkerInfo is the answer of GET /v1/info on the face container.
type WorkerInfo struct {
	Version     string   `json:"version"`
	Detection   bool     `json:"detection"`
	Recognition bool     `json:"recognition"`
	Aura        bool     `json:"aura,omitempty"`
	Extra       bool     `json:"extra,omitempty"`
	Missing     []string `json:"missing,omitempty"`
	Error       string   `json:"error,omitempty"`
	Workers     int      `json:"workers"`
}

// ProbeWorker asks a face container for its features.
func ProbeWorker(ctx context.Context, url, token string) (WorkerInfo, error) {
	var wi WorkerInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/v1/info", nil)
	if err != nil {
		return wi, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return wi, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return wi, errors.New("Token falsch")
	}
	if resp.StatusCode != http.StatusOK {
		return wi, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return wi, json.NewDecoder(resp.Body).Decode(&wi)
}

// probe tells what the face service can do. The first time it waits for
// the answer; after that it answers with what is known and asks again in
// the background once a minute, so a slow service does not hold up the
// status (and the pages asking for it).
func (f *remoteFaces) probe() FaceInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.known {
		f.info, f.infoAt, f.known = f.ask(), time.Now(), true
		return f.info
	}
	if time.Since(f.infoAt) >= time.Minute && !f.refreshing {
		f.refreshing = true
		go func() {
			info := f.ask()
			f.mu.Lock()
			old := f.info
			f.info, f.infoAt, f.refreshing = info, time.Now(), false
			up := f.onUp
			f.mu.Unlock()
			// work that was skipped while the service was away can start now
			if up != nil && (info.Detection && !old.Detection || info.Recognition && !old.Recognition) {
				up()
			}
		}()
	}
	return f.info
}

// ask asks the face service for its features (up to 10 s).
func (f *remoteFaces) ask() FaceInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wi, err := ProbeWorker(ctx, f.url, f.token)
	info := FaceInfo{Mode: "remote", Detection: wi.Detection, Recognition: wi.Recognition, Aura: wi.Aura, Extra: wi.Extra,
		Missing: wi.Missing, Version: wi.Version, Error: wi.Error}
	if err != nil {
		info.Error = fmt.Sprintf("Gesichtserkennung unter %s nicht erreichbar: %v", f.url, err)
	}
	return info
}

// WorkerFace is one face in the answer of POST /v1/faces.
type WorkerFace struct {
	X, Y, W, H float32
	Score      float32 `json:"score"`
	Emb        string  `json:"emb,omitempty"`   // base64 little-endian float32
	Emb2       string  `json:"emb2,omitempty"`  // the second system's, the same way
	Extra      bool    `json:"extra,omitempty"` // found by the second search (or the box asked for)
	Src        string  `json:"src,omitempty"`
	Crop       string  `json:"crop,omitempty"` // base64 JPEG, 200x200
}

func (wf WorkerFace) face() Face {
	return Face{X: wf.X, Y: wf.Y, W: wf.W, H: wf.H, Score: wf.Score, Emb: helper.DecodeEmbedding(wf.Emb),
		Emb2: quant2(helper.DecodeEmbedding(wf.Emb2)), Src: wf.Src}
}

// detect sends the picture (see faceUpload) and stores the returned crops
// next to the other face crops.
func (f *remoteFaces) detect(ctx context.Context, l *Library, it *Item, prefix string, extra bool) ([]Face, error) {
	body, name, err := l.faceUpload(ctx, it)
	if err != nil {
		return nil, err
	}
	hdr := map[string]string{}
	if extra {
		hdr["X-Extra"] = "1"
	}
	found, err := f.post(ctx, body, name, hdr)
	if err != nil {
		return nil, err
	}
	out := make([]Face, 0, len(found))
	for i, wf := range found {
		if b, err := base64.StdEncoding.DecodeString(wf.Crop); err == nil && len(b) > 0 {
			os.WriteFile(fmt.Sprintf("%s_%d.jpg", prefix, i), b, 0o644)
		}
		out = append(out, wf.face())
	}
	return out, nil
}

func (f *remoteFaces) detectData(ctx context.Context, l *Library, data []byte, box *[4]float32) ([]Face, error) {
	hdr := map[string]string{}
	if box != nil {
		hdr["X-Box"] = fmt.Sprintf("%g,%g,%g,%g", box[0], box[1], box[2], box[3])
	}
	found, err := f.post(ctx, data, "face.jpg", hdr)
	if err != nil {
		return nil, err
	}
	out := make([]Face, 0, len(found))
	for _, wf := range found {
		out = append(out, wf.face())
	}
	return out, nil
}

// post sends a picture to the face container and returns what it found.
func (f *remoteFaces) post(ctx context.Context, body []byte, name string, hdr map[string]string) ([]WorkerFace, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/v1/faces", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Filename", name)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "connection refused") {
			f.mu.Lock()
			f.infoAt = time.Time{}
			f.mu.Unlock()
			return nil, fmt.Errorf("%w: %v", errTransient, err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("%w: HTTP %d", errTransient, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var res struct {
		Faces []WorkerFace `json:"faces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Faces, nil
}

// maxUpload: originals up to this size go to the face container as they are.
const maxUpload = 25 << 20

// uploadAsIs: formats the face container decodes and turns itself the way
// the gallery shows them (EXIF orientation; HEIF's own rotation).
var uploadAsIs = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".heic": true, ".heif": true}

// faceUpload is what goes to the face container, the cheapest way for a
// small machine: the large preview when it is made already (no read from
// the share, no decoding), else the original as it is when the container
// decodes it the same way (it decodes much faster, the machine here only
// reads and sends the file), else a JPEG made here (faceImage).
func (l *Library) faceUpload(ctx context.Context, it *Item) ([]byte, string, error) {
	// shown as the gallery shows it, turns included; not when previews are
	// smaller than the 1600 px faces are looked for in
	if l.opt.PreviewSize >= 1600 {
		if p, _ := l.cached(it, VarLarge, true); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				return b, "face.jpg", nil
			}
		}
	}
	if it.Kind == Photo && it.Rot == 0 && uploadAsIs[it.Ext()] && it.Size > 0 && it.Size <= maxUpload {
		if err := l.unreadable(it); err != nil {
			return nil, "", err
		}
		b, err := os.ReadFile(l.Path(it))
		if err == nil {
			return b, "face" + it.Ext(), nil
		}
	}
	return l.faceImage(ctx, it)
}

// faceImage returns an oriented JPEG of at most 1600 px of a photo, made
// here when Go can decode the file (what faces are looked for in, and what
// the face container gets when faceUpload has nothing cheaper).
func (l *Library) faceImage(ctx context.Context, it *Item) ([]byte, string, error) {
	if err := l.unreadable(it); err != nil { // the share hangs on it
		return nil, "", err
	}
	src := l.Path(it)
	if !rawExt[it.Ext()] {
		// not on the slow path (a HEIC the native tool cannot read takes
		// minutes on 32-bit ARM, each time): the large preview below then,
		// made once and shared with the viewer
		if b, err := l.originalJPEGWith(ctx, it, 1600, 90, true, false); err == nil {
			return b, "face.jpg", nil
		}
	}
	// large preview from the cache (RAW, odd formats, the slow HEICs)
	if p, err := l.ensureBG(ctx, it, VarLarge); err == nil {
		b, err := os.ReadFile(p)
		return b, "face.jpg", err
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	b, err := os.ReadFile(src)
	return b, filepath.Base(src), err
}

var _ image.Image = (*image.RGBA)(nil)
