// Package gallery indexes photo and video folders and produces everything the
// web UI needs: metadata, thumbnails, animated video previews, faces and search.
package gallery

import (
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgrieser/schuhkarton/internal/helper"
)

// Kind distinguishes photos from videos.
type Kind uint8

const (
	Photo Kind = 1
	Video Kind = 2
)

// Root is one folder tree that is served and indexed.
type Root struct {
	Name string
	Path string
}

// Options configure a Library.
type Options struct {
	Roots    []Root
	CacheDir string
	Exclude  []string // glob patterns matched against file and folder names
	Workers  int

	ExifTool string // path to exiftool; empty: dates from names and folders only
	FFmpeg   string // path to ffmpeg; empty: no video thumbnails

	ThumbSize        int  // short edge of grid thumbnails
	PreviewSize      int  // long edge of lightbox previews
	Quality          int  // JPEG quality
	Previews         bool // also pre-generate the large lightbox previews
	VideoPreviews    bool // animated video previews
	VideoPreviewSize int  // their short edge
	Transcode        bool // H.264 for browsers that cannot play the original

	// IgnoreFolders: no date, place or category from folder names.
	IgnoreFolders bool
	// WriteFiles: admin edits are written into the files (exiftool).
	WriteFiles bool
	// TrashDays: deleted files leave the trash after this many days (0: never).
	TrashDays int

	Faces FaceOptions
}

// FaceOptions configure face detection.
type FaceOptions struct {
	Mode             string // "off", "local" (Python here) or "remote" (face container)
	URL, Token       string // remote
	Workers          int
	MinScore         float64
	ClusterThreshold float64
	NamedThreshold   float64
	Python           string // local
	ModelsDir        string // local
}

// Face is one detected face. Box coordinates are relative (0..1) to the
// oriented image.
type Face struct {
	X, Y, W, H float32
	Score      float32
	Emb        []float32 // normalized SFace embedding, nil without recognition model
	Person     string    // filled by clustering
}

// Item is one photo or video.
type Item struct {
	ID       string
	Root     int    // index into Options.Roots, resolved from RootName on load
	RootName string // name of the root, persisted
	Rel      string // slash separated, relative to the root
	Kind     Kind
	Size     int64
	MTime    int64

	Taken     int64 // wall-clock time of capture, stored as if it were UTC
	TakenSrc  string
	MetaTaken int64   // capture time from the metadata alone
	W, H      int     // oriented pixel size
	Aspect    float64 // width/height of what is displayed (corrected by thumbnails)
	Orient    int

	GPS      bool
	Lat, Lon float64
	Alt      float64

	Make, Model, Lens, Software string
	FNumber, Focal              float64
	Exposure                    float64
	ISO                         int
	Duration                    float64
	Codec                       string

	Desc     string
	Keywords []string

	Album    string // folder relative to the root
	Place    string // place from a "MM Monat Ort" folder, XMP City or an edit
	Category string // category from a collection like Dokumente/<Jahr>/<MM Monat Kategorie>, XMP or an edit

	MetaPlace    string // XMP-photoshop:City
	MetaCategory string // XMP-photoshop:Category
	Rot          int    // extra clockwise rotation by an admin (0, 90, 180, 270), applied to thumbnails

	LiveVideo string // photo: id of the Live Photo video
	LivePhoto string // video: id of the photo it belongs to (hidden in grids)

	MetaDone  bool
	FacesDone bool
	Faces     []Face

	key string // cache key, derived from path, size and mtime
}

// Key identifies the cached derivatives of this exact file version.
func (it *Item) Key() string { return it.key }

