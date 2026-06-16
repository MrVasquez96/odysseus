package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"odysseus/internal/auth"
)

const appVersion = "1.0.0"

type settingsRoutes struct {
	mu      sync.Mutex
	dataDir string
	mgr     *auth.Manager
}

// SettingsRoutes registers version, health, and settings routes.
func SettingsRoutes(mux *http.ServeMux, dataDir string, mgr *auth.Manager) {
	r := &settingsRoutes{dataDir: dataDir, mgr: mgr}
	mux.HandleFunc("GET /api/version", r.version)
	mux.HandleFunc("GET /api/health", r.health)
	mux.HandleFunc("GET /api/auth/settings", r.getSettings)
	mux.HandleFunc("POST /api/auth/settings", r.setSettings)
}

func (r *settingsRoutes) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": appVersion})
}

func (r *settingsRoutes) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
	})
}

func (r *settingsRoutes) loadSettings() map[string]any {
	data, err := os.ReadFile(filepath.Join(r.dataDir, "settings.json"))
	if err != nil {
		return map[string]any{}
	}
	var s map[string]any
	if json.Unmarshal(data, &s) != nil {
		return map[string]any{}
	}
	return s
}

func (r *settingsRoutes) saveSettings(s map[string]any) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.dataDir, "settings.json")
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (r *settingsRoutes) getSettings(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	settings := r.loadSettings()
	if user != "" && r.mgr != nil && r.mgr.IsAdmin(user) {
		writeJSON(w, http.StatusOK, settings)
		return
	}
	// Non-admin: scrub secret keys.
	writeJSON(w, http.StatusOK, scrubSettings(settings))
}

func (r *settingsRoutes) setSettings(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	if user == "" || r.mgr == nil || !r.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}

	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.loadSettings()
	for k, v := range body {
		current[k] = v
	}
	if err := r.saveSettings(current); err != nil {
		jsonError(w, "Failed to save settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// scrubSettings removes secret-looking values from the settings map.
func scrubSettings(settings map[string]any) map[string]any {
	result := make(map[string]any, len(settings))
	for k, v := range settings {
		result[k] = scrubValue(k, v)
	}
	return result
}

func scrubValue(key string, value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = scrubValue(k, val)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = scrubValue(key, item)
		}
		return out
	case string:
		if isSecretKey(key) && v != "" {
			return ""
		}
		return v
	default:
		return v
	}
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, pattern := range []string{"key", "secret", "token", "password", "credential", "auth"} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}
