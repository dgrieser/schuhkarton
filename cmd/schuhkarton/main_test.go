package main

import (
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/dgrieser/schuhkarton/internal/config"
)

// Moving from 127.0.0.1:P to 0.0.0.0:P must not fail with "address in use".
func TestListenSamePortOtherInterface(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	r := &runner{http: &http.Server{Handler: http.NotFoundHandler()}}
	cfg := config.Defaults()
	if err := r.listen(fmt.Sprintf("127.0.0.1:%d", port), cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.listen(fmt.Sprintf("0.0.0.0:%d", port), cfg); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := r.addr(); got != fmt.Sprintf("0.0.0.0:%d", port) && got != fmt.Sprintf("[::]:%d", port) {
		t.Errorf("listening on %s", got)
	}
	r.http.Close()
}

// With a certificate the printed links start with https.
func TestPageURLsScheme(t *testing.T) {
	if u := pageURLs("127.0.0.1:8088", "", true); len(u) != 1 || u[0] != "https://127.0.0.1:8088" {
		t.Errorf("https: %v", u)
	}
	if u := pageURLs("127.0.0.1:8088", "", false); len(u) != 1 || u[0] != "http://127.0.0.1:8088" {
		t.Errorf("http: %v", u)
	}
	if u := pageURLs("127.0.0.1:8088", "https://example.org/fotos/", true); len(u) != 1 || u[0] != "https://example.org/fotos" {
		t.Errorf("public: %v", u)
	}
}
