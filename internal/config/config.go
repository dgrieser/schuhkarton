// Package config holds Schuhkarton's settings: defaults, the YAML file the
// setup wizard writes, SCHUHKARTON_* environment variables and command line
// flags, in that order of precedence. Settings fixed by the environment or a
// flag are "locked": the settings page shows them but cannot change them.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dgrieser/schuhkarton/internal/pathtpl"
	"gopkg.in/yaml.v3"
)

// Config is the whole configuration.
type Config struct {
	Server  Server  `yaml:"server" json:"server"`
	Access  Access  `yaml:"access" json:"access"`
	Library Library `yaml:"library" json:"library"`
	Cache   Cache   `yaml:"cache" json:"cache"`
	Media   Media   `yaml:"media" json:"media"`
	Faces   Faces   `yaml:"faces" json:"faces"`
	AI      AI      `yaml:"ai" json:"ai"`
	Dupes   Dupes   `yaml:"dupes" json:"dupes"`
	UI      UI      `yaml:"ui" json:"ui"`
	Backup  Backup  `yaml:"backup" json:"backup"`
	Worker  Worker  `yaml:"worker" json:"worker"`

	path   string
	found  bool
	locked map[string]string // key -> where it was fixed ("SCHUHKARTON_X", "--flag")
}

// Server is the web server.
type Server struct {
	// Listen is the address, e.g. 127.0.0.1:8088 or 0.0.0.0:8088.
	Listen string `yaml:"listen" json:"listen"`
	// Token protects the gallery: "auto" (required when listening on a
	// network address), "off", or the token itself.
	Token string `yaml:"token" json:"token"`
	// PublicURL is the address printed in links, e.g. https://fotos.example.org.
	PublicURL string `yaml:"public_url" json:"public_url"`
	TLSCert   string `yaml:"tls_cert" json:"tls_cert"`
	TLSKey    string `yaml:"tls_key" json:"tls_key"`
}

// Access decides who may do what. Admins manage settings, accounts and the
// photos; accounts personalise (favorites, names of people).
type Access struct {
	// Anonymous lets people without an account look at and download the
	// photos (still behind server.token on a network address).
	Anonymous bool `yaml:"anonymous" json:"anonymous"`
	// Register lets anybody create an account; it waits for an admin.
	Register bool `yaml:"register" json:"register"`
}

// Folder is one photo folder.
type Folder struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

// Library is what is served.
type Library struct {
	Folders []Folder `yaml:"folders" json:"folders"`
	// Exclude are glob patterns for file and folder names.
	Exclude []string `yaml:"exclude" json:"exclude"`
	// Rescan reads the folders again at this interval, e.g. "1h"; empty or
	// "0" only at start.
	Rescan string `yaml:"rescan" json:"rescan"`
	// FolderMeta reads date, place and category from folder names like
	// "2019/06 Juni Lübbecke" (a heuristic; Schuhkarton never requires or
	// creates such a layout).
	FolderMeta bool `yaml:"folder_meta" json:"folder_meta"`
	// WriteFiles writes edits by admins (date, place, GPS, text, rotation)
	// into the files with exiftool; false keeps them in the cache only.
	WriteFiles bool `yaml:"write_files" json:"write_files"`
	// TrashDays empties deleted files from the trash after this many days
	// (0: only by hand).
	TrashDays int `yaml:"trash_days" json:"trash_days"`
	// Documents names folders whose files count as documents (scans,
	// receipts): galleries hide them unless documents are switched on.
	Documents []string `yaml:"documents" json:"documents"`
	// Rules decide from the metadata where files live. Without rules
	// nothing moves.
	Rules Rules `yaml:"rules" json:"rules"`
	// Import is a folder that empties itself into the library.
	Import Import `yaml:"import" json:"import"`
}

// Import: photos and videos put into Dir (subfolders too) move into the
// library on their own, once they are completely written. The filing rules
// then put them in place; without rules they keep their subfolders below
// the library folder. Dir is emptied either way; other files stay.
type Import struct {
	Dir string `yaml:"dir" json:"dir"` // empty: no import folder
	// Folder is the name (or path) of the library folder to import into
	// (empty: the first one).
	Folder string `yaml:"folder" json:"folder"`
}

