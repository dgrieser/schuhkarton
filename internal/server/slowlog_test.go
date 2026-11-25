package server

import "testing"

func TestRouteOf(t *testing.T) {
	for in, want := range map[string]string{
		"/api/faces/quiz":           "/api/faces/quiz",
		"/api/face/0a1b2c3d4e5f/2":  "/api/face",
		"/api/admin/dupes/resolve":  "/api/admin/dupes/resolve",
		"/api/people/n-ida-schr":    "/api/people",
		"/t/0a1b2c3d4e5f/s.jpg":     "/t",
		"/":                         "/",
		"/api/faces/0a1b2c/approve": "/api/faces",
	} {
		if got := routeOf(in); got != want {
			t.Errorf("routeOf(%q) = %q, want %q", in, got, want)
		}
	}
}
