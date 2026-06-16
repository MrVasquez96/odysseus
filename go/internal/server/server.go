// Package server provides the Odysseus Go HTTP server.
//
// It serves embedded static files, SPA routes with CSP nonce injection,
// and proxies all API requests to the Python backend.
package server

import (
	"io/fs"
	"net/http"
	"strings"
	"time"

	"odysseus/internal/auth"
	"odysseus/internal/db"
	"odysseus/internal/routes"
)

// New creates the HTTP handler stack for the Odysseus Go server.
//
// The handler hierarchy:
//  1. SecurityHeaders (outermost — adds CSP, X-Frame-Options, etc.)
//  2. Gzip (compress non-SSE responses >= 1024 bytes)
//  3. Auth (cookie/bearer/internal-token validation)
//  4. RequestTimeout (abort non-exempt requests after hardTimeout)
//  5. Router (SPA routes, static files, auth routes, API proxy)
func New(staticFS fs.FS, apiProxy http.Handler, hardTimeout time.Duration, authCfg *AuthConfig, database *db.DB, dataDir string) http.Handler {
	mux := http.NewServeMux()

	spaHandler := NewSPAHandler(staticFS)

	// Register auth routes if auth is enabled.
	var mgr *auth.Manager
	if authCfg != nil && authCfg.Manager != nil {
		mgr = authCfg.Manager
		routes.AuthRoutes(mux, mgr)
	}

	// Register CRUD routes that Go handles directly (Phase 3+).
	if database != nil {
		routes.SessionRoutes(mux, database)
		routes.NoteRoutes(mux, database)
		routes.TokenRoutes(mux, database, mgr)
		routes.TaskRoutes(mux, database)
		routes.EmailRoutes(mux, database)
		routes.CalendarRoutes(mux, database)
	}

	// Register file-backed routes (presets, preferences).
	routes.PresetRoutes(mux, dataDir, mgr)
	routes.PrefsRoutes(mux, dataDir)

	// Register model + settings routes (Phase 5).
	if database != nil {
		routes.ModelRoutes(mux, database, mgr, dataDir)
	}
	routes.SettingsRoutes(mux, dataDir, mgr)

	// Register explicit SPA routes (except "/" which is the catch-all).
	for route := range spaRoutes {
		if route == "/" {
			continue
		}
		mux.Handle(route, spaHandler)
	}
	for route := range specialHTMLRoutes {
		mux.Handle(route, spaHandler)
	}

	// Static files — /static/* served from embedded FS.
	mux.Handle("/static/", NewStaticHandler(staticFS))

	// "/" is the catch-all: serve SPA for exact "/" or known SPA paths,
	// proxy everything else (API routes, etc.) to Python.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Exact "/" → serve SPA (index.html).
		if path == "/" {
			spaHandler.ServeHTTP(w, r)
			return
		}

		// Known SPA or special HTML routes that weren't caught above
		// (shouldn't happen, but safe fallback).
		if spaRoutes[path] || specialHTMLRoutes[path] != "" {
			spaHandler.ServeHTTP(w, r)
			return
		}

		// Static file requests that somehow miss /static/ prefix.
		if strings.HasPrefix(path, "/static/") {
			NewStaticHandler(staticFS).ServeHTTP(w, r)
			return
		}

		// Everything else → proxy to Python.
		apiProxy.ServeHTTP(w, r)
	})

	// Middleware stack: outermost first.
	var handler http.Handler = mux
	handler = TimeoutMiddleware(hardTimeout, handler)
	// Auth middleware sits between timeout and gzip so auth failures
	// are fast (no decompression/timeout overhead) but protected routes
	// still get timeout enforcement.
	if authCfg != nil {
		handler = AuthMiddleware(*authCfg, handler)
	}
	handler = GzipMiddleware(handler)
	handler = SecurityHeadersMiddleware(handler)

	return handler
}