// Rules file photos, videos and documents into folders by their metadata.
// The first rule whose condition matches a file decides its folder; files
// that no rule matches stay where they are.
type Rules struct {
	// Locale is the language of month and weekday names (de, en, fr, es,
	// it, nl, da, sv, pl, pt).
	Locale string `yaml:"locale" json:"locale"`
	// Auto moves files as soon as an admin changes their date, place or
	// category; otherwise they move only when the rules are applied.
	Auto bool   `yaml:"auto" json:"auto"`
	List []Rule `yaml:"list" json:"list"`
}

// Rule is one filing rule.
type Rule struct {
	Name string `yaml:"name,omitempty" json:"name"`
	// When is a search like "typ:dokument" or "ort:Berlin"; empty matches
	// every file.
	When string `yaml:"when,omitempty" json:"when"`
	// Path is the folder below the library folder, a template like
	// "{jahr}/{datum:MM MMMM} {ort}" (see internal/pathtpl).
	Path string `yaml:"path" json:"path"`
}

// Cache is where everything derived lives.
type Cache struct {
	Dir string `yaml:"dir" json:"dir"`
	// Previews also generates the large lightbox previews in advance
	// (useful for folders on a slow network share).
	Previews bool `yaml:"previews" json:"previews"`
}

// Media controls thumbnails, video previews and the external tools.
type Media struct {
	Workers          int    `yaml:"workers" json:"workers"`
	ThumbSize        int    `yaml:"thumb_size" json:"thumb_size"`     // short edge of grid thumbnails
	PreviewSize      int    `yaml:"preview_size" json:"preview_size"` // long edge of lightbox previews
	JPEGQuality      int    `yaml:"jpeg_quality" json:"jpeg_quality"`
	VideoPreviews    bool   `yaml:"video_previews" json:"video_previews"`
	VideoPreviewSize int    `yaml:"video_preview_size" json:"video_preview_size"` // short edge
	Transcode        bool   `yaml:"transcode" json:"transcode"`                   // H.264 for browsers without HEVC
	FFmpeg           string `yaml:"ffmpeg" json:"ffmpeg"`
	ExifTool         string `yaml:"exiftool" json:"exiftool"`
}

// Faces controls face detection and people.
type Faces struct {
	// Mode is "off", "remote" (the face container) or "local" (Python with
	// OpenCV on this machine).
	Mode             string  `yaml:"mode" json:"mode"`
	URL              string  `yaml:"url" json:"url"`     // remote: http://host:8089
	Token            string  `yaml:"token" json:"token"` // remote: the container's token
	Workers          int     `yaml:"workers" json:"workers"`
	MinScore         float64 `yaml:"min_score" json:"min_score"`
	ClusterThreshold float64 `yaml:"cluster_threshold" json:"cluster_threshold"`
	NamedThreshold   float64 `yaml:"named_threshold" json:"named_threshold"`
	// Python and ModelsDir are for mode "local" and for the worker.
	Python    string `yaml:"python" json:"python"`
	ModelsDir string `yaml:"models_dir" json:"models_dir"`
}

// AI describes photos with a vision language model behind an
// OpenAI-compatible API (mittwald AI hosting, vLLM, Ollama, …): a short
// description and tags in German and English that the search finds. Off by
// default: photos are sent (downscaled) to that service.
type AI struct {
	// Mode is "off" or "on".
	Mode string `yaml:"mode" json:"mode"`
	// URL is the API base, e.g. https://llm.aihosting.mittwald.de/v1.
	URL string `yaml:"url" json:"url"`
	// APIKey is the key itself; APIKeyEnv names an environment variable
	// that holds it instead (e.g. MITTWALD_LLM_API_KEY). Local servers need
	// neither.
	APIKey    string `yaml:"api_key" json:"api_key"`
	APIKeyEnv string `yaml:"api_key_env" json:"api_key_env"`
	Model     string `yaml:"model" json:"model"`
	Workers   int    `yaml:"workers" json:"workers"`
	// Languages of descriptions and tags: de, en.
	Languages []string `yaml:"languages" json:"languages"`
	// ImageSize is the long edge of the image that is sent.
	ImageSize int `yaml:"image_size" json:"image_size"`
	// Thinking lets reasoning models think first (slower, more tokens).
	Thinking bool `yaml:"thinking" json:"thinking"`
	// OCR recognizes the text in pictures with a model made for it (e.g.
	// GLM-OCR at mittwald), over the same service and key, also with Mode
	// off: off, documents (files in documents folders) or all (every
	// photo). The text is searched and shown with the photo.
	OCR        string `yaml:"ocr" json:"ocr"`
	OCRModel   string `yaml:"ocr_model" json:"ocr_model"`
	OCRWorkers int    `yaml:"ocr_workers" json:"ocr_workers"`
}