// Status reports background progress for the UI.
type Status struct {
	Phase    string `json:"phase"`
	Photos   int    `json:"photos"`
	Videos   int    `json:"videos"`
	Meta     int64  `json:"meta"`
	MetaTodo int64  `json:"metaTodo"`
	Thumbs   int64  `json:"thumbs"`
	Posters  int64  `json:"posters"`
	Previews int64  `json:"previews"`
	// ThumbFail and PreviewFail count files whose thumbnail or video preview
	// could not be made (broken files); the UI counts them as done.
	ThumbFail   int64  `json:"thumbFail"`
	PreviewFail int64  `json:"previewFail"`
	Faces       int64  `json:"faces"`
	FaceTodo    int64  `json:"faceTodo"`
	People      int    `json:"people"`
	FaceMode    string `json:"faceMode"`
	FaceOK      bool   `json:"faceDetection"`
	FaceRec     bool   `json:"faceRecognition"`
	FaceErr     string `json:"faceError,omitempty"`
	Scanned     int64  `json:"scanned"`
	Errors      int64  `json:"errors"`
	Version     int64  `json:"version"`
}

// Library holds the index and the background workers.
type Library struct {
	opt Options

	mu    sync.RWMutex
	items []*Item // sorted by Taken, newest first
	byID  map[string]*Item

	version  atomic.Int64
	scanning atomic.Bool

	cnt struct {
		meta, metaTodo, thumbs, posters, previews, faces, faceTodo, scanned, errors atomic.Int64
		thumbFail, previewFail                                                      atomic.Int64 // files without a thumbnail / preview (broken)
	}
	phase atomic.Value // string

	favMu sync.Mutex
	favs  map[string]map[string]bool // user -> root name + "/" + rel

	people *peopleStore
	search searchIndex

	opMu    sync.Mutex           // one admin operation (edit, rotate, move, trash) at a time
	editMu  sync.Mutex           // guards edits
	edits   map[string]*Override // root name + "/" + rel -> admin edits
	trashMu sync.Mutex
	trash   []*TrashEntry

	faces faceBackend  // nil when face detection is off
	local *helper.Pool // Python helper for formats Go cannot decode (mode local)
	media *mediaGen

	saveMu sync.Mutex
	dirty  atomic.Bool
	lock   *os.File

	stopBG context.CancelFunc
	bg     sync.WaitGroup
}

const indexVersion = 3

type indexFile struct {
	Version int
	Items   []*Item
}

// Open loads a cached index (if any) and prepares the library. Call Scan
// afterwards to bring it up to date.
func Open(opt Options) (*Library, error) {
	if len(opt.Roots) == 0 {
		return nil, errors.New("no folders to serve")
	}
	opt = withDefaults(opt)
	for _, d := range []string{"", "thumbs", "faces"} {
		if err := os.MkdirAll(filepath.Join(opt.CacheDir, d), 0o755); err != nil {
			return nil, err
		}
	}
	// one process per cache: two would overwrite each other's index
	lf, err := lockCache(filepath.Join(opt.CacheDir, "lock"))
	if err != nil {
		return nil, fmt.Errorf("der Cache %s wird schon von einem anderen schuhkarton-Prozess benutzt", opt.CacheDir)
	}
	l := &Library{opt: opt, byID: map[string]*Item{}, favs: map[string]map[string]bool{}, lock: lf}
	l.phase.Store("bereit")
	switch opt.Faces.Mode {
	case "local":
		l.local = helper.New(helper.Options{Python: opt.Faces.Python, ScriptDir: opt.CacheDir,
			Models: modelDirs(opt), Size: opt.Faces.Workers, MinScore: opt.Faces.MinScore})
		l.faces = &localFaces{pool: l.local}
	case "remote":
		l.faces = newRemoteFaces(opt.Faces.URL, opt.Faces.Token)
	}
	l.media = newMediaGen(l)
	l.people = newPeopleStore(filepath.Join(opt.CacheDir, "people.json"), opt.Faces.ClusterThreshold, opt.Faces.NamedThreshold)
	l.loadEdits()
	l.loadTrash()
	l.loadIndex()
	l.loadFavs()
	l.recluster()
	return l, nil
}

