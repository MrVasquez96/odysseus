package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// SPA routes that all serve index.html (from app.py lines 772-816).
var spaRoutes = map[string]bool{
	"/":            true,
	"/notes":       true,
	"/calendar":    true,
	"/cookbook":     true,
	"/email":       true,
	"/memory":      true,
	"/gallery":     true,
	"/tasks":       true,
	"/library":     true,
}

// Special HTML routes (not index.html).
var specialHTMLRoutes = map[string]string{
	"/login":       "login.html",
	"/backgrounds": "backgrounds.html",
}

// NewStaticHandler creates an http.Handler for embedded static files.
// It serves files from the embedded FS with Cache-Control: no-cache on
// .js/.css/.html files (matching Python's _RevalidatingStatic).
func NewStaticHandler(staticFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(staticFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip /static/ prefix for the embedded FS lookup.
		urlPath := strings.TrimPrefix(r.URL.Path, "/static")
		if urlPath == "" {
			urlPath = "/"
		}

		// Force revalidation on source files (no build step, no versioned URLs).
		ext := path.Ext(urlPath)
		if ext == ".js" || ext == ".css" || ext == ".html" {
			w.Header().Set("Cache-Control", "no-cache")
		}

		// Rewrite the URL path for the file server.
		r.URL.Path = urlPath
		fileServer.ServeHTTP(w, r)
	})
}

// NewSPAHandler creates a handler for SPA and special HTML routes.
// It reads the HTML from the embedded FS, injects the CSP nonce, and returns it.
func NewSPAHandler(staticFS fs.FS) http.Handler {
	// Pre-read HTML files at startup.
	indexHTML := mustReadFile(staticFS, "index.html")
	loginHTML := mustReadFile(staticFS, "login.html")
	backgroundsHTML := mustReadFile(staticFS, "backgrounds.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var html []byte

		if spaRoutes[r.URL.Path] {
			html = indexHTML
		} else if filename, ok := specialHTMLRoutes[r.URL.Path]; ok {
			switch filename {
			case "login.html":
				html = loginHTML
			case "backgrounds.html":
				html = backgroundsHTML
			}
		}

		if html == nil {
			http.NotFound(w, r)
			return
		}

		// Inject CSP nonce into {{CSP_NONCE}} placeholders.
		nonce := CSPNonce(r.Context())
		output := strings.ReplaceAll(string(html), "{{CSP_NONCE}}", nonce)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(output))
	})
}

func mustReadFile(fsys fs.FS, name string) []byte {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		// Not fatal at startup — the file might not exist in dev mode.
		// Return empty so the handler returns 404 at request time.
		return nil
	}
	return data
}
