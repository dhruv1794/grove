// Package web holds the compiled grove web UI (a React SPA) embedded into the
// binary. The source app lives under this directory; `make web` builds it to
// dist/, which is committed and embedded here — so `go build` never needs a
// JavaScript toolchain and the single-binary property is preserved.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// FS returns the built SPA assets rooted at dist/ (so paths are "index.html",
// "assets/…", not "dist/index.html").
func FS() (fs.FS, error) {
	return fs.Sub(assets, "dist")
}
