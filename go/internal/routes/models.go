package routes

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"odysseus/internal/auth"
	"odysseus/internal/db"
	"odysseus/internal/llm"
)

type modelRoutes struct {
	db      *db.DB
	mgr     *auth.Manager
	dataDir string
	prefs   *prefRoutes
}

// ModelRoutes registers model-related read routes served by Go.
// Model create/update/delete/probe/toggle stay proxied to Python.
func ModelRoutes(mux *http.ServeMux, database *db.DB, mgr *auth.Manager, dataDir string) {
	r := &modelRoutes{
		db:      database,
		mgr:     mgr,
		dataDir: dataDir,
		prefs:   &prefRoutes{path: filepath.Join(dataDir, "user_prefs.json")},
	}
	mux.HandleFunc("GET /api/models/default-chat", r.defaultChat)
	mux.HandleFunc("GET /api/models", r.listModels)
}

// loadSettings reads data/settings.json.
func (r *modelRoutes) loadSettings() map[string]any {
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

// defaultChat resolves the default model + endpoint for the current user.
// Matches Python's GET /api/models/default-chat logic.
func (r *modelRoutes) defaultChat(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	isAdmin := user != "" && r.mgr != nil && r.mgr.IsAdmin(user)

	var epID, model string
	var fallbacks []any

	if user != "" && !isAdmin && r.prefs != nil {
		// Regular users: resolve from per-user prefs only.
		userPrefs := r.prefs.loadForUser(user)
		epID, _ = userPrefs["default_endpoint_id"].(string)
		model, _ = userPrefs["default_model"].(string)
		if fb, ok := userPrefs["default_model_fallbacks"].([]any); ok {
			fallbacks = fb
		}
	} else {
		// Admin or unauthenticated: use global settings.
		settings := r.loadSettings()
		epID, _ = settings["default_endpoint_id"].(string)
		model, _ = settings["default_model"].(string)
		if fb, ok := settings["default_model_fallbacks"].([]any); ok {
			fallbacks = fb
		}
	}
	epID = strings.TrimSpace(epID)
	model = strings.TrimSpace(model)

	// Try the configured endpoint.
	if epID != "" {
		ep, found, err := r.db.GetEnabledEndpoint(epID, user, isAdmin)
		if err == nil && found {
			r.resolveDefaultChat(w, ep, model)
			return
		}
	}

	// Try fallback chain.
	for _, entry := range fallbacks {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		fID, _ := entryMap["endpoint_id"].(string)
		fID = strings.TrimSpace(fID)
		if fID == "" {
			continue
		}
		ep, found, err := r.db.GetEnabledEndpoint(fID, user, isAdmin)
		if err == nil && found {
			fModel, _ := entryMap["model"].(string)
			r.resolveDefaultChat(w, ep, strings.TrimSpace(fModel))
			return
		}
	}

	// Last resort: first enabled endpoint for this user.
	ep, found, err := r.db.FirstEnabledEndpoint(user, isAdmin)
	if err != nil || !found {
		writeJSON(w, http.StatusOK, map[string]any{
			"endpoint_id":  "",
			"endpoint_url": "",
			"model":        "",
		})
		return
	}
	r.resolveDefaultChat(w, ep, "")
}

func (r *modelRoutes) resolveDefaultChat(w http.ResponseWriter, ep db.ModelEndpointRow, model string) {
	chatURL := llm.NormalizeChatURL(ep.BaseURL)

	// If no model specified, pick first visible model from cached/pinned.
	if model == "" {
		visible := visibleModels(
			db.ParseModelList(ep.CachedModels),
			db.ParseModelList(ep.HiddenModels),
			db.ParseModelList(ep.PinnedModels),
		)
		if len(visible) > 0 {
			model = visible[0]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"endpoint_id":  ep.ID,
		"endpoint_url": chatURL,
		"model":        model,
	})
}

// listModels returns the model list for the model picker.
// Matches Python's GET /api/models response shape.
func (r *modelRoutes) listModels(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	isAdmin := user != "" && r.mgr != nil && r.mgr.IsAdmin(user)

	endpoints, err := r.db.ListEnabledEndpoints(user, isAdmin)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	items := make([]map[string]any, 0, len(endpoints))
	for _, ep := range endpoints {
		base := strings.TrimRight(ep.BaseURL, "/")
		chatURL := llm.NormalizeChatURL(base)
		epModelType := db.StringVal(ep.ModelType)
		if epModelType == "" {
			epModelType = "llm"
		}
		kind := db.StringVal(ep.EndpointKind)
		if kind == "" {
			kind = "auto"
		}
		category := classifyEndpoint(base, kind)

		visible := visibleModels(
			db.ParseModelList(ep.CachedModels),
			db.ParseModelList(ep.HiddenModels),
			db.ParseModelList(ep.PinnedModels),
		)

		item := map[string]any{
			"host":           "custom",
			"port":           0,
			"url":            chatURL,
			"endpoint_id":    ep.ID,
			"endpoint_name":  ep.Name,
			"category":       category,
			"endpoint_kind":  kind,
			"model_type":     epModelType,
		}

		if len(visible) > 0 {
			pinned := db.ParseModelList(ep.PinnedModels)
			curated, extra := curateModels(visible, pinned)
			item["models"] = curated
			item["models_display"] = displayNames(curated)
			item["models_extra"] = extra
			item["models_extra_display"] = displayNames(extra)
		} else {
			item["models"] = []string{}
			item["models_display"] = []string{}
			item["models_extra"] = []string{}
			item["models_extra_display"] = []string{}
			item["offline"] = true
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hosts": []any{},
		"items": items,
	})
}

// ─── Model list helpers (matching Python logic) ──────────────────────

// visibleModels merges cached + pinned models and removes hidden ones.
func visibleModels(cached, hidden, pinned []string) []string {
	hiddenSet := make(map[string]bool, len(hidden))
	for _, m := range hidden {
		hiddenSet[m] = true
	}

	seen := make(map[string]bool)
	var result []string

	// Cached models first (minus hidden).
	for _, m := range cached {
		if hiddenSet[m] || seen[m] {
			continue
		}
		seen[m] = true
		result = append(result, m)
	}
	// Pinned models always included (even if hidden).
	for _, m := range pinned {
		if seen[m] {
			continue
		}
		seen[m] = true
		result = append(result, m)
	}
	return result
}

// curateModels splits models into curated (primary) and extra lists.
// Pinned models are always in the curated list.
func curateModels(visible, pinned []string) (curated []string, extra []string) {
	pinnedSet := make(map[string]bool, len(pinned))
	for _, m := range pinned {
		pinnedSet[m] = true
	}

	for _, m := range visible {
		if pinnedSet[m] || isChatModel(m) {
			curated = append(curated, m)
		} else {
			extra = append(extra, m)
		}
	}
	// Ensure pinned models are in curated even if not in visible.
	for _, m := range pinned {
		found := false
		for _, c := range curated {
			if c == m {
				found = true
				break
			}
		}
		if !found {
			curated = append(curated, m)
		}
	}
	return
}

// isChatModel returns true if the model ID looks like a chat model.
func isChatModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	// Skip known non-chat patterns.
	for _, skip := range []string{"embed", "whisper", "tts", "dall-e", "moderation", "rerank"} {
		if strings.Contains(lower, skip) {
			return false
		}
	}
	return true
}

// classifyEndpoint determines if an endpoint is local, api, or proxy.
func classifyEndpoint(baseURL, kind string) string {
	if kind != "" && kind != "auto" {
		return kind
	}
	lower := strings.ToLower(baseURL)
	if strings.Contains(lower, "localhost") || strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "0.0.0.0") {
		return "local"
	}
	for _, cloud := range []string{"openai.com", "anthropic.com", "openrouter.ai", "groq.com", "deepseek.com", "mistral.ai"} {
		if strings.Contains(lower, cloud) {
			return "api"
		}
	}
	return "local"
}

// displayNames returns shortened display names for model IDs.
func displayNames(models []string) []string {
	names := make([]string, len(models))
	for i, m := range models {
		if idx := strings.LastIndex(m, "/"); idx >= 0 {
			names[i] = m[idx+1:]
		} else {
			names[i] = m
		}
	}
	return names
}
