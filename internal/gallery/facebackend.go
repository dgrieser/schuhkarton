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
	Error       string   `json:"error,omitempty"`
	Missing     []string `json:"missing,omitempty"`
	Version     string   `json:"version,omitempty"`
}

// faceBackend finds faces in one item and stores their crops.
type faceBackend interface {
	probe() FaceInfo
	// detect stops when ctx ends (the local helper finishes the photo it has).
	detect(ctx context.Context, l *Library, it *Item, cropPrefix string) ([]Face, error)
	// detectData finds faces in a JPEG (no crops kept).
	detectData(ctx context.Context, l *Library, data []byte) ([]Face, error)
}

// errTransient marks failures worth retrying later (container down).
var errTransient = errors.New("face service unreachable")

func isTransient(err error) bool { return errors.Is(err, errTransient) }

// --- local: Python helper on this machine ------------------------------------

type localFaces struct{ pool *helper.Pool }

func (f *localFaces) probe() FaceInfo {
	i := f.pool.Probe()
	return FaceInfo{Mode: "local", Detection: i.Detection, Recognition: i.Recognition, Error: i.Error, Missing: i.Missing}
}

func (f *localFaces) detect(ctx context.Context, l *Library, it *Item, prefix string) ([]Face, error) {
	hf, err := f.pool.Faces(l.Path(it), prefix)
	if err != nil {
		return nil, err
	}
	return fromHelper(hf), nil
}

func (f *localFaces) detectData(ctx context.Context, l *Library, data []byte) ([]Face, error) {
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
	hf, err := f.pool.Faces(tmp.Name(), prefix)
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
		out[i] = Face{X: f.X, Y: f.Y, W: f.W, H: f.H, Score: f.Score, Emb: f.Emb}
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
	info := FaceInfo{Mode: "remote", Detection: wi.Detection, Recognition: wi.Recognition, Missing: wi.Missing, Version: wi.Version, Error: wi.Error}
	if err != nil {
		info.Error = fmt.Sprintf("Gesichtserkennung unter %s nicht erreichbar: %v", f.url, err)
	}
	return info
}

// WorkerFace is one face in the answer of POST /v1/faces.
type WorkerFace struct {
	X, Y, W, H float32
	Score      float32 `json:"score"`
	Emb        string  `json:"emb,omitempty"`  // base64 little-endian float32
	Crop       string  `json:"crop,omitempty"` // base64 JPEG, 200x200
}

// detect sends a 1600 px JPEG (or the original, when Go cannot decode it)
// and stores the returned crops next to the other face crops.
func (f *remoteFaces) detect(ctx context.Context, l *Library, it *Item, prefix string) ([]Face, error) {
	body, name, err := l.faceImage(ctx, it)
	if err != nil {
		return nil, err
	}
	found, err := f.post(ctx, body, name)
	if err != nil {
		return nil, err
	}
	out := make([]Face, 0, len(found))
	for i, wf := range found {
		if b, err := base64.StdEncoding.DecodeString(wf.Crop); err == nil && len(b) > 0 {
			os.WriteFile(fmt.Sprintf("%s_%d.jpg", prefix, i), b, 0o644)
		}
		out = append(out, Face{X: wf.X, Y: wf.Y, W: wf.W, H: wf.H, Score: wf.Score, Emb: helper.DecodeEmbedding(wf.Emb)})
	}
	return out, nil
}

func (f *remoteFaces) detectData(ctx context.Context, l *Library, data []byte) ([]Face, error) {
	found, err := f.post(ctx, data, "face.jpg")
	if err != nil {
		return nil, err
	}
	out := make([]Face, 0, len(found))
	for _, wf := range found {
		out = append(out, Face{X: wf.X, Y: wf.Y, W: wf.W, H: wf.H, Score: wf.Score, Emb: helper.DecodeEmbedding(wf.Emb)})
	}
	return out, nil
}

// post sends a picture to the face container and returns what it found.
func (f *remoteFaces) post(ctx context.Context, body []byte, name string) ([]WorkerFace, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/v1/faces", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Filename", name)
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

// faceImage returns what is sent to the face container: an oriented JPEG
// of at most 1600 px, made here when Go can decode the file.
func (l *Library) faceImage(ctx context.Context, it *Item) ([]byte, string, error) {
	if err := l.unreadable(it); err != nil { // the share hangs on it
		return nil, "", err
	}
	src := l.Path(it)
	if !rawExt[it.Ext()] {
		if b, err := l.originalJPEG(ctx, it, 1600, 90, true); err == nil {
			return b, "face.jpg", nil
		}
	}
	// large preview from the cache (RAW, odd formats)
	if p, err := l.ensureBG(ctx, it, VarLarge); err == nil {
		b, err := os.ReadFile(p)
		return b, "face.jpg", err
	}
	b, err := os.ReadFile(src)
	return b, filepath.Base(src), err
}

var _ image.Image = (*image.RGBA)(nil)
