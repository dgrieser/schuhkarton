package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
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
	detect(l *Library, it *Item, cropPrefix string) ([]Face, error)
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

func (f *localFaces) detect(l *Library, it *Item, prefix string) ([]Face, error) {
	hf, err := f.pool.Faces(l.Path(it), prefix)
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

	mu     sync.Mutex
	info   FaceInfo
	infoAt time.Time
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

func (f *remoteFaces) probe() FaceInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Since(f.infoAt) < time.Minute {
		return f.info
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wi, err := ProbeWorker(ctx, f.url, f.token)
	f.info = FaceInfo{Mode: "remote", Detection: wi.Detection, Recognition: wi.Recognition, Missing: wi.Missing, Version: wi.Version, Error: wi.Error}
	if err != nil {
		f.info.Error = fmt.Sprintf("Gesichtserkennung unter %s nicht erreichbar: %v", f.url, err)
	}
	f.infoAt = time.Now()
	return f.info
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
func (f *remoteFaces) detect(l *Library, it *Item, prefix string) ([]Face, error) {
	body, name, err := l.faceImage(it)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, f.url+"/v1/faces", bytes.NewReader(body))
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
	out := make([]Face, 0, len(res.Faces))
	for i, wf := range res.Faces {
		if b, err := base64.StdEncoding.DecodeString(wf.Crop); err == nil && len(b) > 0 {
			os.WriteFile(fmt.Sprintf("%s_%d.jpg", prefix, i), b, 0o644)
		}
		out = append(out, Face{X: wf.X, Y: wf.Y, W: wf.W, H: wf.H, Score: wf.Score, Emb: helper.DecodeEmbedding(wf.Emb)})
	}
	return out, nil
}

// faceImage returns what is sent to the face container: an oriented JPEG
// of at most 1600 px, made here when Go can decode the file.
func (l *Library) faceImage(it *Item) ([]byte, string, error) {
	src := l.Path(it)
	if !rawExt[strings.ToLower(filepath.Ext(src))] {
		if img, err := decodeFile(src); err == nil {
			img = orient(resizeFit(img, 1600, 1600), l.displayOrient(it, src))
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err == nil {
				return buf.Bytes(), "face.jpg", nil
			}
		}
	}
	// large preview from the cache (RAW, odd formats)
	if p, err := l.Ensure(context.Background(), it, VarLarge); err == nil {
		b, err := os.ReadFile(p)
		return b, "face.jpg", err
	}
	b, err := os.ReadFile(src)
	return b, filepath.Base(src), err
}

var _ image.Image = (*image.RGBA)(nil)
