// Package web holds the embedded single-page UI.
package web

import "embed"

// FS contains the gallery (index.html, app.js, admin.js), the login page,
// the setup page, the translations (i18n.js) and the libraries of others
// (vendor: Leaflet for the map), the logo and the icons.
//
//go:embed *.html *.js *.css *.svg *.png *.ico vendor
var FS embed.FS
