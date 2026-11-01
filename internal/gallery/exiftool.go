package gallery

import (
	"bufio"
	"bytes"
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
	"time"
)

// exifTool is a long-running "exiftool -stay_open" process.
type exifTool struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	errBuf *syncBuffer
}

// FindExifTool returns a usable exiftool path or "".
func FindExifTool(hint string) string {
	cands := []string{hint, "exiftool",
		filepath.Join(os.Getenv("HOME"), ".local/share/picture-organizer/exiftool/usr/bin/exiftool")}
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

func exifToolCmd(bin string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	// exiftool unpacked from a .deb next to its perl modules
	if lib := filepath.Join(filepath.Dir(bin), "../share/perl5"); dirExists(lib) {
		cmd.Env = append(os.Environ(), "PERL5LIB="+lib)
	}
	return cmd
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// startExifTool starts exiftool; cfg is an exiftool config file (the
// XMP-schuhkarton tags) or "".
func startExifTool(bin, cfg string) (*exifTool, error) {
	if bin == "" {
		return nil, errors.New("not found")
	}
	var args []string
	if cfg != "" {
		args = append(args, "-config", cfg) // must come first
	}
	cmd := exifToolCmd(bin, append(args, "-stay_open", "True", "-@", "-")...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	eb := &syncBuffer{}
	cmd.Stderr = eb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &exifTool{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20), errBuf: eb}, nil
}

func (e *exifTool) close() {
	fmt.Fprint(e.in, "-stay_open\nFalse\n")
	e.in.Close()
	done := make(chan struct{})
	go func() { e.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		e.cmd.Process.Kill()
	}
}

var exifTags = []string{
	"-DateTimeOriginal", "-SubSecDateTimeOriginal", "-CreateDate", "-CreationDate", "-MediaCreateDate",
	"-GPSLatitude", "-GPSLongitude", "-GPSAltitude",
	"-Make", "-Model", "-LensModel", "-Software",
	"-FNumber", "-ExposureTime", "-ISO", "-FocalLength",
	"-ImageWidth", "-ImageHeight", "-ExifImageWidth", "-ExifImageHeight", "-Orientation", "-Rotation",
	"-Duration", "-CompressorID", "-VideoCodec",
	"-XMP-dc:Description", "-ImageDescription", "-Caption-Abstract",
	"-XMP-dc:Subject", "-Keywords", "-XMP-photoshop:City", "-XMP-photoshop:Category",
}

// metaTags: exifTags plus what Schuhkarton wrote (xmp.go).
var metaTags = append(append([]string(nil), exifTags...), xmpReadTags...)

// read returns the metadata per path.
func (e *exifTool) read(paths []string) (map[string]map[string]any, error) {
	var b bytes.Buffer
	for _, a := range append([]string{"-json", "-n", "-api", "QuickTimeUTC=1", "-api", "LargeFileSupport=1",
		"-charset", "filename=utf8", "-m"}, metaTags...) {
		b.WriteString(a + "\n")
	}
	for _, p := range paths {
		b.WriteString(p + "\n")
	}
	b.WriteString("-execute\n")
	if _, err := e.in.Write(b.Bytes()); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for {
		line, err := e.out.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "{ready}") {
			break
		}
		buf.WriteString(line)
	}
	res := map[string]map[string]any{}
	if buf.Len() == 0 {
		return res, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil {
		return res, err
	}
	for _, m := range arr {
		if sf, ok := m["SourceFile"].(string); ok {
			res[sf] = m
		}
	}
	return res, nil
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		var parts []string
		for _, x := range v {
			parts = append(parts, fmt.Sprint(x))
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(strings.Fields(v + " 0")[0], 64)
		return f
	}
	return 0
}

func list(m map[string]any, k string) []string {
	switch v := m[k].(type) {
	case string:
		var out []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		var out []string
		for _, x := range v {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case float64:
		return []string{strconv.FormatFloat(v, 'f', -1, 64)}
	}
	return nil
}

// parseExifTime reads "YYYY:MM:DD HH:MM:SS[.ss][+hh:mm]" as wall-clock time.
func parseExifTime(s string) int64 {
	if len(s) < 19 {
		return 0
	}
	return mkTime(s[0:4], s[5:7], s[8:10], s[11:13], s[14:16], s[17:19])
}

func applyMeta(it *Item, m map[string]any) {
	for _, k := range []string{"SubSecDateTimeOriginal", "DateTimeOriginal", "CreationDate", "CreateDate", "MediaCreateDate"} {
		if t := parseExifTime(str(m, k)); t != 0 {
			it.MetaTaken = t
			break
		}
	}
	lat, lon := num(m, "GPSLatitude"), num(m, "GPSLongitude")
	if (lat != 0 || lon != 0) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180 {
		it.GPS, it.Lat, it.Lon = true, lat, lon
		it.Alt = num(m, "GPSAltitude")
	}
	it.Make, it.Model, it.Lens, it.Software = str(m, "Make"), str(m, "Model"), str(m, "LensModel"), str(m, "Software")
	if it.Make != "" && strings.HasPrefix(strings.ToLower(it.Model), strings.ToLower(strings.Fields(it.Make + " x")[0])) {
		it.Make = "" // "Canon" + "Canon EOS 600D"
	}
	it.FNumber, it.Exposure, it.Focal = num(m, "FNumber"), num(m, "ExposureTime"), num(m, "FocalLength")
	it.ISO = int(num(m, "ISO"))
	it.Duration = num(m, "Duration")
	it.Codec = strings.ToLower(str(m, "CompressorID"))
	if it.Codec == "" {
		it.Codec = strings.ToLower(str(m, "VideoCodec"))
	}
	it.Orient = int(num(m, "Orientation"))
	w, h := int(num(m, "ImageWidth")), int(num(m, "ImageHeight"))
	if w == 0 || h == 0 {
		w, h = int(num(m, "ExifImageWidth")), int(num(m, "ExifImageHeight"))
	}
	rot := int(num(m, "Rotation"))
	if (it.Kind == Photo && it.Orient >= 5 && it.Orient <= 8) || (it.Kind == Video && (rot == 90 || rot == 270)) {
		w, h = h, w
	}
	it.W, it.H = w, h
	if w > 0 && h > 0 {
		it.Aspect = float64(w) / float64(h)
	}
	for _, k := range []string{"Description", "ImageDescription", "Caption-Abstract"} {
		if d := str(m, k); d != "" && !junkDescRe.MatchString(d) {
			it.Desc = d
			break
		}
	}
	it.MetaPlace, it.MetaCategory = str(m, "City"), str(m, "Category")
	seen := map[string]bool{}
	it.Keywords = nil
	for _, k := range []string{"Subject", "Keywords"} {
		for _, kw := range list(m, k) {
			if !seen[strings.ToLower(kw)] {
				seen[strings.ToLower(kw)] = true
				it.Keywords = append(it.Keywords, kw)
			}
		}
	}
	it.Keywords = ownKeywords(it.Keywords, m) // not the AI keywords Schuhkarton wrote
}

// extractPreview pulls the embedded JPEG out of a RAW file.
func extractPreview(bin, path string) ([]byte, error) {
	for _, tag := range []string{"-JpgFromRaw", "-PreviewImage", "-ThumbnailImage"} {
		cmd := exifToolCmd(bin, "-b", "-m", tag, path)
		out, err := cmd.Output()
		if err == nil && len(out) > 1000 {
			return out, nil
		}
	}
	return nil, errors.New("no embedded preview")
}
