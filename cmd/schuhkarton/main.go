// Command schuhkarton serves photo and video folders as a fast, searchable web
// gallery with timeline, map, places, people and albums.
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
	"strings"
	"syscall"
	"time"

	"github.com/dgrieser/schuhkarton/internal/gallery"
	"github.com/dgrieser/schuhkarton/internal/server"
)

type rootsFlag []gallery.Root

func (r *rootsFlag) String() string { return fmt.Sprint(*r) }
func (r *rootsFlag) Set(v string) error {
	name, p, ok := strings.Cut(v, "=")
	if !ok {
		name, p = "", v
	}
	*r = append(*r, gallery.Root{Name: name, Path: p})
	return nil
}

const usage = `schuhkarton - der digitale Schuhkarton: Fotos und Videos im Browser

Aufruf:
  schuhkarton [serve] [Optionen] ORDNER...   Schuhkarton öffnen (Standard)
  schuhkarton index   [Optionen] ORDNER...   nur einlesen und alle Vorschauen erzeugen, dann beenden

ORDNER kann als Name=Pfad angegeben werden, z.B. Familie=/mnt/nas/Bilder.

Optionen:
`

func main() {
	log.SetFlags(log.Ltime)
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && (args[0] == "serve" || args[0] == "index") {
		cmd, args = args[0], args[1:]
	}
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("schuhkarton", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
	}
	var roots rootsFlag
	fs.Var(&roots, "root", "Ordner (Name=Pfad), mehrfach möglich")
	listen := fs.String("listen", "127.0.0.1:8088", "Adresse; 0.0.0.0:8088 für das ganze Heimnetz")
	cache := fs.String("cache", filepath.Join(home, ".cache/schuhkarton"), "Cache für Index, Vorschaubilder und Gesichter")
	exclude := fs.String("exclude", "_Duplikate,.*,@eaDir,#recycle,$RECYCLE.BIN,System Volume Information,*.tmp",
		"auszulassende Datei- und Ordnernamen (Muster, kommagetrennt)")
	workers := fs.Int("workers", max(2, runtime.NumCPU()/2), "parallele Vorschau-Jobs")
	exiftool := fs.String("exiftool", "", "Pfad zu exiftool (sonst automatisch)")
	python := fs.String("python", "", "Python mit OpenCV, Pillow, pillow_heif (sonst automatisch)")
	models := fs.String("models", "", "Ordner mit Gesichtsmodellen (YuNet, SFace)")
	noFaces := fs.Bool("no-faces", false, "keine Gesichtserkennung")
	previews := fs.Bool("previews", false, "große Vorschauen für alle Fotos vorab erzeugen (gut für langsame Netzlaufwerke)")
	token := fs.String("token", "auto", "Zugangsschlüssel: auto (nur im Netz), off oder eigener Wert")
	rescan := fs.Duration("rescan", 0, "Ordner regelmäßig neu einlesen, z.B. 30m (0 = nur beim Start)")
	open := fs.Bool("open", false, "Browser öffnen")
	webDir := fs.String("web", "", "Oberfläche aus diesem Ordner statt eingebaut (Entwicklung)")
	fs.Parse(args)
	for _, a := range fs.Args() {
		roots.Set(a)
	}
	if len(roots) == 0 {
		fs.Usage()
		os.Exit(2)
	}
	seen := map[string]bool{}
	for i := range roots {
		p, err := filepath.Abs(expandHome(roots[i].Path, home))
		if err != nil {
			log.Fatal(err)
		}
		roots[i].Path = p
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			log.Fatalf("kein Ordner: %s", p)
		}
		if roots[i].Name == "" {
			roots[i].Name = filepath.Base(p)
		}
		for n := 2; seen[roots[i].Name]; n++ {
			roots[i].Name = fmt.Sprintf("%s-%d", filepath.Base(p), n)
		}
		seen[roots[i].Name] = true
	}
	var excl []string
	for _, e := range strings.Split(*exclude, ",") {
		if e = strings.TrimSpace(e); e != "" {
			excl = append(excl, e)
		}
	}
	opt := gallery.Options{
		Roots: roots, CacheDir: expandHome(*cache, home), Exclude: excl, Workers: *workers,
		ExifTool: gallery.FindExifTool(*exiftool), Python: gallery.FindPython(*python),
		ModelsDir: *models, Faces: !*noFaces, Previews: *previews,
	}
	lib, err := gallery.Open(opt)
	if err != nil {
		log.Fatal(err)
	}
	defer lib.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cmd == "index" {
		if err := lib.Scan(ctx); err != nil {
			log.Fatal(err)
		}
		lib.GenerateAll(ctx)
		st := lib.Status()
		log.Printf("fertig: %d Fotos, %d Videos, %d Personen", st.Photos, st.Videos, st.People)
		return
	}

	tok := *token
	host, _, _ := net.SplitHostPort(*listen)
	switch tok {
	case "off":
		tok = ""
	case "auto":
		tok = ""
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			tok = loadToken(opt.CacheDir)
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Handler: server.New(ctx, lib, tok, *webDir), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	urls := pageURLs(ln.Addr().(*net.TCPAddr), host, tok)
	for _, u := range urls {
		log.Printf("Schuhkarton: %s", u)
	}
	if *open && len(urls) > 0 {
		exec.Command("xdg-open", urls[0]).Start()
	}
	lib.Start(ctx)
	go func() {
		if err := lib.Scan(ctx); err != nil && ctx.Err() == nil {
			log.Printf("scan: %v", err)
		}
		if *rescan <= 0 {
			return
		}
		t := time.NewTicker(*rescan)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := lib.Scan(ctx); err != nil && ctx.Err() == nil {
					log.Printf("scan: %v", err)
				}
			}
		}
	}()
	<-ctx.Done()
	log.Printf("beende ...")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func loadToken(dir string) string {
	p := filepath.Join(dir, "token")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 12 {
		return strings.TrimSpace(string(b))
	}
	b := make([]byte, 12)
	rand.Read(b)
	t := hex.EncodeToString(b)
	os.WriteFile(p, []byte(t+"\n"), 0o600)
	return t
}

func pageURLs(addr *net.TCPAddr, host, tok string) []string {
	q := ""
	if tok != "" {
		q = "/?t=" + tok
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return []string{fmt.Sprintf("http://%s%s", net.JoinHostPort(host, fmt.Sprint(addr.Port)), q)}
	}
	var out []string
	ifaces, _ := net.InterfaceAddrs()
	for _, a := range ifaces {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		out = append(out, fmt.Sprintf("http://%s:%d%s", ipn.IP, addr.Port, q))
	}
	return append(out, fmt.Sprintf("http://127.0.0.1:%d%s", addr.Port, q))
}
