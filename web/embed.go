// Package web embeds the built admin console so the rpop binary is self-contained.
package web

import (
	"embed"
	"io/fs"
)

// dist holds the Vite build output. Run `npm run build` in web/ before `go build`; without it only the
// committed placeholder is embedded and the console reports that the frontend is not built.
//
//go:embed all:dist
var dist embed.FS

// Dist returns the embedded frontend rooted at the build directory.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is a fixed, valid path; this cannot fail.
	}
	return sub
}
