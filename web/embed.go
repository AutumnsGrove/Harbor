// Package web embeds the built SvelteKit frontend. The build directory is
// produced by `make web`, CI and the Docker frontend stage; only its .gitkeep is
// tracked, so the source and the embedded output cannot drift (design.md §3).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var embedded embed.FS

// Assets is the frontend root (the contents of web/build).
func Assets() fs.FS {
	assets, err := fs.Sub(embedded, "build")
	if err != nil {
		// The embed directive guarantees this subdirectory exists; a failure
		// here is a broken build, not a runtime condition.
		panic("web: embedded build directory is missing: " + err.Error())
	}
	return assets
}
