// Package web holds the embedded single-page UI.
package web

import "embed"

// FS contains the gallery (index.html, app.js, admin.js), the login page,
// the setup page and the translations (i18n.js).
//
//go:embed *.html *.js *.css
var FS embed.FS
