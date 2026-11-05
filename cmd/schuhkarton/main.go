// Command schuhkarton serves photo and video folders as a fast, searchable
// web gallery: timeline, memories, map, places, people and albums. One
// binary; face detection runs in a separate container when wanted.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dgrieser/schuhkarton/internal/auth"
	"github.com/dgrieser/schuhkarton/internal/backup"
	"github.com/dgrieser/schuhkarton/internal/config"
	"github.com/dgrieser/schuhkarton/internal/gallery"
	"github.com/dgrieser/schuhkarton/internal/helper"
	"github.com/dgrieser/schuhkarton/internal/schedule"
	"github.com/dgrieser/schuhkarton/internal/server"
	"github.com/dgrieser/schuhkarton/internal/setup"
	"github.com/dgrieser/schuhkarton/internal/version"
	"github.com/dgrieser/schuhkarton/internal/worker"
)

const usage = `schuhkarton – der digitale Schuhkarton: Fotos und Videos im Browser

Aufruf:
  schuhkarton [serve] [Optionen] [ORDNER...]  Galerie starten (Standard). Beim ersten Start
                                              ohne Ordner führt ein Einrichtungslink durch alles.
  schuhkarton index [Optionen] [ORDNER...]    nur einlesen und alle Vorschauen erzeugen, dann beenden
  schuhkarton worker [Optionen]               Gesichtserkennung als eigener Dienst (meist im Container)
  schuhkarton doctor                          Einstellungen, Ordner und Werkzeuge prüfen
  schuhkarton config [path]                   gültige Einstellungen bzw. Pfad der Datei zeigen
  schuhkarton models download [-dir DIR]      Gesichtsmodelle laden (nur ohne Container nötig)
  schuhkarton user list|add|passwd|approve|role|disable|enable|del NAME [admin|user]
                                              Konten verwalten (z.B. "user add Anna admin")
  schuhkarton version

Mit -home DIR (oder SCHUHKARTON_HOME) liegen Einstellungen, Konten und Cache zusammen in DIR
(config.yaml, users.json, cache/): zum Umziehen den Ordner kopieren.

ORDNER als Pfad oder Name=Pfad, z.B. Familie=/mnt/nas/Bilder. Ordner auf der Kommandozeile
ersetzen die aus der Einstellungsdatei. Jede Einstellung geht auch als Umgebungsvariable,
z.B. SCHUHKARTON_SERVER_LISTEN=0.0.0.0:8088 oder SCHUHKARTON_FACES_URL=http://nas:8089.

Optionen:
`