// Used reports whether anything is sent to the service (descriptions or
// text recognition).
func (a AI) Used() bool { return a.Mode == "on" || (a.OCR != "" && a.OCR != "off") }

// Dupes is the search for duplicates (perceptual hashes of the thumbnails;
// cheap enough to run inside the gallery).
type Dupes struct {
	// Mode is "on" (default) or "off".
	Mode string `yaml:"mode" json:"mode"`
	// Sensitivity is strict, normal or loose.
	Sensitivity string `yaml:"sensitivity" json:"sensitivity"`
	// SimilarMinutes: photos of a series are at most this far apart.
	SimilarMinutes int `yaml:"similar_minutes" json:"similar_minutes"`
}

// MittwaldAIURL is the OpenAI-compatible endpoint of mittwald AI hosting.
const MittwaldAIURL = "https://llm.aihosting.mittwald.de/v1"

// Key returns the API key, from the environment when APIKeyEnv is set.
func (a AI) Key() string {
	if a.APIKey != "" {
		return a.APIKey
	}
	if a.APIKeyEnv != "" {
		return os.Getenv(strings.TrimPrefix(a.APIKeyEnv, "$"))
	}
	return ""
}

// UI is the look of the web app.
type UI struct {
	Title string `yaml:"title" json:"title"`
	Theme string `yaml:"theme" json:"theme"` // system, light, dark: the default until a viewer picks one
	// Language of the pages: auto (the browser's), de or en; viewers can switch.
	Language       string `yaml:"language" json:"language"`
	MapTiles       string `yaml:"map_tiles" json:"map_tiles"`
	MapAttribution string `yaml:"map_attribution" json:"map_attribution"`
}

// Backup copies photos, cache and settings into a folder in the background.
type Backup struct {
	// Dir is the backup folder (e.g. a USB disk or a network share); empty: off.
	Dir string `yaml:"dir" json:"dir"`
	// Interval between runs, e.g. "24h"; "0": only on demand.
	Interval  string `yaml:"interval" json:"interval"`
	Originals bool   `yaml:"originals" json:"originals"`
	Cache     bool   `yaml:"cache" json:"cache"`
	Config    bool   `yaml:"config" json:"config"`
}

// Worker is the face detection server ("schuhkarton worker").
type Worker struct {
	Listen  string `yaml:"listen" json:"listen"`
	Token   string `yaml:"token" json:"token"`
	Workers int    `yaml:"workers" json:"workers"`
}

