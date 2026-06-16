// Package config loads Odysseus configuration from environment variables.
//
// The env-var contract is documented in go/testdata/env_contract.json.
// Phase 1 reads only the variables the Go proxy itself needs; all others
// pass through to the Python backend unchanged.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds the Go binary's runtime configuration.
type Config struct {
	// ListenAddr is the address the Go server binds to (APP_BIND:APP_PORT).
	ListenAddr string

	// PythonAddr is the Python ML worker's internal URL.
	PythonAddr string

	// HardTimeout is the request timeout in seconds for non-SSE endpoints.
	HardTimeout float64

	// AllowedOrigins is the list of allowed CORS origins.
	AllowedOrigins []string

	// DataDir is the root directory for persistent data.
	DataDir string
}

// Load reads configuration from environment variables, applying Odysseus defaults.
func Load() Config {
	bind := env("APP_BIND", "0.0.0.0")
	port := env("APP_PORT", "7000")

	timeout, _ := strconv.ParseFloat(env("REQUEST_HARD_TIMEOUT", "45"), 64)
	if timeout <= 0 {
		timeout = 45
	}

	origins := strings.Split(env("ALLOWED_ORIGINS", "http://localhost,http://127.0.0.1"), ",")
	for i := range origins {
		origins[i] = strings.TrimSpace(origins[i])
	}

	return Config{
		ListenAddr:     fmt.Sprintf("%s:%s", bind, port),
		PythonAddr:     env("ODYSSEUS_PYTHON_ADDR", "http://127.0.0.1:7001"),
		HardTimeout:    timeout,
		AllowedOrigins: origins,
		DataDir:        env("ODYSSEUS_DATA_DIR", "./data"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
