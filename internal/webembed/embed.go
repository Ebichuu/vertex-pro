package webembed

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var files embed.FS

func Handler() http.Handler {
	content, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(content))
}
