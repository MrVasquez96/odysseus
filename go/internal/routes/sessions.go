package routes

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"odysseus/internal/db"
)

// hiddenSystemSessionNames matches Python's _HIDDEN_SYSTEM_SESSION_NAMES.
var hiddenSystemSessionNames = map[string]bool{
	"[Task] Chat Sessions Tidy":    true,
	"[Task] Documents Tidy":        true,
	"[Task] Memory Tidy":           true,
	"[Task] Research Tidy":         true,
	"[Task] Email Mark Boundaries": true,
	"[Task] Email Tags":            true,
	"[Task] Skills Audit":          true,
}

// compareSessionPrefix — blind-compare sessions hide their model.
const compareSessionPrefix = "[CMP] "

type sessionRoutes struct {
	db *db.DB
}

// SessionRoutes registers session CRUD routes on the mux.
// Create/delete are NOT registered here — they proxy to Python because
// the Python session_manager needs to stay in sync for active chats.
func SessionRoutes(mux *http.ServeMux, database *db.DB) {
	r := &sessionRoutes{db: database}

	mux.HandleFunc("GET /api/sessions", r.listSessions)
	mux.HandleFunc("GET /api/sessions/archived", r.listArchivedSessions)
	mux.HandleFunc("GET /api/history/{sid}", r.getHistory)
	mux.HandleFunc("GET /api/sessions/{sid}/messages", r.getMessages)
	mux.HandleFunc("PATCH /api/session/{sid}", r.updateSession)
	mux.HandleFunc("POST /api/session/{sid}/archive", r.archiveSession)
	mux.HandleFunc("POST /api/session/{sid}/unarchive", r.unarchiveSession)
	mux.HandleFunc("POST /api/session/{sid}/important", r.markImportant)
}

// --- Handlers ---

func (r *sessionRoutes) listSessions(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)

	// Lazy purge stale incognito sessions (>10 min old).
	cutoff := time.Now().UTC().Add(-10 * time.Minute)
	r.db.PurgeStaleIncognito(cutoff)

	sessions, err := r.db.ListActiveSessions(user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Fetch document and image session sets for enrichment.
	docSessions, _ := r.db.SessionHasDocuments(user)
	imgSessions, _ := r.db.SessionHasImages(user)

	result := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		name := strings.TrimSpace(s.Name)
		if hiddenSystemSessionNames[name] {
			continue
		}

		model := s.Model
		if strings.HasPrefix(name, compareSessionPrefix) {
			model = ""
		}

		// last_message_at with fallback to updated_at then created_at.
		var lastMsgAt *string
		if s.LastMessageAt.Valid {
			v := s.LastMessageAt.Time.UTC().Format("2006-01-02T15:04:05")
			lastMsgAt = &v
		} else {
			v := s.UpdatedAt.UTC().Format("2006-01-02T15:04:05")
			if !s.UpdatedAt.IsZero() {
				lastMsgAt = &v
			} else if !s.CreatedAt.IsZero() {
				v = s.CreatedAt.UTC().Format("2006-01-02T15:04:05")
				lastMsgAt = &v
			}
		}

		entry := map[string]any{
			"id":              s.ID,
			"name":            s.Name,
			"model":           model,
			"endpoint_url":    s.EndpointURL,
			"rag":             s.RAG,
			"archived":        s.Archived,
			"folder":          db.StringVal(s.Folder),
			"total_tokens":    s.TotalInputTokens + s.TotalOutputTokens,
			"is_important":    s.IsImportant,
			"created_at":      formatTimeISO(s.CreatedAt),
			"updated_at":      formatTimeISO(s.UpdatedAt),
			"last_message_at": lastMsgAt,
			"has_documents":   docSessions[s.ID],
			"has_images":      imgSessions[s.ID],
			"mode":            db.StringVal(s.Mode),
			"message_count":   s.MessageCount,
		}
		if s.Folder.Valid {
			entry["folder"] = s.Folder.String
		} else {
			entry["folder"] = nil
		}
		result = append(result, entry)
	}

	writeJSON(w, http.StatusOK, result)
}

func (r *sessionRoutes) listArchivedSessions(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	q := req.URL.Query()
	search := q.Get("search")
	sort := q.Get("sort")
	modelFilter := q.Get("model")
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}

	sessions, total, err := r.db.ListArchivedSessions(user, search, sort, modelFilter, offset, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	items := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, map[string]any{
			"id":            s.ID,
			"name":          s.Name,
			"model":         s.Model,
			"message_count": s.MessageCount,
			"created_at":    formatTimeISO(s.CreatedAt),
			"updated_at":    formatTimeISO(s.UpdatedAt),
			"is_important":  s.IsImportant,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": items,
		"total":    total,
	})
}

