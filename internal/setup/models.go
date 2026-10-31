package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Model is one face model from the OpenCV model zoo.
type Model struct {
	File   string
	URL    string
	SHA256 string
	For    string
}

// Models are the face models the helper loads.
var Models = []Model{
	{"face_detection_yunet_2023mar.onnx",
		"https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx",
		"8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4", "Gesichter finden (YuNet)"},
	{"face_recognition_sface_2021dec.onnx",
		"https://github.com/opencv/opencv_zoo/raw/main/models/face_recognition_sface/face_recognition_sface_2021dec.onnx",
		"0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79", "Personen zuordnen (SFace)"},
}

// DownloadModels fetches missing models into dir and checks their hashes.
func DownloadModels(ctx context.Context, dir string, log func(string)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, m := range Models {
		dst := filepath.Join(dir, m.File)
		if ok, _ := fileHash(dst, m.SHA256); ok {
			log(fmt.Sprintf("%s ist schon da", m.File))
			continue
		}
		log(fmt.Sprintf("lade %s …", m.File))
		if err := download(ctx, m, dst); err != nil {
			return fmt.Errorf("%s: %w", m.File, err)
		}
	}
	return nil
}

func fileHash(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

func download(ctx context.Context, m Model, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		os.Remove(tmp)
		return fmt.Errorf("Prüfsumme stimmt nicht (%s)", got)
	}
	return os.Rename(tmp, dst)
}
