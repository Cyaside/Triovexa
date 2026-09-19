package http

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets/*
var uiAssetFiles embed.FS

var uiAssetHandler = newUIAssetHandler()

func uiAppAvailable() bool {
	_, err := uiAssetFiles.ReadFile("assets/app/index.html")
	return err == nil
}

func serveUIApp(w http.ResponseWriter, r *http.Request) {
	body, err := uiAssetFiles.ReadFile("assets/app/index.html")
	if err != nil {
		http.Error(w, "operator console has not been built", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

func newUIAssetHandler() http.Handler {
	assetFS, err := fs.Sub(uiAssetFiles, "assets")
	if err != nil {
		panic(err)
	}

	fileServer := http.StripPrefix("/ui/assets/", http.FileServer(http.FS(assetFS)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fileServer.ServeHTTP(w, r)
	})
}
