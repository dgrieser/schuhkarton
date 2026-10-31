// Package web holds the embedded single-page UI.
package web

import "embed"

// FS contains index.html, app.js and app.css.
//
//go:embed index.html app.js app.css
var FS embed.FS