// Defaults returns the built-in configuration.
func Defaults() *Config {
	return &Config{
		Server: Server{Listen: "127.0.0.1:8088", Token: "auto"},
		Access: Access{Anonymous: true, Register: true},
		Library: Library{
			Exclude:    []string{"_Duplikate", ".*", "@eaDir", "#recycle", "$RECYCLE.BIN", "System Volume Information", "*.tmp"},
			FolderMeta: true, WriteFiles: true, TrashDays: 30, Rules: Rules{Locale: "de", Auto: true},
			Documents: []string{"Dokumente", "Documents", "Scans", "Belege"},
		},
		Cache: Cache{Dir: DefaultCacheDir()},
		Media: Media{
			Workers: max(2, runtime.NumCPU()/2), ThumbSize: 360, PreviewSize: 2048, JPEGQuality: 82,
			VideoPreviews: true, VideoPreviewSize: 240, Transcode: true,
		},
		Faces: Faces{Mode: "off", Workers: max(1, runtime.NumCPU()/4), MinScore: 0.86, ClusterThreshold: 0.48, NamedThreshold: 0.42},
		AI: AI{Mode: "off", URL: MittwaldAIURL, Model: "Qwen3.8-27B-NVFP4", Workers: 2, Languages: []string{"de", "en"}, ImageSize: 1024,
			OCR: "off", OCRModel: "GLM-OCR", OCRWorkers: 2},
		Dupes: Dupes{Mode: "on", Sensitivity: "normal", SimilarMinutes: 10},
		UI: UI{Title: "Schuhkarton", Theme: "system", Language: "auto",
			MapTiles: "https://tile.openstreetmap.org/{z}/{x}/{y}.png", MapAttribution: "© OpenStreetMap"},
		Backup: Backup{Interval: "24h", Originals: true, Cache: true, Config: true},
		Worker: Worker{Listen: "0.0.0.0:8089", Workers: max(1, runtime.NumCPU()/2)},
		locked: map[string]string{},
	}
}

// Load reads the defaults, the file (if it exists) and the environment.
// An empty path means SCHUHKARTON_CONFIG or the default location.
func Load(path string) (*Config, error) {
	c := Defaults()
	if path == "" {
		path = os.Getenv("SCHUHKARTON_CONFIG")
	}
	if path == "" {
		path = DefaultConfigPath()
	}
	c.path = ExpandHome(path)
	if abs, err := filepath.Abs(c.path); err == nil {
		c.path = abs // relative cache paths are taken from its folder
	}
	raw, err := os.ReadFile(c.path)
	switch {
	case err == nil:
		dec := yaml.NewDecoder(bytes.NewReader([]byte(os.Expand(string(raw), envOrKeep))))
		dec.KnownFields(true)
		if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) { // empty file: defaults
			return nil, fmt.Errorf("%s: %w", c.path, err)
		}
		c.found = true
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if err := c.applyEnv(os.LookupEnv); err != nil {
		return nil, err
	}
	c.normalize()
	return c, nil
}

func envOrKeep(name string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return "${" + name + "}"
}

