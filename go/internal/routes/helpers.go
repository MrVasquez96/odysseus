package routes

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, msg string, code int) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// effectiveUser returns the real human behind the request.
// For API tokens, returns the token owner (so API clients see the same data
// as the owner's browser session). Falls back to currentUser.
func effectiveUser(r *http.Request) string {
	if isAPI, _ := r.Context().Value(CtxKeyAPIToken).(bool); isAPI {
		if owner, _ := r.Context().Value(CtxKeyAPITokenOwner).(string); owner != "" {
			return owner
		}
	}
	return currentUser(r)
}
