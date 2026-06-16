package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// prefRoutes manages per-user preferences (data/user_prefs.json).
type prefRoutes struct {
	mu   sync.Mutex
	path string
}

// PrefsRoutes registers user preference routes.
func PrefsRoutes(mux *http.ServeMux, dataDir string) {
	r := &prefRoutes{
		path: filepath.Join(dataDir, "user_prefs.json"),
	}
	mux.HandleFunc("GET /api/prefs", r.getAll)
	mux.HandleFunc("GET /api/prefs/{key}", r.getOne)
	mux.HandleFunc("PUT /api/prefs/{key}", r.setOne)
}

func (r *prefRoutes) loadAll() map[string]any {
	data, err := os.ReadFile(r.path)
	if err != nil {
		return map[string]any{}
	}
	var all map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		return map[string]any{}
	}
	return all
}

func (r *prefRoutes) saveAll(all map[string]any) {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp.%d", r.path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
	}
}

// loadForUser returns prefs for a specific user. Handles both legacy flat
// format and multi-user {"_users": {"alice": {...}}} format.
func (r *prefRoutes) loadForUser(user string) map[string]any {
	all := r.loadAll()
	users, hasUsers := all["_users"].(map[string]any)
	if hasUsers {
		if user == "" {
			// Auth disabled — return first user's prefs for backward compat.
			for _, v := range users {
				if m, ok := v.(map[string]any); ok {
					return m
				}
			}
			return map[string]any{}
		}
		if m, ok := users[user].(map[string]any); ok {
			return m
		}
		return map[string]any{}
	}
	// Legacy flat format.
	return all
}

// saveForUser persists prefs for a specific user.
func (r *prefRoutes) saveForUser(user string, prefs map[string]any) {
	all := r.loadAll()
	if user == "" {
		// Auth disabled. If already multi-user, write into the same slot.
		if users, ok := all["_users"].(map[string]any); ok {
			for k := range users {
				users[k] = prefs
				r.saveAll(all)
				return
			}
		}
		r.saveAll(prefs)
		return
	}
	users, ok := all["_users"].(map[string]any)
	if !ok {
		users = map[string]any{}
	}
	users[user] = prefs
	all["_users"] = users
	r.saveAll(all)
}

func (r *prefRoutes) getAll(w http.ResponseWriter, req *http.Request) {
	user := effectiveUser(req)
	r.mu.Lock()
	prefs := r.loadForUser(user)
	r.mu.Unlock()
	writeJSON(w, http.StatusOK, prefs)
}

func (r *prefRoutes) getOne(w http.ResponseWriter, req *http.Request) {
	user := effectiveUser(req)
	key := req.PathValue("key")
	r.mu.Lock()
	prefs := r.loadForUser(user)
	r.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": prefs[key]})
}

func (r *prefRoutes) setOne(w http.ResponseWriter, req *http.Request) {
	user := effectiveUser(req)
	key := req.PathValue("key")
	var body struct {
		Value any `json:"value"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	prefs := r.loadForUser(user)
	prefs[key] = body.Value
	r.saveForUser(user, prefs)
	r.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": prefs[key]})
}
