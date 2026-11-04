// Package web embeds the compiled frontend assets.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// Assets returns files relative to the frontend build directory.
func Assets() fs.FS {
	result, _ := fs.Sub(assets, "dist")
	return result
}
