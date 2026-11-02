package gallery

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Files whose content does not fit their name, like a HEIC photo saved as
// IMG_0253.jpeg by a phone app or a converter. The name stays as it is;
// Schuhkarton decodes, turns, serves and writes such a file by what it is.

// sniffExt returns the extension that fits the start of a file ("" when the
// format is not known).
func sniffExt(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(head, []byte("GIF8")):
		return ".gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return ".webp"
	case bytes.HasPrefix(head, []byte("II*\x00")), bytes.HasPrefix(head, []byte("MM\x00*")):
		return ".tif"
	case bytes.HasPrefix(head, []byte("BM")) && len(head) >= 14:
		return ".bmp"
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		// the major brand, then the compatible ones up to the end of the box
		end := min(len(head), int(uint32(head[0])<<24|uint32(head[1])<<16|uint32(head[2])<<8|uint32(head[3])))
		var brands []string
		for i := 8; i+4 <= end; i += 4 {
			if i != 12 { // the minor version
				brands = append(brands, string(head[i:i+4]))
			}
		}
		if len(brands) == 0 {
			return ".mp4"
		}
		for _, b := range brands {
			if b == "avif" || b == "avis" {
				return ".avif"
			}
		}
		switch brands[0] {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
			return ".heic"
		case "qt  ":
			return ".mov"
		case "crx ":
			return ".cr3"
		}
		return ".mp4"
	}
	return ""
}

// canonExt maps the extensions of one format onto one of them.
func canonExt(ext string) string {
	switch ext {
	case ".jpeg", ".jpe":
		return ".jpg"
	case ".tiff":
		return ".tif"
	case ".heif":
		return ".heic"
	}
	return ext
}

// detectFormat returns the extension that fits the content of a photo whose
// name says something else, "" when the name fits or the content is not
// known. RAW files (TIFF inside) and videos are left alone.
func detectFormat(p string) string {
	ext, _ := checkFormat(p)
	return ext
}

// checkFormat is detectFormat that also tells whether the file could be read.
func checkFormat(p string) (string, bool) {
	ext := strings.ToLower(path.Ext(p))
	if !photoExt[ext] || rawExt[ext] {
		return "", true
	}
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	head := make([]byte, 64)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", false
	}
	real := sniffExt(head[:n])
	if !photoExt[real] || real == canonExt(ext) { // a video inside stays what its name says
		return "", true
	}
	return real, true
}

// Ext returns the extension of an item's format in lower case: by its
// content when the name does not fit, else by its name.
func (it *Item) Ext() string {
	if it.Format != "" {
		return it.Format
	}
	return strings.ToLower(path.Ext(it.Rel))
}

// withRealName lets exiftool write into a file whose name does not fit its
// content. exiftool picks the format by the name and refuses such a file
// ("Not a valid JPEG (looks more like a MOV)"), so a copy with the right
// extension in the cache takes the change, which is then written back into
// the file itself: its name, links, owner and permissions stay. Should that
// fail halfway, the file gets its old content back.
func (l *Library) withRealName(it *Item, p string, write func(string) error) error {
	if it.Format == "" {
		return write(p)
	}
	tmp := func(ext string) (string, error) {
		f, err := os.CreateTemp(l.opt.CacheDir, "write-*"+ext)
		if err != nil {
			return "", err
		}
		f.Close()
		return f.Name(), nil
	}
	backup, err := tmp(".orig")
	if err != nil {
		return err
	}
	keep := false // the old content when it could not be restored
	defer func() {
		if !keep {
			os.Remove(backup)
		}
	}()
	local, err := tmp(it.Format)
	if err != nil {
		return err
	}
	defer os.Remove(local)
	if err := copyFile(p, backup); err != nil {
		return err
	}
	if err := copyFile(backup, local); err != nil {
		return err
	}
	if err := write(local); err != nil {
		return err
	}
	if err := overwrite(p, local); err != nil {
		if rerr := overwrite(p, backup); rerr != nil {
			keep = true
			return fmt.Errorf("%v; restoring failed too: %v (old content in %s)", err, rerr, backup)
		}
		return err
	}
	return nil
}

// overwrite writes the content and time of src into the file dst.
func overwrite(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if st, err := in.Stat(); err == nil {
		os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}
