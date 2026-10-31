package gallery

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// EXIF orientation as (mirror horizontally first, then rotate clockwise).
var orientMR = [9][2]int{{0, 0}, {0, 0}, {1, 0}, {0, 180}, {1, 180}, {1, 270}, {0, 90}, {1, 90}, {0, 270}}

// composeOrient returns the orientation that shows an image stored with
// orientation o turned by deg more degrees clockwise.
func composeOrient(o, deg int) int {
	if o < 1 || o > 8 {
		o = 1
	}
	m, r := orientMR[o][0], ((orientMR[o][1]+deg)%360+360)%360
	for i := 1; i <= 8; i++ {
		if orientMR[i][0] == m && orientMR[i][1] == r {
			return i
		}
	}
	return 1
}

// displayOrient is the orientation to apply to the decoded pixels of an
// item: EXIF (unless the decoder already applied the container rotation)
// plus an admin rotation.
func (l *Library) displayOrient(it *Item, src string) int {
	o := it.Orient
	if !needsOrient(src) {
		o = 1
	}
	return composeOrient(o, it.Rot)
}

// rotateBox turns a relative face box by deg clockwise.
func rotateBox(f Face, deg int) Face {
	switch ((deg % 360) + 360) % 360 {
	case 90:
		f.X, f.Y, f.W, f.H = 1-(f.Y+f.H), f.X, f.H, f.W
	case 180:
		f.X, f.Y = 1-(f.X+f.W), 1-(f.Y+f.H)
	case 270:
		f.X, f.Y, f.W, f.H = f.Y, 1-(f.X+f.W), f.H, f.W
	}
	return f
}

// rotateJPEGFile writes src turned by deg clockwise to dst.
func rotateJPEGFile(src, dst string, deg int) error {
	img, err := decodeFile(src)
	if err != nil {
		return err
	}
	return writeJPEG(dst, orient(img, composeOrient(1, deg)), 90)
}

// orientWritable reports whether the rotation can go into the file as an
// EXIF orientation that every viewer honours.
func orientWritable(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".jpg", ".jpeg", ".jpe", ".tif", ".tiff":
		return true
	}
	return false
}

// Rotate turns photos by deg clockwise (90, 180, 270; -90 counts as 270).
// JPEG and TIFF get a new EXIF orientation when files may be written;
// everything else keeps the rotation in Schuhkarton (thumbnails and
// previews are turned, the original stays as it is).
func (l *Library) Rotate(ids []string, deg int, by string) (EditResult, error) {
	var res EditResult
	deg = ((deg % 360) + 360) % 360
	if deg == 0 || deg%90 != 0 {
		return res, errors.New("nur 90, 180 oder 270 Grad")
	}
	l.opMu.Lock()
	defer l.opMu.Unlock()
	var changed []*Item
	for _, id := range ids {
		it := l.Get(id)
		if it == nil {
			continue
		}
		if it.Kind != Photo {
			res.warn("%s: Videos lassen sich nicht drehen", path.Base(it.Rel))
			continue
		}
		oldKey := it.key
		written := false
		if l.opt.WriteFiles && l.opt.ExifTool != "" && it.Rot == 0 && orientWritable(it.Rel) {
			no := composeOrient(it.Orient, deg)
			if err := l.writeTags(it, []string{fmt.Sprintf("-Orientation#=%d", no)}); err != nil {
				res.warn("%s: %v", path.Base(it.Rel), err)
			} else {
				written = true
				res.Written++
				st, serr := os.Stat(l.Path(it))
				l.mu.Lock()
				it.Orient = no
				if serr == nil {
					it.Size, it.MTime = st.Size(), st.ModTime().Unix()
				}
				if deg != 180 {
					it.W, it.H = it.H, it.W
					if it.Aspect > 0 {
						it.Aspect = 1 / it.Aspect
					}
				}
				l.mu.Unlock()
			}
		}
		if !written {
			l.editMu.Lock()
			key := l.favKey(it)
			o := l.edits[key]
			if o == nil {
				o = &Override{}
				l.edits[key] = o
			}
			o.Rot = (o.Rot + deg) % 360
			o.At, o.By = nowUnix(), by
			l.editMu.Unlock()
			l.mu.Lock()
			l.applyOverride(it) // sets Rot, swaps W/H
			l.mu.Unlock()
		}
		l.mu.Lock()
		for i := range it.Faces {
			it.Faces[i] = rotateBox(it.Faces[i], deg)
		}
		l.mu.Unlock()
		l.rekey(it, oldKey, deg)
		changed = append(changed, it)
		res.Changed++
		// a Live Photo's video turns along, or it would play sideways over the photo
		if it.LiveVideo != "" {
			if lv := l.Get(it.LiveVideo); lv != nil {
				if err := l.rotateVideo(lv, deg); err != nil {
					res.warn("%s: %v", path.Base(lv.Rel), err)
				} else {
					changed = append(changed, lv)
				}
			}
		}
	}
	if err := l.saveEdits(); err != nil {
		return res, err
	}
	l.refresh(changed)
	return res, nil
}

func nowUnix() int64 { return time.Now().Unix() }

// rotateVideo turns a video by deg clockwise through the rotation of its
// QuickTime track header, which browsers and ffmpeg honour. Thumbnails,
// previews and transcodes are made again from the turned file.
func (l *Library) rotateVideo(it *Item, deg int) error {
	if !l.opt.WriteFiles || l.opt.ExifTool == "" {
		return errors.New("Live-Video bleibt ungedreht: Änderungen werden nicht in Dateien geschrieben")
	}
	out, err := exifToolCmd(l.opt.ExifTool, "-s3", "-n", "-Rotation", l.Path(it)).Output()
	if err != nil {
		return fmt.Errorf("Drehung nicht lesbar: %v", err)
	}
	cur := atoi(strings.TrimSpace(string(out)))
	next := ((cur+deg)%360 + 360) % 360
	if err := l.writeTags(it, []string{fmt.Sprintf("-Rotation=%d", next)}); err != nil {
		return err
	}
	oldKey := it.key
	st, serr := os.Stat(l.Path(it))
	if serr == nil && st.Size() == it.Size && st.ModTime().Unix() == it.MTime {
		// same size within the same second: move the time on, so the cache key changes
		t := st.ModTime().Add(time.Second)
		if os.Chtimes(l.Path(it), t, t) == nil {
			st, serr = os.Stat(l.Path(it))
		}
	}
	l.mu.Lock()
	if serr == nil {
		it.Size, it.MTime = st.Size(), st.ModTime().Unix()
	}
	if deg != 180 {
		it.W, it.H = it.H, it.W
		if it.Aspect > 0 {
			it.Aspect = 1 / it.Aspect
		}
	}
	it.key = l.keyOf(it)
	l.mu.Unlock()
	if it.key != oldKey {
		l.removeDerivatives(oldKey, 0) // the poster and previews show the old turn
	}
	return nil
}
