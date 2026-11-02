// Package web embeds the built React frontend so the backend ships as one binary.
//
// `npm run build` in frontend/ writes into dist/ here. Without a build, only the
// placeholder is embedded and the server explains how to build the UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded frontend rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether a real frontend build was embedded.
func Built() bool {
	_, err := fs.Stat(FS(), "index.html")
	return err == nil
}