// normalize fills gaps and expands ~ in paths.
func (c *Config) normalize() {
	d := Defaults()
	// relative cache and model folders live beside the configuration file
	c.Cache.Dir = besideConfig(c.path, ExpandHome(cmpOr(c.Cache.Dir, d.Cache.Dir)))
	for i := range c.Library.Folders {
		c.Library.Folders[i].Path = ExpandHome(c.Library.Folders[i].Path)
	}
	c.Faces.ModelsDir = besideConfig(c.path, ExpandHome(c.Faces.ModelsDir))
	c.Backup.Dir = ExpandHome(c.Backup.Dir)
	c.Library.Import.Dir = ExpandHome(strings.TrimSpace(c.Library.Import.Dir))
	c.Library.Import.Folder = strings.TrimSpace(c.Library.Import.Folder)
	c.Server.Listen = cmpOr(c.Server.Listen, d.Server.Listen)
	c.Server.Token = cmpOr(c.Server.Token, "auto")
	c.Faces.Mode = cmpOr(strings.ToLower(c.Faces.Mode), "off")
	c.Library.Rules.Locale = cmpOr(strings.ToLower(strings.TrimSpace(c.Library.Rules.Locale)), d.Library.Rules.Locale)
	for i, r := range c.Library.Rules.List {
		// a rule without path stays, so Validate reports it instead of it vanishing
		c.Library.Rules.List[i] = Rule{Name: strings.TrimSpace(r.Name), When: strings.TrimSpace(r.When), Path: strings.TrimSpace(r.Path)}
	}
	c.AI.Mode = cmpOr(strings.ToLower(c.AI.Mode), "off")
	c.AI.OCR = cmpOr(strings.ToLower(strings.TrimSpace(c.AI.OCR)), "off")
	c.AI.OCRModel = cmpOr(strings.TrimSpace(c.AI.OCRModel), d.AI.OCRModel)
	if c.AI.OCRWorkers <= 0 {
		c.AI.OCRWorkers = d.AI.OCRWorkers
	}
	c.Dupes.Mode = cmpOr(strings.ToLower(c.Dupes.Mode), d.Dupes.Mode)
	c.Dupes.Sensitivity = cmpOr(strings.ToLower(c.Dupes.Sensitivity), d.Dupes.Sensitivity)
	if c.Dupes.SimilarMinutes <= 0 {
		c.Dupes.SimilarMinutes = d.Dupes.SimilarMinutes
	}
	c.AI.URL = strings.TrimRight(cmpOr(strings.TrimSpace(c.AI.URL), d.AI.URL), "/")
	c.AI.Model = cmpOr(strings.TrimSpace(c.AI.Model), d.AI.Model)
	c.AI.APIKeyEnv = strings.TrimPrefix(strings.TrimSpace(c.AI.APIKeyEnv), "$")
	if c.AI.Workers <= 0 {
		c.AI.Workers = d.AI.Workers
	}
	if c.AI.ImageSize < 256 || c.AI.ImageSize > 4096 {
		c.AI.ImageSize = d.AI.ImageSize
	}
	var langs []string
	for _, l := range c.AI.Languages {
		if l = strings.ToLower(strings.TrimSpace(l)); (l == "de" || l == "en") && !slices.Contains(langs, l) {
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		langs = d.AI.Languages
	}
	c.AI.Languages = langs
	c.UI.Title = cmpOr(c.UI.Title, d.UI.Title)
	c.UI.Theme = cmpOr(c.UI.Theme, d.UI.Theme)
	c.UI.Language = strings.ToLower(cmpOr(c.UI.Language, d.UI.Language))
	c.UI.MapTiles = cmpOr(c.UI.MapTiles, d.UI.MapTiles)
	if c.Media.Workers <= 0 {
		c.Media.Workers = d.Media.Workers
	}
	if c.Media.ThumbSize <= 0 {
		c.Media.ThumbSize = d.Media.ThumbSize
	}
	if c.Media.PreviewSize <= 0 {
		c.Media.PreviewSize = d.Media.PreviewSize
	}
	if c.Media.JPEGQuality <= 0 || c.Media.JPEGQuality > 100 {
		c.Media.JPEGQuality = d.Media.JPEGQuality
	}
	if c.Media.VideoPreviewSize <= 0 {
		c.Media.VideoPreviewSize = d.Media.VideoPreviewSize
	}
	if c.Faces.Workers <= 0 {
		c.Faces.Workers = d.Faces.Workers
	}
	if c.Faces.MinScore <= 0 {
		c.Faces.MinScore = d.Faces.MinScore
	}
	if c.Faces.ClusterThreshold <= 0 {
		c.Faces.ClusterThreshold = d.Faces.ClusterThreshold
	}
	if c.Faces.NamedThreshold <= 0 {
		c.Faces.NamedThreshold = d.Faces.NamedThreshold
	}
	c.Worker.Listen = cmpOr(c.Worker.Listen, d.Worker.Listen)
	if c.Worker.Workers <= 0 {
		c.Worker.Workers = d.Worker.Workers
	}
	// folder names: base name, unique
	seen := map[string]bool{}
	for i := range c.Library.Folders {
		f := &c.Library.Folders[i]
		if f.Name == "" {
			f.Name = filepath.Base(f.Path)
		}
		base := f.Name
		for n := 2; seen[f.Name]; n++ {
			f.Name = fmt.Sprintf("%s-%d", base, n)
		}
		seen[f.Name] = true
	}
	// the import folder may name its library folder by path (the settings
	// page does, as names change while editing): store the final name
	if imp := ExpandHome(c.Library.Import.Folder); filepath.IsAbs(imp) {
		for _, f := range c.Library.Folders {
			if filepath.Clean(f.Path) == filepath.Clean(imp) {
				c.Library.Import.Folder = f.Name
			}
		}
	}
}

func cmpOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// Validate reports settings that cannot work.
func (c *Config) Validate() error {
	var errs []string
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		errs = append(errs, fmt.Sprintf("server.listen %q: %v", c.Server.Listen, err))
	}
	for _, f := range c.Library.Folders {
		if !filepath.IsAbs(f.Path) {
			errs = append(errs, fmt.Sprintf("Ordner %q: Pfad muss absolut sein", f.Path))
		}
	}
	switch c.AI.Mode {
	case "off", "on":
	default:
		errs = append(errs, fmt.Sprintf("ai.mode %q: off oder on", c.AI.Mode))
	}
	switch c.AI.OCR {
	case "off", "documents", "all":
	default:
		errs = append(errs, fmt.Sprintf("ai.ocr %q: off, documents oder all", c.AI.OCR))
	}
	if c.AI.Used() && !strings.HasPrefix(c.AI.URL, "http://") && !strings.HasPrefix(c.AI.URL, "https://") {
		errs = append(errs, fmt.Sprintf("ai.url %q: http(s)://… erwartet", c.AI.URL))
	}
	switch c.Dupes.Mode {
	case "on", "off":
	default:
		errs = append(errs, fmt.Sprintf("dupes.mode %q: on oder off", c.Dupes.Mode))
	}
	switch c.Dupes.Sensitivity {
	case "strict", "normal", "loose":
	default:
		errs = append(errs, fmt.Sprintf("dupes.sensitivity %q: strict, normal oder loose", c.Dupes.Sensitivity))
	}
	switch c.Faces.Mode {
	case "off", "local":
	case "remote":
		if c.Faces.URL == "" {
			errs = append(errs, "faces.url fehlt für faces.mode remote")
		}
	default:
		errs = append(errs, fmt.Sprintf("faces.mode %q: off, remote oder local", c.Faces.Mode))
	}
	if !pathtpl.Supported(c.Library.Rules.Locale) {
		errs = append(errs, fmt.Sprintf("library.rules.locale %q: %s", c.Library.Rules.Locale, strings.Join(pathtpl.LocaleCodes, ", ")))
	}
	for i, r := range c.Library.Rules.List {
		if _, err := pathtpl.Parse(r.Path); err != nil {
			errs = append(errs, fmt.Sprintf("Regel %d (%s): %v", i+1, cmpOr(r.Name, r.Path), err))
		}
	}
	if d := c.Library.Import.Dir; d != "" {
		if !filepath.IsAbs(d) {
			errs = append(errs, "Import-Ordner: Pfad muss absolut sein")
		}
		rd := RealPath(d)
		for _, f := range c.Library.Folders {
			if rf := RealPath(f.Path); inside(d, f.Path) || inside(f.Path, d) || inside(rd, rf) || inside(rf, rd) {
				errs = append(errs, fmt.Sprintf("Import-Ordner und Fotoordner %q dürfen nicht ineinander liegen", f.Path))
			}
		}
		overlaps := func(o string) bool {
			ro := RealPath(o)
			return inside(d, o) || inside(o, d) || inside(rd, ro) || inside(ro, rd)
		}
		if overlaps(c.Cache.Dir) || (c.Backup.Dir != "" && overlaps(c.Backup.Dir)) {
			errs = append(errs, "Import-Ordner darf nicht im Cache oder in der Sicherung liegen")
		}
		if n := c.Library.Import.Folder; n != "" && !slices.ContainsFunc(c.Library.Folders, func(f Folder) bool {
			return f.Name == n || (filepath.IsAbs(ExpandHome(n)) && filepath.Clean(f.Path) == filepath.Clean(ExpandHome(n)))
		}) {
			errs = append(errs, fmt.Sprintf("Import: Fotoordner %q gibt es nicht", n))
		}
	}
	if c.Library.TrashDays < 0 {
		errs = append(errs, "library.trash_days darf nicht negativ sein")
	}
	if c.Library.Rescan != "" && c.Library.Rescan != "0" {
		if _, err := time.ParseDuration(c.Library.Rescan); err != nil {
			errs = append(errs, fmt.Sprintf("library.rescan %q: %v", c.Library.Rescan, err))
		}
	}
	if c.Backup.Dir != "" {
		if !filepath.IsAbs(c.Backup.Dir) {
			errs = append(errs, "Sicherungsordner: Pfad muss absolut sein")
		}
		for _, f := range c.Library.Folders {
			if inside(c.Backup.Dir, f.Path) || inside(f.Path, c.Backup.Dir) {
				errs = append(errs, fmt.Sprintf("Sicherungsordner und Fotoordner %q dürfen nicht ineinander liegen", f.Path))
			}
		}
		if inside(c.Backup.Dir, c.Cache.Dir) || inside(c.Cache.Dir, c.Backup.Dir) {
			errs = append(errs, "Sicherungsordner und Cache dürfen nicht ineinander liegen")
		}
		if c.Backup.Interval != "" && c.Backup.Interval != "0" {
			if _, err := time.ParseDuration(c.Backup.Interval); err != nil {
				errs = append(errs, fmt.Sprintf("backup.interval %q: %v", c.Backup.Interval, err))
			}
		}
	}
	switch c.UI.Language {
	case "auto", "de", "en":
	default:
		errs = append(errs, fmt.Sprintf("ui.language %q: auto, de oder en", c.UI.Language))
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		errs = append(errs, "server.tls_cert und server.tls_key nur zusammen")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// RealPath resolves symbolic links in p, also when p itself does not exist
// yet (its nearest existing parent is resolved).
func RealPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// inside reports whether path is inside (or equal to) dir.
func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// BackupInterval returns the parsed backup interval (0 = on demand).
func (c *Config) BackupInterval() time.Duration {
	d, _ := time.ParseDuration(c.Backup.Interval)
	return d
}

// RescanInterval returns the parsed rescan interval (0 = off).
func (c *Config) RescanInterval() time.Duration {
	d, _ := time.ParseDuration(c.Library.Rescan)
	return d
}

// Path is the configuration file (it may not exist yet).
func (c *Config) Path() string { return c.path }

// SetPath changes where Save writes.
func (c *Config) SetPath(p string) { c.path = ExpandHome(p) }

// Found reports whether the file existed when loaded.
func (c *Config) Found() bool { return c.found }

// Lock marks a key as fixed outside the file.
func (c *Config) Lock(key, origin string) {
	if c.locked == nil {
		c.locked = map[string]string{}
	}
	c.locked[key] = origin
}

// Locked returns where a key was fixed, or "".
func (c *Config) Locked(key string) string { return c.locked[key] }

// LockedKeys returns all locked keys with their origin.
func (c *Config) LockedKeys() map[string]string {
	out := make(map[string]string, len(c.locked))
	for k, v := range c.locked {
		out[k] = v
	}
	return out
}

// Clone returns a deep copy that keeps the path and locks.
func (c *Config) Clone() *Config {
	n := *c
	n.Library.Folders = append([]Folder(nil), c.Library.Folders...)
	n.Library.Exclude = append([]string(nil), c.Library.Exclude...)
	n.Library.Documents = append([]string(nil), c.Library.Documents...)
	n.Library.Rules.List = append([]Rule(nil), c.Library.Rules.List...)
	n.AI.Languages = append([]string(nil), c.AI.Languages...)
	n.locked = c.LockedKeys()
	return &n
}

// NeedsSetup reports whether the wizard has to run: no file and no folders
// from the environment or flags.
func (c *Config) NeedsSetup() bool { return !c.found && len(c.Library.Folders) == 0 }

// Save writes the configuration file (mode 0600: it holds tokens).
func (c *Config) Save() error {
	c.normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString("# Schuhkarton configuration. Written by the setup wizard and the settings page;\n" +
		"# edit by hand while Schuhkarton is stopped. SCHUHKARTON_* variables override it.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	out := *c // folders inside the configuration's folder are written relative to it
	out.Cache.Dir = relToConfig(c.path, c.Cache.Dir)
	out.Faces.ModelsDir = relToConfig(c.path, c.Faces.ModelsDir)
	if err := enc.Encode(&out); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.found = true
	return nil
}

// Masked returns a copy with tokens hidden, for showing.
func (c *Config) Masked() *Config {
	n := c.Clone()
	mask := func(s string) string {
		if s == "" || s == "auto" || s == "off" {
			return s
		}
		return "********"
	}
	n.Server.Token, n.Faces.Token, n.Worker.Token = mask(n.Server.Token), mask(n.Faces.Token), mask(n.Worker.Token)
	n.AI.APIKey = mask(n.AI.APIKey)
	return n
}
