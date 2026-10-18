// Package dashboard embeds the relay dashboard's built files. Build them with
// `just ui` before `go build`; without them the admin listener serves a page
// saying so, and its API and /metrics work as usual.
package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built dashboard, rooted at its index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is always embedded
	}
	return sub
}

// Built reports whether the dashboard was built into this binary.
func Built() bool {
	_, err := fs.Stat(FS(), "index.html")
	return err == nil
}
