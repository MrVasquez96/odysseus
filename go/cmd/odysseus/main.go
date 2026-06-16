// Odysseus-Go — the Go frontend server for Odysseus.
//
// Phase 1: transparent reverse proxy to the Python backend.
// Serves embedded static files and proxies all API requests.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"odysseus/internal/config"
	"odysseus/internal/db"
	"odysseus/internal/proxy"
	"odysseus/internal/server"
)

// Embed the entire static/ directory.
// The symlink go/cmd/odysseus/static -> ../../../static must exist for this.
//
//go:embed static
var embeddedStatic embed.FS

func main() {
	// Structured logging.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Load configuration from environment variables.
	cfg := config.Load()

	slog.Info("starting Odysseus-Go",
		"listen", cfg.ListenAddr,
		"python", cfg.PythonAddr,
		"timeout", cfg.HardTimeout,
	)

	// Create the embedded static filesystem (strip the "static" prefix).
	staticFS, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		slog.Error("failed to create static filesystem", "err", err)
		os.Exit(1)
	}

	// Open SQLite database (WAL mode for concurrent Go+Python access).
	database := db.MustOpen(cfg.DataDir)
	defer database.Close()

	// Initialize auth (must come before proxy so we have the internal token).
	authCfg := server.LoadAuthConfig(cfg.DataDir)
	authCfg.Database = database
	if authCfg.Enabled {
		slog.Info("auth middleware enabled",
			"configured", authCfg.Manager.IsConfigured(),
			"localhost_bypass", authCfg.LocalhostBypass,
		)
	} else {
		slog.Info("auth middleware disabled (AUTH_ENABLED=false)")
	}

	// Create the reverse proxy to the Python backend.
	// Pass the internal token so proxied requests are trusted by Python.
	apiProxy := proxy.New(cfg.PythonAddr, authCfg.InternalToken)

	// Build the HTTP handler stack.
	hardTimeout := time.Duration(cfg.HardTimeout * float64(time.Second))
	handler := server.New(staticFS, apiProxy, hardTimeout, &authCfg, database, cfg.DataDir)

	// Create and start the HTTP server.
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout — SSE streams are long-lived.
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal.
	<-ctx.Done()
	slog.Info("shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}

	slog.Info("server stopped")
}
