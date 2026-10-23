package webembed

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var files embed.FS

func Handler() http.Handler {
	content, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(content))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.Path, "\\") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimSuffix(r.URL.Path, "/")
		spa := path == "" || path == "/sites" || path == "/rules" || path == "/history" || path == "/exceptions" || path == "/settings" || path == "/aggregation" || strings.HasPrefix(path, "/history/") || strings.HasPrefix(path, "/downloaders/") || strings.HasPrefix(path, "/rules/")
		if spa && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			static.ServeHTTP(w, clone)
			return
		}
		static.ServeHTTP(w, r)
	})
}