// Close stops helpers and writes pending state.
func (l *Library) Close() {
	if l.stopBG != nil {
		l.stopBG()
	}
	done := make(chan struct{})
	go func() { l.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		l.logf("background jobs still running, closing anyway")
	}
	l.saveIndex()
	if l.local != nil {
		l.local.Close()
	}
	l.lock.Close()
}

func withDefaults(o Options) Options {
	o.Workers = max(1, o.Workers)
	if o.ThumbSize <= 0 {
		o.ThumbSize = 360
	}
	if o.PreviewSize <= 0 {
		o.PreviewSize = 2048
	}
	if o.Quality <= 0 || o.Quality > 100 {
		o.Quality = 82
	}
	if o.VideoPreviewSize <= 0 {
		o.VideoPreviewSize = 240
	}
	if o.Faces.Mode == "" {
		o.Faces.Mode = "off"
	}
	o.Faces.Workers = max(1, o.Faces.Workers)
	if o.Faces.MinScore <= 0 {
		o.Faces.MinScore = 0.86
	}
	if o.Faces.ClusterThreshold <= 0 {
		o.Faces.ClusterThreshold = 0.48
	}
	if o.Faces.NamedThreshold <= 0 {
		o.Faces.NamedThreshold = 0.42
	}
	return o
}

// modelDirs lists where the face models are looked up.
func modelDirs(o Options) []string {
	dirs := []string{o.Faces.ModelsDir, filepath.Join(o.CacheDir, "models")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".cache/picture-organizer"))
	}
	return dirs
}

// FaceStatus reports the face backend.
func (l *Library) FaceStatus() FaceInfo {
	if l.faces == nil {
		return FaceInfo{Mode: "off"}
	}
	return l.faces.probe()
}

func (l *Library) logf(format string, args ...any) { log.Printf(format, args...) }

// Options returns the configuration.
func (l *Library) Options() Options { return l.opt }

// Version changes whenever items, favorites or people change.
func (l *Library) Version() int64 { return l.version.Load() }

func (l *Library) bump() { l.version.Add(1) }

// Items returns the items newest first. The slice must not be modified.
func (l *Library) Items() []*Item {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.items
}

func (l *Library) allItems() []*Item { return l.Items() }

// Get returns one item.
func (l *Library) Get(id string) *Item {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byID[id]
}

// Path returns the absolute file path of an item.
func (l *Library) Path(it *Item) string {
	return filepath.Join(l.opt.Roots[it.Root].Path, filepath.FromSlash(it.Rel))
}

// RootName returns the display name of the item's root.
func (l *Library) RootName(it *Item) string { return l.opt.Roots[it.Root].Name }

// Status returns progress counters.
func (l *Library) Status() Status {
	s := Status{
		Phase:       l.phase.Load().(string),
		Meta:        l.cnt.meta.Load(),
		MetaTodo:    l.cnt.metaTodo.Load(),
		Thumbs:      l.cnt.thumbs.Load(),
		Posters:     l.cnt.posters.Load(),
		Previews:    l.cnt.previews.Load(),
		ThumbFail:   l.cnt.thumbFail.Load(),
		PreviewFail: l.cnt.previewFail.Load(),
		Faces:       l.cnt.faces.Load(),
		FaceTodo:    l.cnt.faceTodo.Load(),
		Scanned:     l.cnt.scanned.Load(),
		Errors:      l.cnt.errors.Load(),
		Version:     l.Version(),
	}
	fs := l.FaceStatus()
	s.FaceMode, s.FaceOK, s.FaceRec, s.FaceErr = fs.Mode, fs.Detection, fs.Recognition, fs.Error
	for _, it := range l.Items() {
		if it.Kind == Video {
			if it.LivePhoto == "" {
				s.Videos++
			}
		} else {
			s.Photos++
		}
	}
	s.People = l.people.count()
	return s
}

