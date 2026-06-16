package routes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"odysseus/internal/auth"
)

// presetRoutes manages JSON-file-backed presets (data/presets.json).
type presetRoutes struct {
	mu      sync.Mutex
	path    string
	mgr     *auth.Manager
	presets map[string]any
}

var defaultPresets = map[string]any{
	"code_analyze": map[string]any{
		"name":          "Code Analyze",
		"temperature":   0.2,
		"max_tokens":    8000,
		"system_prompt": "You are a code analyzer.\nANALYSIS FORMAT:\n- Issues: [specific problems found]\n- Security: [vulnerabilities if any]\n- Performance: [optimization opportunities]\n- Fix: [concrete solutions with code examples]\n\nStart directly with findings. No preamble. If input isn't code, state: \"Input is not code. Please provide code to analyze.\"",
	},
	"brainstorm": map[string]any{
		"name":          "Brainstorm",
		"temperature":   0.9,
		"max_tokens":    4096,
		"system_prompt": "You are a creative ideation assistant focused on divergent thinking.\n\nGenerate diverse, unexpected ideas that span from practical to experimental.\n- Mix conventional and unconventional approaches\n- Connect unrelated concepts to spark innovation\n- Consider multiple perspectives and contexts\n- Include both immediate solutions and long-term possibilities\n- Challenge assumptions without being absurd for absurdity's sake\n\nStructure ideas clearly but allow creative freedom in presentation. Aim for quantity and variety over filtering.",
	},
	"reason": map[string]any{
		"name":          "Reason",
		"temperature":   0.3,
		"max_tokens":    6000,
		"system_prompt": "You are a systematic reasoning assistant.\n\nStructure all responses using clear logical progression:\n1. Identify key components of the question\n2. State relevant principles or facts\n3. Build argument step by step\n4. Address potential counterarguments\n5. Conclude with justified answer\n\nUse precise language. Show causal relationships explicitly. Quantify uncertainty where applicable.",
	},
	"custom": map[string]any{
		"name":          "Custom",
		"temperature":   1.0,
		"max_tokens":    0,
		"system_prompt": "",
		"inject_prefix": "",
		"inject_suffix": "",
		"enabled":       false,
	},
}

// PresetRoutes registers preset CRUD routes.
// POST /api/presets/expand is NOT registered — it stays proxied to Python (uses LLM).
func PresetRoutes(mux *http.ServeMux, dataDir string, mgr *auth.Manager) {
	r := &presetRoutes{
		path: filepath.Join(dataDir, "presets.json"),
		mgr:  mgr,
	}
	r.presets = r.load()

	mux.HandleFunc("GET /api/presets", r.getPresets)
	mux.HandleFunc("POST /api/presets/custom", r.updateCustom)
	mux.HandleFunc("GET /api/presets/templates", r.getTemplates)
	mux.HandleFunc("POST /api/presets/templates", r.saveTemplate)
	mux.HandleFunc("DELETE /api/presets/templates/{id}", r.deleteTemplate)
	mux.HandleFunc("GET /api/presets/groups", r.getGroups)
	mux.HandleFunc("POST /api/presets/groups", r.saveGroups)
}

func (r *presetRoutes) requireAdmin(req *http.Request, w http.ResponseWriter) bool {
	user := currentUser(req)
	if user == "internal-tool" {
		return true
	}
	if r.mgr != nil && r.mgr.IsConfigured() {
		if user == "" || !r.mgr.IsAdmin(user) {
			jsonError(w, "Admin access required", http.StatusForbidden)
			return false
		}
	}
	return true
}

// load reads presets from disk, healing missing defaults.
func (r *presetRoutes) load() map[string]any {
	data, err := os.ReadFile(r.path)
	if err != nil {
		r.save(defaultPresets)
		return copyMap(defaultPresets)
	}
	var presets map[string]any
	if err := json.Unmarshal(data, &presets); err != nil {
		return copyMap(defaultPresets)
	}
	// Heal missing built-in presets.
	healed := false
	for k, v := range defaultPresets {
		if _, ok := presets[k]; !ok {
			presets[k] = v
			healed = true
		}
	}
	if healed {
		r.save(presets)
	}
	return presets
}

