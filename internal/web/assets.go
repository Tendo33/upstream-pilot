package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist/* dist/assets/*
var assets embed.FS

func SPAHandler() http.HandlerFunc {
	content, _ := fs.Sub(assets, "dist")
	server := http.FileServer(http.FS(content))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested != "." && requested != "" {
			if file, err := content.Open(requested); err == nil {
				_ = file.Close()
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				server.ServeHTTP(w, r)
				return
			}
		}
		r.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-cache")
		server.ServeHTTP(w, r)
	}
}
