package routes

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"odysseus/internal/auth"
	"odysseus/internal/db"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	maxTokenNameLen = 100
	defaultScopes   = "chat"
)

var allowedScopes = map[string]bool{
	"chat": true, "todos:read": true, "todos:write": true,
	"documents:read": true, "documents:write": true,
	"email:read": true, "email:draft": true, "email:send": true,
	"calendar:read": true, "calendar:write": true,
	"memory:read": true, "memory:write": true,
	"cookbook:read": true, "cookbook:launch": true,
}

var tokenProfiles = map[string][]string{
	"chat":               {"chat"},
	"codex_todos":        {"todos:read", "todos:write"},
	"codex_email_drafts": {"email:read", "email:draft", "documents:read", "documents:write"},
}

// writeDeps pairs like {write, read} — if write scope is present, ensure read is too.
var writeDeps = [][2]string{
	{"todos:write", "todos:read"},
	{"documents:write", "documents:read"},
	{"calendar:write", "calendar:read"},
	{"memory:write", "memory:read"},
	{"email:draft", "email:read"},
	{"cookbook:launch", "cookbook:read"},
}

type tokenRoutes struct {
	db  *db.DB
	mgr *auth.Manager
}

// TokenRoutes registers API token CRUD routes.
func TokenRoutes(mux *http.ServeMux, database *db.DB, mgr *auth.Manager) {
	r := &tokenRoutes{db: database, mgr: mgr}

	mux.HandleFunc("GET /api/tokens", r.listTokens)
	mux.HandleFunc("GET /api/tokens/profiles", r.tokenProfiles)
	mux.HandleFunc("POST /api/tokens", r.createToken)
	mux.HandleFunc("PATCH /api/tokens/{id}", r.updateToken)
	mux.HandleFunc("DELETE /api/tokens/{id}", r.deleteToken)
}

func (r *tokenRoutes) requireAdmin(req *http.Request, w http.ResponseWriter) bool {
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

func (r *tokenRoutes) listTokens(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	tokens, err := r.db.ListAllTokens()
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	result := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		scopes := splitScopes(t.Scopes)
		result = append(result, map[string]any{
			"id":           t.ID,
			"name":         t.Name,
			"owner":        db.StringVal(t.Owner),
			"token_prefix": t.TokenPrefix,
			"scopes":       scopes,
			"is_active":    t.IsActive,
			"last_used_at": nullTimeStr(t.LastUsedAt),
			"created_at":   nullTimeStr(t.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (r *tokenRoutes) tokenProfiles(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	sortedScopes := make([]string, 0, len(allowedScopes))
	for s := range allowedScopes {
		sortedScopes = append(sortedScopes, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles":       tokenProfiles,
		"allowed_scopes": sortedScopes,
	})
}

func (r *tokenRoutes) createToken(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Scopes  any    `json:"scopes"`
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if len(name) > maxTokenNameLen {
		name = name[:maxTokenNameLen]
	}
	if name == "" {
		jsonError(w, "Token name is required", http.StatusBadRequest)
		return
	}
	owner := currentUser(req)

	scopeList, err := normalizeScopes(body.Scopes, body.Profile)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	scopesValue := strings.Join(scopeList, ",")

	// Generate raw token.
	raw := make([]byte, 32)
	rand.Read(raw)
	rawToken := "ody_" + base64.URLEncoding.EncodeToString(raw)

	hash, _ := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	tokenID := uuid.New().String()[:8]

	if err := r.db.CreateToken(tokenID, name, owner, string(hash), rawToken[:8], scopesValue); err != nil {
		jsonError(w, "Failed to create token", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":           tokenID,
		"name":         name,
		"owner":        owner,
		"token":        rawToken,
		"token_prefix": rawToken[:8],
		"scopes":       scopeList,
	})
}

func (r *tokenRoutes) updateToken(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	tokenID := req.PathValue("id")
	curUser := currentUser(req)

	token, err := r.db.GetToken(tokenID)
	if err != nil || token == nil {
		jsonError(w, "Token not found", http.StatusNotFound)
		return
	}
	if curUser != "" && db.StringVal(token.Owner) != curUser {
		jsonError(w, "Not your token", http.StatusForbidden)
		return
	}

	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		body = map[string]any{}
	}

	if name, ok := body["name"].(string); ok && strings.TrimSpace(name) != "" {
		n := strings.TrimSpace(name)
		if len(n) > maxTokenNameLen {
			n = n[:maxTokenNameLen]
		}
		r.db.UpdateTokenName(tokenID, n)
		token.Name = n
	}
	if _, ok := body["scopes"]; ok {
		scopeList, err := normalizeScopes(body["scopes"], "")
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		scopesStr := strings.Join(scopeList, ",")
		r.db.UpdateTokenScopes(tokenID, scopesStr)
		token.Scopes = scopesStr
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":           tokenID,
		"name":         token.Name,
		"owner":        db.StringVal(token.Owner),
		"token_prefix": token.TokenPrefix,
		"scopes":       splitScopes(token.Scopes),
	})
}

func (r *tokenRoutes) deleteToken(w http.ResponseWriter, req *http.Request) {
	if !r.requireAdmin(req, w) {
		return
	}
	tokenID := req.PathValue("id")
	curUser := currentUser(req)

	token, err := r.db.GetToken(tokenID)
	if err != nil || token == nil {
		jsonError(w, "Token not found", http.StatusNotFound)
		return
	}
	if curUser != "" && db.StringVal(token.Owner) != curUser {
		jsonError(w, "Not your token", http.StatusForbidden)
		return
	}
	r.db.DeleteToken(tokenID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// --- helpers ---

func normalizeScopes(scopes any, profile string) ([]string, error) {
	profile = strings.TrimSpace(profile)
	var requested []string
	if profile != "" {
		p, ok := tokenProfiles[profile]
		if !ok {
			return nil, &scopeError{"Unknown token profile"}
		}
		requested = append(requested, p...)
	} else {
		switch v := scopes.(type) {
		case []any:
			for _, s := range v {
				if str, ok := s.(string); ok && strings.TrimSpace(str) != "" {
					requested = append(requested, strings.TrimSpace(str))
				}
			}
		case string:
			for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
				if s != "" {
					requested = append(requested, s)
				}
			}
		}
		if len(requested) == 0 {
			requested = []string{defaultScopes}
		}
	}

	// Validate and deduplicate.
	normalized := make([]string, 0, len(requested))
	seen := map[string]bool{}
	for _, s := range requested {
		if !allowedScopes[s] {
			return nil, &scopeError{"Unknown token scope: " + s}
		}
		if !seen[s] {
			normalized = append(normalized, s)
			seen[s] = true
		}
	}

	// Ensure read deps for write scopes.
	for _, dep := range writeDeps {
		writeScope, readScope := dep[0], dep[1]
		if seen[writeScope] && !seen[readScope] {
			// Insert read scope before write scope.
			idx := -1
			for i, s := range normalized {
				if s == writeScope {
					idx = i
					break
				}
			}
			if idx >= 0 {
				normalized = append(normalized[:idx+1], normalized[idx:]...)
				normalized[idx] = readScope
			}
		}
	}
	if len(normalized) == 0 {
		normalized = []string{defaultScopes}
	}
	return normalized, nil
}

type scopeError struct{ msg string }

func (e *scopeError) Error() string { return e.msg }

func splitScopes(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return []string{defaultScopes}
	}
	return result
}

func nullTimeStr(ns sql.NullString) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return &ns.String
}
