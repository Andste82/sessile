// Package web embeds the built frontend for single-binary distribution.
//
// The build pipeline copies frontend/dist into ./dist (see the Makefile
// build target) before compiling. A committed placeholder keeps the embed
// directive valid even before the frontend has been built.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the embedded frontend filesystem rooted at the dist directory.
func Dist() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}