func (r *presetRoutes) save(presets map[string]any) bool {
	data, err := json.MarshalIndent(presets, "", "  ")
	if err != nil {
		return false
	}
	tmp := fmt.Sprintf("%s.tmp.%d", r.path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return false
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
		return false
	}
	r.presets = presets
	return true
}

func (r *presetRoutes) getPresets(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	writeJSON(w, http.StatusOK, r.presets)
}

func (r *presetRoutes) updateCustom(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	var body struct {
		Temperature  float64 `json:"temperature"`
		MaxTokens    int     `json:"max_tokens"`
		SystemPrompt string  `json:"system_prompt"`
		Name         string  `json:"name"`
		Enabled      bool    `json:"enabled"`
		InjectPrefix string  `json:"inject_prefix"`
		InjectSuffix string  `json:"inject_suffix"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	name := body.Name
	if name == "" {
		name = "Custom"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.presets["custom"] = map[string]any{
		"name":           name,
		"character_name": name,
		"temperature":    body.Temperature,
		"max_tokens":     body.MaxTokens,
		"system_prompt":  body.SystemPrompt,
		"inject_prefix":  body.InjectPrefix,
		"inject_suffix":  body.InjectSuffix,
		"enabled":        body.Enabled,
	}
	if r.save(r.presets) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Custom preset updated"})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "message": "Failed to save preset"})
	}
}

func (r *presetRoutes) getTemplates(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	templates, _ := r.presets["user_templates"].([]any)
	if templates == nil {
		templates = []any{}
	}
	writeJSON(w, http.StatusOK, templates)
}

func (r *presetRoutes) saveTemplate(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	var body struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		SystemPrompt string  `json:"system_prompt"`
		Temperature  float64 `json:"temperature"`
		MaxTokens    int     `json:"max_tokens"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		jsonError(w, "Name is required", http.StatusBadRequest)
		return
	}
	if body.ID == "" {
		b := make([]byte, 4)
		rand.Read(b)
		body.ID = "user-" + hex.EncodeToString(b)
	}
	template := map[string]any{
		"id":            body.ID,
		"name":          body.Name,
		"system_prompt": body.SystemPrompt,
		"temperature":   body.Temperature,
		"max_tokens":    body.MaxTokens,
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	templates, _ := r.presets["user_templates"].([]any)
	// Update existing or append.
	found := false
	for i, t := range templates {
		if m, ok := t.(map[string]any); ok && m["id"] == body.ID {
			templates[i] = template
			found = true
			break
		}
	}
	if !found {
		templates = append(templates, template)
	}
	r.presets["user_templates"] = templates
	if r.save(r.presets) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "template": template})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "message": "Failed to save template"})
	}
}

func (r *presetRoutes) deleteTemplate(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	templateID := req.PathValue("id")

	r.mu.Lock()
	defer r.mu.Unlock()
	templates, _ := r.presets["user_templates"].([]any)
	var filtered []any
	for _, t := range templates {
		if m, ok := t.(map[string]any); ok && m["id"] == templateID {
			continue
		}
		filtered = append(filtered, t)
	}
	r.presets["user_templates"] = filtered
	if r.save(r.presets) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "message": "Failed to delete template"})
	}
}

func (r *presetRoutes) getGroups(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	groups, _ := r.presets["group_presets"].([]any)
	if groups == nil {
		groups = []any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (r *presetRoutes) saveGroups(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	var body struct {
		Groups []any `json:"groups"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.presets["group_presets"] = body.Groups
	r.save(r.presets)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func copyMap(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	json.Unmarshal(data, &out)
	return out
}
