package gallery

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
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
				l.generate(ctx)
			}
		}
	}()
	l.media.kick()
	if l.aiJob.client != nil {
		l.bg.Add(1)
		go l.aiLoop(ctx)
	}
	if !l.opt.Dupes.Off {
		l.bg.Add(1)
		go l.dupeLoop(ctx)
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
	items := l.Items()
	var thumbs, previews, faces []*Item
	var nThumb, nPoster, nPrev, nFace int64
	fs := l.FaceStatus()
	faceOK, recog := fs.Detection, fs.Recognition
	for _, it := range items {
		if !exists(l.CachePath(it, VarSmall)) || (l.opt.Previews && it.Kind == Photo && !exists(l.CachePath(it, VarLarge))) {
			thumbs = append(thumbs, it)
		} else if it.Kind == Photo {
			nThumb++
		} else {
			nPoster++
		}
		if it.Kind == Video && l.opt.VideoPreviews {
			if exists(l.CachePath(it, VarPreview)) {
				nPrev++
			} else {
				previews = append(previews, it)
			}
		}
		if it.Kind == Photo && faceOK {
			done := it.FacesDone && (!recog || len(it.Faces) == 0 || it.Faces[0].Emb != nil)
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
	if len(thumbs)+len(previews)+len(faces) == 0 {
		l.kickDupes()
		return
	}
	l.logf("background: %d thumbnails, %d video previews, %d face scans", len(thumbs), len(previews), len(faces))

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
		if _, err := l.Ensure(ctx, it, VarSmall); err != nil {
			l.logf("thumbnail %s: %v", it.Rel, err)
			l.cnt.thumbFail.Add(1)
			return
		}
		if l.opt.Previews && it.Kind == Photo {
			l.Ensure(ctx, it, VarLarge)
		}
		if it.Kind == Photo {
			l.cnt.thumbs.Add(1)
		} else {
			l.cnt.posters.Add(1)
		}
	})
	run(previews, max(1, w/3), func(it *Item) {
		if _, err := l.Ensure(ctx, it, VarPreview); err != nil {
			l.logf("preview %s: %v", it.Rel, err)
			l.cnt.previewFail.Add(1)
			return
		}
		l.cnt.previews.Add(1)
	})
	var faceMu sync.Mutex
	lastCluster := time.Now()
	run(faces, l.opt.Faces.Workers, func(it *Item) {
		l.scanFaces(it)
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

func (l *Library) scanFaces(it *Item) {
	prefix := filepath.Join(l.opt.CacheDir, "faces", it.key[:2], it.key)
	os.MkdirAll(filepath.Dir(prefix), 0o755)
	faces, err := l.faces.detect(l, it, prefix)
	if err != nil {
		l.logf("faces %s: %v", it.Rel, err)
		if isTransient(err) {
			return // container unreachable: try again on the next pass
		}
		faces = nil
	}
	l.mu.Lock()
	it.Faces, it.FacesDone = faces, true
	l.mu.Unlock()
	l.markDirty()
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
