package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// uiHandler serves the embedded UI under /ui/*. In Docker we usually run a
// separate nginx container that serves the same files; this is a fallback so
// the single binary can be used standalone (no Docker, no nginx) for dev.
func uiHandler() http.HandlerFunc {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// embed always succeeds; the only way err != nil is build-time
		// misconfiguration. Panic so we notice immediately.
		panic(err)
	}
	fileServer := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
	return func(w http.ResponseWriter, r *http.Request) {
		fileServer.ServeHTTP(w, r)
	}
}
