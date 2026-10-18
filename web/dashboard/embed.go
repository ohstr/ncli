// Package dashboard embeds the relay's web pages: the admin dashboard and
// the public homepage. Build them with `just ui` before `go build`; without
// them the admin listener says so and the homepage falls back to plain HTML.
package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

//go:embed all:dist-home
var distHome embed.FS

// FS returns the built dashboard, rooted at its index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is always embedded
	}
	return sub
}

// HomeFS returns the built homepage, rooted at its home.html.
func HomeFS() fs.FS {
	sub, err := fs.Sub(distHome, "dist-home")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether the dashboard was built into this binary.
func Built() bool {
	_, err := fs.Stat(FS(), "index.html")
	return err == nil
}
