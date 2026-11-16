package server

import "net/http"

// contentSecurityPolicy: scripts only from the gallery itself (no inline
// code, nothing from other sites), so text that reached a page unescaped
// could not run. Styles may be inline (the pages set sizes and positions
// that way), images come from the gallery and the map tiles of any site.
// Nobody else may put the gallery into a frame.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https: http:; media-src 'self' blob:; connect-src 'self'; font-src 'self' data:; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

// Headers adds the security headers to every answer: of the gallery, the
// setup page and the pages in between.
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
