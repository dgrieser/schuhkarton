package gallery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/dgrieser/schuhkarton/internal/schedule"
)

// Start runs the background generation of thumbnails, previews and faces
// until ctx ends. Scan kicks it after every pass.
func (l *Library) Start(ctx context.Context) {
	ctx, l.stopBG = context.WithCancel(ctx)
	l.bg.Add(1)
	go func() {
		defer l.bg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.media.wake:
				win := l.window(schedule.JobMedia)
				if !win.Open(time.Now()) {
					l.mediaTodo() // the counts for the UI meanwhile
				}
				wctx, cancel, ok := win.Wait(ctx)
				if !ok {
					return
				}
				l.generate(wctx)
				if schedule.Closed(ctx, wctx) {
					l.media.kick() // goes on in the next window
				}
				cancel()
			}
		}
	}()
	l.media.kick()
	if l.aiJob.client != nil {
		l.bg.Add(1)
		go l.aiLoop(ctx)
	}
	if l.ocrJob.client != nil {
		l.bg.Add(1)
		go l.ocrLoop(ctx)
	}
	if l.speechJob.client != nil {
		l.bg.Add(1)
		go l.speechLoop(ctx)
	}
	if l.xmpJob.on {
		l.bg.Add(1)
		go l.xmpLoop(ctx)
	}
	if !l.opt.Dupes.Off {
		l.bg.Add(1)
		go l.dupeLoop(ctx)
	}
	if l.opt.OverflowDir != "" && l.opt.MaxCache >= 0 {
		l.bg.Add(1)
		go l.spillLoop(ctx)
	}
	if l.opt.Import.Dir != "" {
		l.bg.Add(1)
		go l.importLoop(ctx)
	}
}

