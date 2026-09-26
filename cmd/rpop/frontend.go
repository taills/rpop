package main

import (
	"io/fs"
	"net/http"
	"strings"
)

// frontendHandler serves the single-page admin console from fsys. Paths that do not name a file fall
// back to index.html so client-side routes survive reloads.
func frontendHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			http.Error(w, "rpop frontend is not built; run `npm run build` in web/ and rebuild the binary", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/" {
			rel := strings.TrimPrefix(r.URL.Path, "/")
			if !fs.ValidPath(rel) {
				r.URL.Path = "/"
			} else if info, err := fs.Stat(fsys, rel); err != nil || info.IsDir() {
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}