func itemID(root, rel string) string {
	h := sha1.Sum([]byte(root + "\x00" + rel))
	return hex.EncodeToString(h[:6])
}

func itemKey(root, rel string, size, mtime int64) string {
	h := sha1.Sum(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d", root, rel, size, mtime))
	return hex.EncodeToString(h[:10])
}

// keyOf is the cache key of an item: its file version plus an admin rotation.
func (l *Library) keyOf(it *Item) string {
	k := itemKey(l.opt.Roots[it.Root].Name, it.Rel, it.Size, it.MTime)
	if it.Rot != 0 {
		h := sha1.Sum(fmt.Appendf(nil, "%s\x00rot%d", k, it.Rot))
		k = hex.EncodeToString(h[:10])
	}
	return k
}

func (l *Library) loadIndex() {
	f, err := os.Open(filepath.Join(l.opt.CacheDir, "index.gob.gz"))
	if err != nil {
		return
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return
	}
	var idx indexFile
	if err := gob.NewDecoder(zr).Decode(&idx); err != nil || idx.Version != indexVersion {
		l.logf("index cache ignored (%v)", err)
		return
	}
	// Map cached items onto the configured roots by name
	byName := map[string]int{}
	for i, r := range l.opt.Roots {
		byName[r.Name] = i
	}
	var items []*Item
	for _, it := range idx.Items {
		i, ok := byName[it.RootName]
		if !ok {
			continue
		}
		it.Root = i
		items = append(items, it)
	}
	l.setItems(items)
	l.logf("index cache: %d items", len(items))
}

func (l *Library) setItems(items []*Item) {
	for _, it := range items {
		it.key = l.keyOf(it)
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].Taken != items[b].Taken {
			return items[a].Taken > items[b].Taken
		}
		return items[a].Rel > items[b].Rel
	})
	m := make(map[string]*Item, len(items))
	for _, it := range items {
		m[it.ID] = it
	}
	l.mu.Lock()
	l.items, l.byID = items, m
	l.mu.Unlock()
	l.bump()
}

func (l *Library) saveIndex() {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	l.dirty.Store(false)
	path := filepath.Join(l.opt.CacheDir, "index.gob.gz")
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		l.logf("save index: %v", err)
		return
	}
	zw := gzip.NewWriter(f)
	l.mu.RLock()
	err = gob.NewEncoder(zw).Encode(indexFile{Version: indexVersion, Items: l.items})
	l.mu.RUnlock()
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		l.logf("save index: %v", err)
	}
}

// markDirty schedules an index save.
func (l *Library) markDirty() {
	if l.dirty.Swap(true) {
		return
	}
	time.AfterFunc(20*time.Second, l.saveIndex)
}

// --- favorites ---------------------------------------------------------------

// Favorites belong to an account. favorites.json maps user names to item keys
// (root name + "/" + rel). The file of older versions held one list for
// everybody; it is kept under the empty user name until an admin claims it.

func (l *Library) favKey(it *Item) string { return l.opt.Roots[it.Root].Name + "/" + it.Rel }

type favFile struct {
	Version int                 `json:"version"`
	Users   map[string][]string `json:"users"`
}

func (l *Library) loadFavs() {
	b, err := os.ReadFile(filepath.Join(l.opt.CacheDir, "favorites.json"))
	if err != nil {
		return
	}
	var legacy []string
	if json.Unmarshal(b, &legacy) == nil {
		set := map[string]bool{}
		for _, k := range legacy {
			set[k] = true
		}
		l.favs[""] = set
		return
	}
	var f favFile
	if json.Unmarshal(b, &f) == nil {
		for u, keys := range f.Users {
			set := map[string]bool{}
			for _, k := range keys {
				set[k] = true
			}
			l.favs[u] = set
		}
	}
}

