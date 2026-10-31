// Package web holds the embedded single-page UI.
package web

import "embed"

// FS contains the gallery (index.html, app.js) and the setup page (setup.html, setup.js).
//
//go:embed index.html app.js app.css setup.html setup.js
var FS embed.FS