func main() {
	log.SetFlags(log.Ltime)
	defaultMemoryLimit()
	// "schuhkarton convert": the child that decodes and scales one photo for
	// the gallery (gallery/convert.go); not a command for people
	if len(os.Args) == 2 && os.Args[1] == "convert" {
		if err := gallery.RunConvert(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	gallery.ConvertCommand = gallery.ConvertSelfCommand()
	args := takeHome(os.Args[1:])
	cmd := "serve"
	if len(args) > 0 {
		switch args[0] {
		case "serve", "index", "worker", "doctor", "config", "models", "user", "version", "help":
			cmd, args = args[0], args[1:]
		case "-h", "--help", "-help":
			cmd = "help"
		case "-version", "--version":
			cmd = "version"
		}
	}
	var err error
	switch cmd {
	case "version":
		fmt.Println("schuhkarton " + version.Full())
	case "help":
		fmt.Fprint(os.Stderr, usage)
		newFlags("serve").fs.PrintDefaults()
	case "worker":
		err = runWorker(args)
	case "doctor":
		err = runDoctor(args)
	case "config":
		err = runConfig(args)
	case "models":
		err = runModels(args)
	case "user":
		err = runUser(args)
	default:
		err = runServe(cmd, args)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// takeHome reads -home DIR (also --home, -home=DIR) for every command and
// leaves the rest: config, accounts, worker and models all use the folder.
func takeHome(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-home" || a == "--home") && i+1 < len(args):
			os.Setenv("SCHUHKARTON_HOME", args[i+1])
			i++
		case strings.HasPrefix(a, "-home=") || strings.HasPrefix(a, "--home="):
			os.Setenv("SCHUHKARTON_HOME", a[strings.IndexByte(a, '=')+1:])
		default:
			out = append(out, a)
		}
	}
	return out
}

// --- flags --------------------------------------------------------------------

type flagSet struct {
	fs   *flag.FlagSet
	keys map[string]*string // flag name -> value, mapped to config keys
	cfgf *string
	home *string
	web  *string
	open *bool
}

// flag name -> configuration key
var flagKeys = map[string]string{
	"backup": "backup.dir",
	"listen": "server.listen", "token": "server.token", "public-url": "server.public_url",
	"cache": "cache.dir", "exclude": "library.exclude", "rescan": "library.rescan",
	"workers": "media.workers", "exiftool": "media.exiftool", "ffmpeg": "media.ffmpeg",
	"faces": "faces.mode", "faces-url": "faces.url", "faces-token": "faces.token",
	"ai": "ai.mode", "ai-url": "ai.url", "ai-model": "ai.model",
	"python": "faces.python", "models": "faces.models_dir", "previews": "cache.previews",
}

var flagHelp = map[string]string{
	"backup":      "Sicherungsordner: Fotos, Cache und Einstellungen werden im Hintergrund dorthin kopiert",
	"listen":      "Adresse, z.B. 127.0.0.1:8088 oder 0.0.0.0:8088 fürs Heimnetz",
	"token":       "Zugangsschlüssel: auto (nur im Netz), off oder eigener Wert",
	"public-url":  "Adresse für Links, z.B. https://fotos.example.org",
	"cache":       "Cache für Index, Vorschaubilder und Gesichter",
	"exclude":     "auszulassende Datei- und Ordnernamen (Muster, kommagetrennt)",
	"rescan":      "Ordner regelmäßig neu einlesen, z.B. 1h",
	"workers":     "parallele Vorschau-Jobs",
	"exiftool":    "Pfad zu exiftool",
	"ffmpeg":      "Pfad zu ffmpeg",
	"faces":       "Gesichtserkennung: off, remote (Container) oder local (Python hier)",
	"faces-url":   "Adresse des Gesichtserkennungs-Containers, z.B. http://nas:8089",
	"faces-token": "Token des Containers",
	"ai":          "Bildbeschreibungen durch ein KI-Modell: off oder on (Schlüssel über SCHUHKARTON_AI_API_KEY oder ai.api_key_env)",
	"ai-url":      "Adresse der OpenAI-kompatiblen API, z.B. https://llm.aihosting.mittwald.de/v1 oder http://localhost:8000/v1",
	"ai-model":    "Modell für Bildbeschreibungen, z.B. Qwen3.8-27B-NVFP4",
	"python":      "Python mit OpenCV für faces=local",
	"models":      "Ordner mit den Gesichtsmodellen für faces=local",
	"previews":    "große Vorschauen vorab erzeugen (true/false; gut für langsame Netzlaufwerke)",
}

func newFlags(name string) *flagSet {
	f := &flagSet{fs: flag.NewFlagSet("schuhkarton "+name, flag.ExitOnError), keys: map[string]*string{}}
	f.fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		f.fs.PrintDefaults()
	}
	f.cfgf = f.fs.String("config", "", "Einstellungsdatei (Standard: "+config.DefaultConfigPath()+")")
	f.home = f.fs.String("home", "", "Datenordner für Einstellungen, Konten und Cache zusammen (auch SCHUHKARTON_HOME)")
	for name, help := range flagHelp {
		f.keys[name] = f.fs.String(name, "", help)
	}
	f.web = f.fs.String("web", "", "Oberfläche aus diesem Ordner statt eingebaut (Entwicklung)")
	f.open = f.fs.Bool("open", false, "Browser öffnen")
	return f
}

// load reads the configuration and applies flags and folder arguments.
func (f *flagSet) load(args []string) (*config.Config, error) {
	f.fs.Parse(args)
	if *f.home != "" {
		os.Setenv("SCHUHKARTON_HOME", *f.home) // read by config and the user command alike
	}
	cfg, err := config.Load(*f.cfgf)
	if err != nil {
		return nil, err
	}
	var ferr error
	f.fs.Visit(func(fl *flag.Flag) {
		key, ok := flagKeys[fl.Name]
		if !ok {
			return
		}
		if err := config.SetValue(cfg, key, fl.Value.String()); err != nil {
			ferr = fmt.Errorf("-%s: %w", fl.Name, err)
			return
		}
		cfg.Lock(key, "-"+fl.Name)
	})
	if ferr != nil {
		return nil, ferr
	}
	if rest := f.fs.Args(); len(rest) > 0 {
		var folders []config.Folder
		for _, a := range rest {
			name, p, ok := strings.Cut(a, "=")
			if !ok || strings.ContainsAny(name, `/\`) {
				name, p = "", a
			}
			abs, err := filepath.Abs(config.ExpandHome(p))
			if err != nil {
				return nil, err
			}
			folders = append(folders, config.Folder{Name: name, Path: abs})
		}
		cfg.Library.Folders = folders
		cfg.Lock("library.folders", "Kommandozeile")
	}
	cfg.Cache.Dir = config.ExpandHome(cfg.Cache.Dir)
	return cfg, nil
}

// --- serve / index ------------------------------------------------------------------

func libOptions(cfg *config.Config) gallery.Options {
	var roots []gallery.Root
	for _, f := range cfg.Library.Folders {
		roots = append(roots, gallery.Root{Name: f.Name, Path: f.Path})
	}
	python := ""
	if cfg.Faces.Mode == "local" {
		python = helper.Find(cfg.Faces.Python)
	}
	return gallery.Options{
		Roots: roots, CacheDir: cfg.Cache.Dir, Exclude: cfg.Library.Exclude, Workers: cfg.Media.Workers,
		ExifTool: gallery.FindExifTool(cfg.Media.ExifTool), FFmpeg: gallery.FindFFmpeg(cfg.Media.FFmpeg),
		ThumbSize: cfg.Media.ThumbSize, PreviewSize: cfg.Media.PreviewSize, Quality: cfg.Media.JPEGQuality,
		Previews: cfg.Cache.Previews, VideoPreviews: cfg.Media.VideoPreviews, VideoPreviewSize: cfg.Media.VideoPreviewSize,
		Transcode:     cfg.Media.Transcode,
		SlowDecodes:   cfg.Media.SlowDecodes,
		MaxCache:      cfg.Cache.MaxBytes(),
		OverflowDir:   cfg.Cache.OverflowDir,
		IgnoreFolders: !cfg.Library.FolderMeta, WriteFiles: cfg.Library.WriteFiles, WriteMetadata: cfg.Library.WriteMetadata, TrashDays: cfg.Library.TrashDays,
		DocFolders: cfg.Library.Documents,
		Faces: gallery.FaceOptions{
			Mode: cfg.Faces.Mode, URL: cfg.Faces.URL, Token: cfg.Faces.Token, Workers: cfg.Faces.Workers,
			MinScore: cfg.Faces.MinScore, ClusterThreshold: cfg.Faces.ClusterThreshold, NamedThreshold: cfg.Faces.NamedThreshold,
			Python: python, ModelsDir: cfg.Faces.ModelsDir,
		},
		AI:          aiOptions(cfg),
		RetryFailed: cfg.RetryFailed(),
		OCR:         ocrOptions(cfg),
		Speech:      speechOptions(cfg),
		Rules:       ruleSet(cfg),
		Window:      cfg.Window(),
		Import:      importOptions(cfg),
		Dupes: gallery.DupeOptions{Off: cfg.Dupes.Mode == "off", Sensitivity: cfg.Dupes.Sensitivity,
			SimilarWindow: time.Duration(cfg.Dupes.SimilarMinutes) * time.Minute},
	}
}

// importOptions: the import folder and the library folder it fills.
func importOptions(cfg *config.Config) gallery.ImportOptions {
	o := gallery.ImportOptions{Dir: cfg.Library.Import.Dir}
	for i, f := range cfg.Library.Folders {
		if f.Name == cfg.Library.Import.Folder {
			o.Root = i
		}
	}
	return o
}

// ruleSet hands the filing rules to the library.
func ruleSet(cfg *config.Config) gallery.RuleSet {
	rs := gallery.RuleSet{Locale: cfg.Library.Rules.Locale, Auto: cfg.Library.Rules.Auto}
	for _, r := range cfg.Library.Rules.List {
		rs.List = append(rs.List, gallery.Rule{Name: r.Name, When: r.When, Path: r.Path})
	}
	return rs
}

// ocrOptions: text recognition with ai.ocr documents or all, over the AI
// service (also with descriptions off).
func ocrOptions(cfg *config.Config) gallery.OCROptions {
	if cfg.AI.OCR == "off" {
		return gallery.OCROptions{}
	}
	return gallery.OCROptions{URL: cfg.AI.URL, Key: cfg.AI.Key(), Model: cfg.AI.OCRModel, Workers: cfg.AI.OCRWorkers, Scope: cfg.AI.OCR}
}

// speechOptions: speech recognition with ai.speech on, over the AI service.
func speechOptions(cfg *config.Config) gallery.SpeechOptions {
	if cfg.AI.Speech != "on" {
		return gallery.SpeechOptions{}
	}
	return gallery.SpeechOptions{URL: cfg.AI.URL, Key: cfg.AI.Key(), Model: cfg.AI.SpeechModel, Fallback: cfg.AI.SpeechFallback, Workers: cfg.AI.SpeechWorkers}
}

// aiOptions: descriptions only with ai.mode on.
func aiOptions(cfg *config.Config) gallery.AIOptions {
	if cfg.AI.Mode != "on" {
		return gallery.AIOptions{}
	}
	return gallery.AIOptions{URL: cfg.AI.URL, Key: cfg.AI.Key(), Model: cfg.AI.Model, Workers: cfg.AI.Workers,
		Languages: cfg.AI.Languages, ImageSize: cfg.AI.ImageSize, Thinking: cfg.AI.Thinking}
}

func runServe(cmd string, args []string) error {
	f := newFlags(cmd)
	cfg, err := f.load(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cmd == "index" {
		if len(cfg.Library.Folders) == 0 {
			return errors.New("keine Ordner: als Argument angeben oder erst \"schuhkarton\" einrichten")
		}
		lib, err := gallery.Open(libOptions(cfg))
		if err != nil {
			return err
		}
		defer lib.Close()
		if err := lib.Scan(ctx); err != nil {
			return err
		}
		lib.GenerateAll(ctx)
		st := lib.Status()
		log.Printf("fertig: %d Fotos, %d Videos, %d Personen", st.Photos, st.Videos, st.People)
		return nil
	}

	users, err := auth.Open(auth.PathFor(cfg.Path()))
	if err != nil {
		return err
	}
	defer users.Flush()
	r := &runner{base: ctx, webDir: *f.web, sw: &handlerSwitch{}, prog: &server.ProgressBox{}, users: users, setupToken: server.NewToken()}
	r.http = &http.Server{Handler: r.sw, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	if err := r.listen(cfg.Server.Listen, cfg); err != nil {
		return err
	}
	r.cfg = cfg
	go r.dropPastOnce(ctx)
	if cfg.NeedsSetup() {
		token := r.setupToken
		r.sw.Set(server.NewSetup(server.SetupOptions{
			Install: true, Token: token, Users: users, Config: r.config, Listen: r.addr, WebDir: r.webDir, Apply: r.apply, Progress: r.prog,
		}).Handler())
		fmt.Fprintf(os.Stderr, "\nSchuhkarton ist noch nicht eingerichtet. Diesen Link öffnen, um Ordner, Speicher und Gesichtserkennung festzulegen:\n\n")
		for _, u := range pageURLs(r.addr(), cfg.Server.PublicURL) {
			fmt.Fprintf(os.Stderr, "  %s/setup?token=%s\n", u, token)
		}
		fmt.Fprintf(os.Stderr, "\nDie Einstellungen landen in %s\n\n", cfg.Path())
	} else if err := r.start(cfg); err != nil {
		return err
	} else {
		r.printLinks()
		if *f.open {
			if u := pageURLs(r.addr(), cfg.Server.PublicURL); len(u) > 0 {
				exec.Command(openCmd(), u[0]).Start()
			}
		}
	}
	<-ctx.Done()
	log.Printf("beende ...")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.http.Shutdown(sctx)
	r.stop()
	return nil
}

func openCmd() string {
	switch runtime.GOOS {
	case "darwin":
		return "open"
	case "windows":
		return "explorer"
	}
	return "xdg-open"
}

// runner owns the running gallery and swaps it on settings changes.
type runner struct {
	base   context.Context
	webDir string
	sw     *handlerSwitch
	http   *http.Server

	prog  *server.ProgressBox
	users *auth.Store
	// setupToken opens the settings while there is no admin account yet
	setupToken string

	mu     sync.Mutex
	saveMu sync.Mutex // one writer of the settings file
	cfg    *config.Config
	lib    *gallery.Library
	cancel context.CancelFunc
	token  string
	ln     net.Listener
}

// dropPastOnce removes one-time background windows from the settings file
// once they are over (the schedule ignores them anyway; the settings page
// should not list them). No restart: nothing else changes.
func (r *runner) dropPastOnce(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	var failed *config.Config // the settings it could not write: no new try every minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.saveMu.Lock()
		r.mu.Lock()
		cur := r.cfg
		r.mu.Unlock()
		if cur != nil && cur != failed && cur.Path() != "" && cur.Found() {
			next := *cur
			// over now, or already when the file was read (it still has them)
			if next.DropPastOnce(time.Now()) || cur.PrunedOnce() {
				next.ClearPruned()
				if err := next.Save(); err != nil {
					log.Printf("einmalige Zeitfenster entfernen: %v", err)
					failed = cur
				} else {
					r.mu.Lock()
					if r.cfg == cur {
						r.cfg = &next
					}
					r.mu.Unlock()
				}
			}
		}
		r.saveMu.Unlock()
	}
}

func (r *runner) config() *config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

func (r *runner) addr() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln == nil {
		return ""
	}
	return r.ln.Addr().String()
}

// listen (re)binds the server; the old listener closes once the new one runs.
func (r *runner) listen(addr string, cfg *config.Config) error {
	r.mu.Lock()
	old := r.ln
	r.mu.Unlock()
	// same port on another interface (127.0.0.1:8088 -> 0.0.0.0:8088): free it first
	if old != nil && samePort(old.Addr().String(), addr) {
		old.Close()
		old = nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if r.ln != nil && old == nil {
			// back to the previous address
			if back, berr := net.Listen("tcp", r.ln.Addr().String()); berr == nil {
				r.serve(back, cfg)
			}
		}
		return err
	}
	r.mu.Lock()
	r.ln = ln
	r.mu.Unlock()
	r.serve(ln, cfg)
	if old != nil {
		old.Close()
	}
	return nil
}

func (r *runner) serve(ln net.Listener, cfg *config.Config) {
	r.mu.Lock()
	r.ln = ln
	r.mu.Unlock()
	go func() {
		var err error
		if cfg.Server.TLSCert != "" {
			err = r.http.ServeTLS(ln, cfg.Server.TLSCert, cfg.Server.TLSKey)
		} else {
			err = r.http.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("server: %v", err)
		}
	}()
}

func samePort(a, b string) bool {
	_, pa, _ := net.SplitHostPort(a)
	_, pb, _ := net.SplitHostPort(b)
	return pa != "" && pa == pb
}

func (r *runner) start(cfg *config.Config) error {
	lib, err := gallery.Open(libOptions(cfg))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(r.base)
	lib.Start(ctx)
	lib.Go(func() { scanLoop(ctx, lib, cfg.RescanInterval(), cfg.Window().For(schedule.JobScan)) })
	var br *backup.Runner
	if cfg.Backup.Dir != "" {
		var srcs []backup.Source
		for _, f := range cfg.Library.Folders {
			srcs = append(srcs, backup.Source{Name: f.Name, Path: f.Path})
		}
		br = backup.New(backup.Options{Dir: cfg.Backup.Dir, Sources: srcs, Exclude: cfg.Library.Exclude,
			CacheDir: cfg.Cache.Dir, ConfigPath: cfg.Path(), Originals: cfg.Backup.Originals,
			Cache: cfg.Backup.Cache, Config: cfg.Backup.Config, Every: cfg.BackupInterval(), Window: cfg.Window().For(schedule.JobBackup)})
		lib.Go(func() { br.Loop(ctx) })
	}
	token := appToken(cfg, r.addr())
	if a := r.users.FirstAdmin(); a != "" {
		// favorites from before accounts existed belong to the first admin
		if err := lib.ClaimLegacyFavs(a); err != nil {
			log.Printf("favorites: %v", err)
		}
	}
	settings := server.NewSetup(server.SetupOptions{Config: r.config, Listen: r.addr, WebDir: r.webDir, Apply: r.apply,
		AccessLink: r.accessLink, Progress: r.prog, Users: r.users, Token: r.setupToken}).Handler()
	h := server.New(ctx, server.Options{Lib: lib, Config: cfg, Token: token, WebDir: r.webDir, Setup: settings, Backup: br, Users: r.users})
	r.mu.Lock()
	r.cfg, r.lib, r.cancel, r.token = cfg, lib, cancel, token
	r.mu.Unlock()
	r.sw.Set(h)
	return nil
}

func (r *runner) stop() {
	r.mu.Lock()
	lib, cancel := r.lib, r.cancel
	r.lib, r.cancel = nil, nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if lib != nil {
		lib.Close()
	}
}

// apply saves new settings and restarts the gallery with them.
func (r *runner) apply(ctx context.Context, p server.Plan, progress func(server.Progress)) (string, error) {
	cfg := p.Config
	progress(server.Progress{Phase: "Einstellungen speichern"})
	r.saveMu.Lock()
	err := cfg.Save()
	if err == nil {
		// the new settings at once: dropPastOnce must not write the old
		// ones back while the gallery restarts
		r.mu.Lock()
		r.cfg = cfg
		r.mu.Unlock()
	}
	r.saveMu.Unlock()
	if err != nil {
		return "", err
	}
	r.sw.Set(restarting{})
	r.stop()
	if p.Import != "" {
		src := config.ExpandHome(p.Import)
		if filepath.Clean(src) != filepath.Clean(cfg.Cache.Dir) {
			progress(server.Progress{Phase: "Cache übernehmen"})
			err := gallery.ImportCache(src, cfg.Cache.Dir, func(done, total int) {
				progress(server.Progress{Phase: "Cache übernehmen", Done: done, Total: total})
			})
			if err != nil {
				log.Printf("cache import: %v", err)
			}
		}
	}
	old := r.addr()
	if cfg.Server.Listen != r.cfgListen() && cfg.Locked("server.listen") == "" {
		if err := r.listen(cfg.Server.Listen, cfg); err != nil {
			log.Printf("neue Adresse %s: %v (bleibe bei %s)", cfg.Server.Listen, err, old)
		}
	}
	progress(server.Progress{Phase: "Galerie starten"})
	if err := r.start(cfg); err != nil {
		r.sw.Set(failed{err})
		return "", err
	}
	r.printLinks()
	link := "/"
	if t := r.token; t != "" {
		link = "/?t=" + t
	}
	if cfg.Server.Listen != "" && r.addr() != old {
		if u := pageURLs(r.addr(), cfg.Server.PublicURL); len(u) > 0 {
			link = u[0] + link
		}
	}
	return link, nil
}

func (r *runner) cfgListen() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln == nil {
		return ""
	}
	return r.ln.Addr().String()
}

// accessLink is the first gallery link, with the token when one is needed.
func (r *runner) accessLink() string {
	r.mu.Lock()
	cfg, token := r.cfg, r.token
	r.mu.Unlock()
	u := pageURLs(r.addr(), cfg.Server.PublicURL)
	if len(u) == 0 {
		return ""
	}
	if token != "" {
		return u[0] + "/?t=" + token
	}
	return u[0] + "/"
}

func (r *runner) printLinks() {
	r.mu.Lock()
	cfg, token := r.cfg, r.token
	r.mu.Unlock()
	q := ""
	if token != "" {
		q = "/?t=" + token
	}
	for _, u := range pageURLs(r.addr(), cfg.Server.PublicURL) {
		log.Printf("Schuhkarton: %s%s", u, q)
	}
	log.Printf("Einstellungen: %s", cfg.Path())
	if !r.users.HasAdmin() {
		log.Printf("Noch kein Admin-Konto. Anlegen unter (oder mit \"schuhkarton user add NAME admin\"):")
		for _, u := range pageURLs(r.addr(), cfg.Server.PublicURL) {
			log.Printf("  %s/setup?token=%s", u, r.setupToken)
		}
	}
}

// scanLoop reads the folders at the start and then regularly, inside the
// background window. A scan that has begun is not cut short.
func scanLoop(ctx context.Context, lib *gallery.Library, every time.Duration, w schedule.Window) {
	if err := lib.Scan(ctx); err != nil && ctx.Err() == nil {
		log.Printf("scan: %v", err)
	}
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, cancel, ok := w.Wait(ctx)
			if !ok {
				return
			}
			cancel()
			select { // ticks while waiting for the window: one scan is enough
			case <-t.C:
			default:
			}
			if err := lib.Scan(ctx); err != nil && ctx.Err() == nil {
				log.Printf("scan: %v", err)
			}
		}
	}
}

// appToken resolves server.token: "off", "auto" (a stored token, only when
// reachable from the network) or the value itself.
func appToken(cfg *config.Config, addr string) string {
	switch cfg.Server.Token {
	case "off", "":
		return ""
	case "auto":
		host, _, _ := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return ""
		}
		p := filepath.Join(cfg.Cache.Dir, "token")
		if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 12 {
			return strings.TrimSpace(string(b))
		}
		b := make([]byte, 12)
		rand.Read(b)
		t := hex.EncodeToString(b)
		os.MkdirAll(cfg.Cache.Dir, 0o755)
		os.WriteFile(p, []byte(t+"\n"), 0o600)
		return t
	}
	return cfg.Server.Token
}

func pageURLs(addr, public string) []string {
	if public != "" {
		return []string{strings.TrimRight(public, "/")}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && !ip.IsUnspecified() {
		return []string{"http://" + net.JoinHostPort(host, port)}
	}
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		out = append(out, "http://"+net.JoinHostPort(ipn.IP.String(), port))
	}
	return append(out, "http://"+net.JoinHostPort("127.0.0.1", port))
}

// handlerSwitch serves whatever is current: the wizard, the gallery or a
// short "restarting" page.
type handlerSwitch struct{ h atomic.Value }

type holder struct{ h http.Handler }

func (s *handlerSwitch) Set(h http.Handler) { s.h.Store(holder{h}) }

func (s *handlerSwitch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.h.Load().(holder); ok && v.h != nil {
		v.h.ServeHTTP(w, r)
		return
	}
	restarting{}.ServeHTTP(w, r)
}

type restarting struct{}

func (restarting) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, "Schuhkarton startet neu", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Retry-After", "2")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="2"><title>Schuhkarton</title>`+
		`<body style="font:16px system-ui;padding:2rem;background:#111;color:#eee"><p>Schuhkarton startet neu …</p>`)
}

type failed struct{ err error }

func (f failed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Schuhkarton konnte nicht starten: "+f.err.Error(), http.StatusInternalServerError)
}

// --- worker -------------------------------------------------------------------

func runWorker(args []string) error {
	fs := flag.NewFlagSet("schuhkarton worker", flag.ExitOnError)
	cfgPath := fs.String("config", "", "Einstellungsdatei")
	listen := fs.String("listen", "", "Adresse (Standard 0.0.0.0:8089)")
	token := fs.String("token", "", "Token, den die Galerie mitschicken muss (leer: ohne Schutz)")
	python := fs.String("python", "", "Python mit OpenCV, Pillow, pillow_heif")
	models := fs.String("models", "", "Ordner mit den Gesichtsmodellen")
	workers := fs.Int("workers", 0, "parallele Prozesse")
	fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	w := cfg.Worker
	if *listen != "" {
		w.Listen = *listen
	}
	if *token != "" {
		w.Token = *token
	}
	if *workers > 0 {
		w.Workers = *workers
	}
	py := helper.Find(firstNonEmpty(*python, cfg.Faces.Python))
	if py == "" {
		return errors.New("kein Python gefunden; der Gesichtserkennungs-Container bringt alles mit (siehe README)")
	}
	dirs := []string{firstNonEmpty(*models, cfg.Faces.ModelsDir), filepath.Join(cfg.Cache.Dir, "models"), "/models"}
	if w.Token == "" {
		log.Printf("Achtung: ohne Token kann jeder im Netz Bilder zur Erkennung schicken (-token oder SCHUHKARTON_WORKER_TOKEN)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := worker.New(worker.Options{Listen: w.Listen, Token: w.Token, Python: py, Models: dirs,
		Workers: w.Workers, MinScore: cfg.Faces.MinScore})
	return srv.Run(ctx)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// --- doctor, config, models -------------------------------------------------------

func runDoctor(args []string) error {
	f := newFlags("doctor")
	cfg, err := f.load(args)
	if err != nil {
		return err
	}
	fmt.Printf("schuhkarton %s\n\nEinstellungen: %s", version.Full(), cfg.Path())
	if !cfg.Found() {
		fmt.Print(" (noch nicht vorhanden)")
	}
	fmt.Println()
	if err := cfg.Validate(); err != nil {
		fmt.Printf("  Fehler: %v\n", err)
	}
	fmt.Println("\nOrdner:")
	if len(cfg.Library.Folders) == 0 {
		fmt.Println("  keine – beim nächsten Start führt der Einrichtungslink durch alles")
	}
	for _, fo := range cfg.Library.Folders {
		c := setup.CheckPhotos(fo.Path, 5*time.Second)
		more := ""
		if c.Truncated {
			more = "+"
		}
		fmt.Printf("  %-14s %s: %d Fotos, %d Videos%s %s\n", fo.Name, fo.Path, c.Photos, c.Videos, more, c.Problem)
	}
	c := setup.CheckCache(cfg.Cache.Dir)
	ci := gallery.InspectCache(cfg.Cache.Dir)
	fmt.Printf("\nCache: %s – %s frei, %d Einträge im Index, %d Vorschaudateien %s\n", cfg.Cache.Dir, human(c.FreeBytes), ci.Items, ci.Files, c.Problem)
	fmt.Println("\nWerkzeuge:")
	for _, t := range setup.Tools(cfg.Media.ExifTool, cfg.Media.FFmpeg) {
		if t.Path == "" {
			fmt.Printf("  %-9s FEHLT – nötig für %s\n", t.Name, t.Needed)
		} else {
			fmt.Printf("  %-9s %s (%s)\n", t.Name, t.Path, t.Version)
		}
	}
	fmt.Printf("\nGesichtserkennung: %s\n", cfg.Faces.Mode)
	switch cfg.Faces.Mode {
	case "remote":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		wi, err := gallery.ProbeWorker(ctx, cfg.Faces.URL, cfg.Faces.Token)
		if err != nil {
			fmt.Printf("  %s nicht erreichbar: %v\n", cfg.Faces.URL, err)
		} else {
			fmt.Printf("  %s: Erkennung %v, Zuordnung %v, %s %s\n", cfg.Faces.URL, wi.Detection, wi.Recognition, wi.Version, strings.Join(wi.Missing, ", "))
		}
	case "local":
		p := helper.New(helper.Options{Python: helper.Find(cfg.Faces.Python), ScriptDir: os.TempDir(),
			Models: []string{cfg.Faces.ModelsDir, filepath.Join(cfg.Cache.Dir, "models")}, Size: 1, MinScore: cfg.Faces.MinScore})
		i := p.Probe()
		p.Close()
		fmt.Printf("  Python %s: Erkennung %v, Zuordnung %v %s %s\n", i.Python, i.Detection, i.Recognition, strings.Join(i.Missing, ", "), i.Error)
	}
	fmt.Printf("\nBildbeschreibungen: %s\n", cfg.AI.Mode)
	if cfg.AI.Used() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		key := "kein Schlüssel"
		if cfg.AI.Key() != "" {
			key = "Schlüssel gesetzt"
		} else if cfg.AI.APIKeyEnv != "" {
			key = "$" + cfg.AI.APIKeyEnv + " ist leer"
		}
		t := gallery.TestAI(ctx, aiOptions(cfg), false)
		if t.Error != "" {
			fmt.Printf("  %s (%s): %s\n", cfg.AI.URL, key, t.Error)
		} else {
			has := map[bool]string{true: "vorhanden", false: "NICHT in der Liste"}
			fmt.Printf("  %s (%s): %d Modelle", cfg.AI.URL, key, len(t.Models))
			if cfg.AI.Mode == "on" {
				fmt.Printf(", Beschreibungen %s %s", cfg.AI.Model, has[slices.Contains(t.Models, cfg.AI.Model)])
			}
			if cfg.AI.OCR != "off" {
				fmt.Printf(", Texterkennung %s %s", cfg.AI.OCRModel, has[slices.Contains(t.Models, cfg.AI.OCRModel)])
			}
			if cfg.AI.Speech == "on" {
				fmt.Printf(", Spracherkennung %s %s", cfg.AI.SpeechModel, has[slices.Contains(t.Models, cfg.AI.SpeechModel)])
				if cfg.AI.SpeechFallback != "" {
					fmt.Printf(", zweiter Versuch %s %s", cfg.AI.SpeechFallback, has[slices.Contains(t.Models, cfg.AI.SpeechFallback)])
				}
			}
			fmt.Println()
		}
	}
	return nil
}

func human(b uint64) string {
	switch {
	case b == 0:
		return "?"
	case b > 1<<40:
		return fmt.Sprintf("%.1f TB", float64(b)/(1<<40))
	case b > 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MB", b>>20)
}

func runConfig(args []string) error {
	f := newFlags("config")
	rest := args
	if len(args) > 0 && args[0] == "path" {
		rest = args[1:]
	}
	cfg, err := f.load(rest)
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "path" {
		fmt.Println(cfg.Path())
		return nil
	}
	fmt.Printf("# %s", cfg.Path())
	if !cfg.Found() {
		fmt.Print(" (nicht vorhanden: Standardwerte)")
	}
	fmt.Println()
	for k, v := range cfg.LockedKeys() {
		fmt.Printf("# %s festgelegt durch %s\n", k, v)
	}
	b, err := yaml.Marshal(cfg.Masked())
	if err != nil {
		return err
	}
	os.Stdout.Write(b)
	return nil
}

func runModels(args []string) error {
	if len(args) == 0 || args[0] != "download" {
		return errors.New("Aufruf: schuhkarton models download [-dir DIR]")
	}
	fs := flag.NewFlagSet("schuhkarton models download", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(config.DefaultCacheDir(), "models"), "Zielordner")
	fs.Parse(args[1:])
	return setup.DownloadModels(context.Background(), *dir, func(s string) { log.Print(s) })
}