// saveFavs writes favorites.json; the caller holds favMu.
func (l *Library) saveFavs() error {
	f := favFile{Version: 2, Users: map[string][]string{}}
	for u, set := range l.favs {
		if len(set) == 0 {
			continue
		}
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		f.Users[u] = keys
	}
	b, _ := json.MarshalIndent(f, "", " ")
	return writeFileAtomic(filepath.Join(l.opt.CacheDir, "favorites.json"), b)
}

// IsFav reports whether an item is one of user's favorites. Without an
// account ("") nothing is a favorite.
func (l *Library) IsFav(user string, it *Item) bool {
	if user == "" {
		return false
	}
	l.favMu.Lock()
	defer l.favMu.Unlock()
	return l.favs[user][l.favKey(it)]
}

// FavCount returns how many favorites user has.
func (l *Library) FavCount(user string) int {
	l.favMu.Lock()
	defer l.favMu.Unlock()
	return len(l.favs[user])
}

// SetFav marks or unmarks favorites of user.
func (l *Library) SetFav(user string, ids []string, on bool) error {
	if user == "" {
		return errors.New("favorites need an account")
	}
	l.favMu.Lock()
	defer l.favMu.Unlock()
	set := l.favs[user]
	if set == nil {
		set = map[string]bool{}
		l.favs[user] = set
	}
	for _, id := range ids {
		if it := l.Get(id); it != nil {
			if on {
				set[l.favKey(it)] = true
			} else {
				delete(set, l.favKey(it))
			}
		}
	}
	l.bump()
	return l.saveFavs()
}

// ClaimLegacyFavs gives the favorites of older versions (one list for
// everybody) to user, typically the first admin.
func (l *Library) ClaimLegacyFavs(user string) error {
	if user == "" {
		return nil
	}
	l.favMu.Lock()
	defer l.favMu.Unlock()
	legacy := l.favs[""]
	if len(legacy) == 0 {
		return nil
	}
	set := l.favs[user]
	if set == nil {
		set = map[string]bool{}
		l.favs[user] = set
	}
	for k := range legacy {
		set[k] = true
	}
	delete(l.favs, "")
	l.bump()
	return l.saveFavs()
}

// DropUserFavs forgets the favorites of a deleted account.
func (l *Library) DropUserFavs(user string) error {
	l.favMu.Lock()
	defer l.favMu.Unlock()
	if _, ok := l.favs[user]; !ok {
		return nil
	}
	delete(l.favs, user)
	l.bump()
	return l.saveFavs()
}

// RenameKeys moves per-file state (favorites of every user) from old item
// keys (root name + "/" + rel) to new ones after files were moved.
func (l *Library) RenameKeys(pairs map[string]string) error {
	if len(pairs) == 0 {
		return nil
	}
	l.favMu.Lock()
	defer l.favMu.Unlock()
	changed := false
	for _, set := range l.favs {
		for from, to := range pairs {
			if set[from] {
				delete(set, from)
				if to != "" {
					set[to] = true
				}
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	l.bump()
	return l.saveFavs()
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- media type helpers -----------------------------------------------------

var photoExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".gif": true, ".webp": true,
	".heic": true, ".heif": true, ".avif": true, ".bmp": true, ".tif": true, ".tiff": true,
	".arw": true, ".cr2": true, ".cr3": true, ".nef": true, ".dng": true, ".orf": true, ".rw2": true, ".raf": true,
}

var videoExt = map[string]bool{
	".mov": true, ".mp4": true, ".m4v": true, ".3gp": true, ".avi": true, ".mts": true, ".m2ts": true,
	".mkv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".wmv": true, ".flv": true,
}

var rawExt = map[string]bool{".arw": true, ".cr2": true, ".cr3": true, ".nef": true, ".dng": true, ".orf": true, ".rw2": true, ".raf": true}

// KindOf tells photos from videos by the file extension (0: neither).
func KindOf(name string) Kind {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case photoExt[ext]:
		return Photo
	case videoExt[ext]:
		return Video
	}
	return 0
}
