package handler

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// NewStaticHandler creates a handler for serving static files from web/dist
func NewStaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get the web/dist directory path
		webDistPath := filepath.Join("web", "dist")

		// Clean the URL path
		urlPath := path.Clean(r.URL.Path)
		if urlPath == "/" || urlPath == "." {
			urlPath = "/index.html"
		}
		urlPath = strings.TrimPrefix(urlPath, "/")

		// Build full file path
		filePath := filepath.Join(webDistPath, urlPath)

		// Try to open the file
		file, err := os.Open(filePath)
		if err != nil {
			// File not found, try index.html for SPA routing
			filePath = filepath.Join(webDistPath, "index.html")
			file, err = os.Open(filePath)
			if err != nil {
				// index.html also doesn't exist - frontend not built
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("Frontend not built yet. Run 'task web-build' to build the frontend."))
				return
			}
		}
		defer file.Close()

		// Get file info for modification time
		stat, err := file.Stat()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Serve the file
		http.ServeContent(w, r, filepath.Base(filePath), stat.ModTime(), file)
	})
}