func (r *sessionRoutes) getHistory(w http.ResponseWriter, req *http.Request) {
	sid := req.PathValue("sid")
	user := currentUser(req)

	if !r.verifyOwner(w, sid, user) {
		return
	}

	msgs, err := r.db.GetSessionMessages(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	history := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		entry := map[string]any{
			"role":      m.Role,
			"content":   m.Content,
			"timestamp": m.Timestamp.UTC().Format("2006-01-02T15:04:05"),
		}
		if m.MetaData.Valid && m.MetaData.String != "" {
			var meta any
			if json.Unmarshal([]byte(m.MetaData.String), &meta) == nil {
				entry["metadata"] = meta
			}
		}
		history = append(history, entry)
	}

	writeJSON(w, http.StatusOK, map[string]any{"history": history})
}

// getMessages is the same data as getHistory but returns a flat array.
// Matches the frontend's /api/sessions/{sid}/messages call.
func (r *sessionRoutes) getMessages(w http.ResponseWriter, req *http.Request) {
	sid := req.PathValue("sid")
	user := currentUser(req)

	if !r.verifyOwner(w, sid, user) {
		return
	}

	msgs, err := r.db.GetSessionMessages(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	result := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		entry := map[string]any{
			"id":         m.ID,
			"session_id": m.SessionID,
			"role":       m.Role,
			"content":    m.Content,
			"timestamp":  m.Timestamp.UTC().Format("2006-01-02T15:04:05"),
		}
		if m.MetaData.Valid && m.MetaData.String != "" {
			var meta any
			if json.Unmarshal([]byte(m.MetaData.String), &meta) == nil {
				entry["metadata"] = meta
			}
		}
		result = append(result, entry)
	}

	writeJSON(w, http.StatusOK, result)
}

func (r *sessionRoutes) updateSession(w http.ResponseWriter, req *http.Request) {
	sid := req.PathValue("sid")
	user := currentUser(req)

	if !r.verifyOwner(w, sid, user) {
		return
	}

	// Parse form data (matching Python's Form() parameters).
	req.ParseMultipartForm(1 << 20)
	result := map[string]any{"id": sid}
	changed := false

	if name := req.FormValue("name"); name != "" {
		if err := r.db.UpdateSessionName(sid, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		result["name"] = name
		changed = true
	}

	if folder, ok := formValuePresent(req, "folder"); ok {
		var f *string
		if folder != "" {
			f = &folder
		}
		if err := r.db.UpdateSessionFolder(sid, f); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if f != nil {
			result["folder"] = *f
		} else {
			result["folder"] = nil
		}
		changed = true
	}

	// Model/endpoint switch — only if both are provided.
	model := req.FormValue("model")
	endpointURL := req.FormValue("endpoint_url")
	if model != "" && endpointURL != "" {
		if err := r.db.UpdateSessionModel(sid, model, endpointURL, nil); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		result["model"] = model
		result["endpoint_url"] = endpointURL
		changed = true
	}

	if !changed {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (r *sessionRoutes) archiveSession(w http.ResponseWriter, req *http.Request) {
	sid := req.PathValue("sid")
	user := currentUser(req)
	if !r.verifyOwner(w, sid, user) {
		return
	}
	if err := r.db.ArchiveSession(sid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "archived"})
}

func (r *sessionRoutes) unarchiveSession(w http.ResponseWriter, req *http.Request) {
	sid := req.PathValue("sid")
	user := currentUser(req)
	if !r.verifyOwner(w, sid, user) {
		return
	}
	if err := r.db.UnarchiveSession(sid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unarchived"})
}

func (r *sessionRoutes) markImportant(w http.ResponseWriter, req *http.Request) {
	sid := req.PathValue("sid")
	user := currentUser(req)
	if !r.verifyOwner(w, sid, user) {
		return
	}

	// Parse form data.
	req.ParseMultipartForm(1 << 20)
	important := true
	if v := req.FormValue("important"); v != "" {
		important = strings.EqualFold(v, "true")
	}

	if err := r.db.SetSessionImportant(sid, important); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "success",
		"is_important": important,
	})
}

// --- helpers ---

// verifyOwner checks the user owns the session. Returns false and writes 404 if not.
func (r *sessionRoutes) verifyOwner(w http.ResponseWriter, sid, user string) bool {
	owner := r.db.SessionOwner(sid)
	if owner == "" {
		// Session doesn't exist or has no owner (legacy) — allow.
		return true
	}
	if owner != user && user != "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Session not found"})
		return false
	}
	return true
}

// formValuePresent checks if a form field was submitted (even if empty).
func formValuePresent(r *http.Request, key string) (string, bool) {
	if r.MultipartForm != nil {
		if vals, ok := r.MultipartForm.Value[key]; ok && len(vals) > 0 {
			return vals[0], true
		}
	}
	if r.PostForm != nil {
		if vals, ok := r.PostForm[key]; ok && len(vals) > 0 {
			return vals[0], true
		}
	}
	return "", false
}

// currentUser extracts the username from request context (set by auth middleware).
func currentUser(r *http.Request) string {
	if v, ok := r.Context().Value(CtxKeyCurrentUser).(string); ok {
		return v
	}
	return ""
}

func formatTimeISO(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05")
	return &s
}