func (m *mediaGen) kick() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// generate creates whatever is missing, newest items first.
func (l *Library) generate(ctx context.Context) {
	thumbs, previews, faces := l.mediaTodo()
	if len(thumbs)+len(previews)+len(faces) == 0 {
		l.kickDupes()
		return
	}
	l.logf("background: %d thumbnails, %d video previews, %d face scans", len(thumbs), len(previews), len(faces))
	if !wasmCompiled {
		// HEIC/AVIF that the native tool cannot read take minutes each here:
		// last, so they do not hold up everything else
		heifLast(thumbs)
		heifLast(faces)
	}

	var wg sync.WaitGroup
	run := func(list []*Item, workers int, fn func(*Item)) {
		ch := make(chan *Item)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for it := range ch {
					fn(it)
				}
			}()
		}
		go func() {
			defer close(ch)
			for _, it := range list {
				select {
				case ch <- it:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	w := l.opt.Workers
	run(thumbs, w, func(it *Item) {
		if _, err := l.ensureBG(ctx, it, VarSmall); err != nil {
			if ctx.Err() != nil {
				return // the window closed or Schuhkarton stops
			}
			l.logf("thumbnail %s: %v", it.Rel, err)
			l.cnt.thumbFail.Add(1)
			return
		}
		if l.opt.Previews && it.Kind == Photo {
			l.ensureBG(ctx, it, VarLarge)
		}
		if it.Kind == Photo {
			l.cnt.thumbs.Add(1)
		} else {
			l.cnt.posters.Add(1)
		}
	})
	run(previews, max(1, w/3), func(it *Item) {
		if _, err := l.ensureBG(ctx, it, VarPreview); err != nil {
			if ctx.Err() != nil {
				return
			}
			l.logf("preview %s: %v", it.Rel, err)
			l.cnt.previewFail.Add(1)
			return
		}
		l.cnt.previews.Add(1)
	})
	var faceMu sync.Mutex
	lastCluster := time.Now()
	run(faces, l.opt.Faces.Workers, func(it *Item) {
		if !l.scanFaces(ctx, it) {
			return // the window closed or Schuhkarton stops
		}
		l.cnt.faces.Add(1)
		faceMu.Lock()
		if time.Since(lastCluster) > 45*time.Second {
			lastCluster = time.Now()
			faceMu.Unlock()
			l.recluster()
			l.bump()
			return
		}
		faceMu.Unlock()
	})
	wg.Wait()
	if len(faces) > 0 {
		l.recluster()
		l.bump()
	}
	l.saveIndex()
	l.kickDupes() // the new thumbnails can be compared now
	l.logf("background: done")
}

// mediaTodo returns what is missing and stores the counts for the UI.
func (l *Library) mediaTodo() (thumbs, previews, faces []*Item) {
	items := l.Items()
	var nThumb, nPoster, nPrev, nFace int64
	fs := l.FaceStatus()
	faceOK, recog := fs.Detection, fs.Recognition
	has := l.haveCached() // tier 2 listed once, not asked per file
	for _, it := range items {
		if !has(it, VarSmall) || (l.opt.Previews && it.Kind == Photo && !has(it, VarLarge)) {
			thumbs = append(thumbs, it)
		} else if it.Kind == Photo {
			nThumb++
		} else {
			nPoster++
		}
		if it.Kind == Video && l.opt.VideoPreviews {
			if has(it, VarPreview) {
				nPrev++
			} else {
				previews = append(previews, it)
			}
		}
		if faceOK && (it.Kind == Photo || (it.Kind == Video && it.LivePhoto == "" && l.media.ffmpeg != "")) {
			done := it.FacesDone && (!recog || !needsEmbedding(it.Faces))
			if done {
				nFace++
			} else {
				faces = append(faces, it)
			}
		}
	}
	l.cnt.thumbs.Store(nThumb)
	l.cnt.thumbFail.Store(0)
	l.cnt.previewFail.Store(0)
	l.cnt.posters.Store(nPoster)
	l.cnt.previews.Store(nPrev)
	l.cnt.faces.Store(nFace)
	l.cnt.faceTodo.Store(nFace + int64(len(faces))) // total, shown as faces/faceTodo
	return thumbs, previews, faces
}

// scanFaces finds the faces of one photo or video; false when ctx ended
// first (it is scanned again later).
func (l *Library) scanFaces(ctx context.Context, it *Item) bool {
	if ctx.Err() != nil {
		return false
	}
	prefix := filepath.Join(l.opt.CacheDir, "faces", it.key[:2], it.key)
	os.MkdirAll(filepath.Dir(prefix), 0o755)
	// crops first under a name of their own: faces marked by hand or
	// labeled keep their index and crop, found ones move to where they end up
	tmp := prefix + "-new"
	labeled := l.people.labeled(it.ID)
	var faces []Face
	var err error
	if it.Kind == Video {
		faces, err = l.detectVideo(ctx, it, tmp, l.ItemFaces(it), labeled)
	} else {
		faces, err = l.faces.detect(ctx, l, it, tmp)
	}
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		l.logf("faces %s: %v", it.Rel, err)
		if isTransient(err) {
			return true // container unreachable: try again on the next pass
		}
		faces = nil
	}
	l.mu.Lock()
	merged, place := mergeFaces(it.Faces, faces, labeled, l.people.threshNew)
	it.Faces, it.FacesDone = merged, true
	l.mu.Unlock()
	for i, to := range place {
		from := fmt.Sprintf("%s_%d.jpg", tmp, i)
		if to < 0 {
			os.Remove(from)
		} else {
			os.Rename(from, fmt.Sprintf("%s_%d.jpg", prefix, to))
		}
	}
	l.applyMetaFaces(it) // names from the file's metadata (a new cache)
	l.markDirty()
	return true
}

// ItemFaces returns a copy of the faces of an item.
func (l *Library) ItemFaces(it *Item) []Face {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]Face(nil), it.Faces...)
}

// Go runs fn in the background; Close waits for it.
func (l *Library) Go(fn func()) {
	l.bg.Add(1)
	go func() {
		defer l.bg.Done()
		fn()
	}()
}

// GenerateAll creates every missing derivative and returns when done.
func (l *Library) GenerateAll(ctx context.Context) { l.generate(ctx) }

// heifLast moves HEIC/AVIF photos to the end, keeping the order otherwise.
func heifLast(list []*Item) {
	slices.SortStableFunc(list, func(a, b *Item) int {
		ha, hb := heifItem(a), heifItem(b)
		switch {
		case ha == hb:
			return 0
		case hb:
			return -1
		}
		return 1
	})
}

func heifItem(it *Item) bool {
	switch it.Ext() {
	case ".heic", ".heif", ".avif", ".hif":
		return true
	}
	return false
}
