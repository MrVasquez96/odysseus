package routes

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"odysseus/internal/db"

	"github.com/google/uuid"
)

type noteRoutes struct {
	db *db.DB
}

// NoteRoutes registers note CRUD routes.
// POST /api/notes/fire-reminder is NOT registered — stays proxied to Python (uses LLM/email).
func NoteRoutes(mux *http.ServeMux, database *db.DB) {
	r := &noteRoutes{db: database}

	mux.HandleFunc("GET /api/notes", r.listNotes)
	mux.HandleFunc("POST /api/notes", r.createNote)
	mux.HandleFunc("POST /api/notes/reorder", r.reorderNotes)
	mux.HandleFunc("GET /api/notes/{id}", r.getNote)
	mux.HandleFunc("PUT /api/notes/{id}", r.updateNote)
	mux.HandleFunc("DELETE /api/notes/{id}", r.deleteNote)
	mux.HandleFunc("POST /api/notes/{id}/pin", r.togglePin)
	mux.HandleFunc("POST /api/notes/{id}/archive", r.toggleArchive)
	mux.HandleFunc("POST /api/notes/{id}/items/{index}/toggle", r.toggleItem)
}

func noteToJSON(n *db.NoteRow) map[string]any {
	out := map[string]any{
		"id":          n.ID,
		"owner":       db.StringVal(n.Owner),
		"title":       n.Title,
		"content":     db.StringVal(n.Content),
		"note_type":   n.NoteType,
		"color":       db.StringVal(n.Color),
		"label":       db.StringVal(n.Label),
		"pinned":      n.Pinned,
		"archived":    n.Archived,
		"due_date":    db.StringVal(n.DueDate),
		"source":      n.Source,
		"session_id":  db.StringVal(n.SessionID),
		"sort_order":  n.SortOrder,
		"image_url":   db.StringVal(n.ImageURL),
		"repeat":      n.Repeat,
		"created_at":  formatTimeISO(n.CreatedAt),
		"updated_at":  formatTimeISO(n.UpdatedAt),
	}
	// Parse JSON fields.
	if n.Items.Valid && n.Items.String != "" {
		var items any
		if json.Unmarshal([]byte(n.Items.String), &items) == nil {
			out["items"] = items
		}
	}
	if n.AIClassification.Valid && n.AIClassification.String != "" {
		var cls any
		if json.Unmarshal([]byte(n.AIClassification.String), &cls) == nil {
			out["ai_classification"] = cls
		}
	}
	if n.AIContentHash.Valid {
		out["ai_content_hash"] = n.AIContentHash.String
	}
	if n.AgentSessionID.Valid {
		out["agent_session_id"] = n.AgentSessionID.String
	}
	return out
}

func (r *noteRoutes) ownerCheck(n *db.NoteRow, user string) bool {
	if user == "" {
		return true // auth disabled
	}
	return db.StringVal(n.Owner) == user
}

func (r *noteRoutes) listNotes(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	var archived *bool
	if v := req.URL.Query().Get("archived"); v != "" {
		b := v == "true" || v == "1"
		archived = &b
	}
	label := req.URL.Query().Get("label")

	notes, err := r.db.ListNotes(user, archived, label)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	result := make([]map[string]any, 0, len(notes))
	for i := range notes {
		result = append(result, noteToJSON(&notes[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": result})
}

func (r *noteRoutes) createNote(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	var body struct {
		Title     string  `json:"title"`
		Content   *string `json:"content"`
		Items     any     `json:"items"`
		NoteType  string  `json:"note_type"`
		Color     *string `json:"color"`
		Label     *string `json:"label"`
		Pinned    bool    `json:"pinned"`
		DueDate   *string `json:"due_date"`
		Source    string  `json:"source"`
		SessionID *string `json:"session_id"`
		ImageURL  *string `json:"image_url"`
		Repeat    *string `json:"repeat"`
		SortOrder *int    `json:"sort_order"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if body.NoteType == "" {
		body.NoteType = "note"
	}
	if body.Source == "" {
		body.Source = "user"
	}
	repeat := "none"
	if body.Repeat != nil {
		repeat = *body.Repeat
	}
	sortOrder := 0
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}

	var itemsJSON sql.NullString
	if body.Items != nil {
		data, _ := json.Marshal(body.Items)
		itemsJSON = sql.NullString{String: string(data), Valid: true}
	}

	n := &db.NoteRow{
		ID:        uuid.New().String(),
		Owner:     sql.NullString{String: user, Valid: user != ""},
		Title:     body.Title,
		Content:   nullStrPtr(body.Content),
		Items:     itemsJSON,
		NoteType:  body.NoteType,
		Color:     nullStrPtr(body.Color),
		Label:     nullStrPtr(body.Label),
		Pinned:    body.Pinned,
		DueDate:   nullStrPtr(body.DueDate),
		Source:    body.Source,
		SessionID: nullStrPtr(body.SessionID),
		SortOrder: sortOrder,
		ImageURL:  nullStrPtr(body.ImageURL),
		Repeat:    repeat,
	}
	if err := r.db.CreateNote(n); err != nil {
		jsonError(w, "Failed to create note", http.StatusInternalServerError)
		return
	}
	// Re-read to get timestamps.
	created, _ := r.db.GetNote(n.ID)
	if created != nil {
		writeJSON(w, http.StatusOK, noteToJSON(created))
	} else {
		writeJSON(w, http.StatusOK, noteToJSON(n))
	}
}

func (r *noteRoutes) getNote(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	noteID := req.PathValue("id")
	note, err := r.db.GetNote(noteID)
	if err != nil || note == nil {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	if !r.ownerCheck(note, user) {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, noteToJSON(note))
}

func (r *noteRoutes) updateNote(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	noteID := req.PathValue("id")
	note, err := r.db.GetNote(noteID)
	if err != nil || note == nil {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	if !r.ownerCheck(note, user) {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}

	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Build dynamic UPDATE.
	sets := []string{}
	args := []any{}
	if v, ok := body["title"]; ok {
		sets = append(sets, "title = ?")
		args = append(args, v)
	}
	if v, ok := body["content"]; ok {
		sets = append(sets, "content = ?")
		args = append(args, v)
	}
	if v, ok := body["items"]; ok {
		data, _ := json.Marshal(v)
		sets = append(sets, "items = ?")
		args = append(args, string(data))
	}
	if v, ok := body["note_type"]; ok {
		sets = append(sets, "note_type = ?")
		args = append(args, v)
	}
	if v, ok := body["color"]; ok {
		sets = append(sets, "color = ?")
		args = append(args, v)
	}
	if v, ok := body["label"]; ok {
		sets = append(sets, "label = ?")
		args = append(args, v)
	}
	if v, ok := body["pinned"]; ok {
		sets = append(sets, "pinned = ?")
		args = append(args, v)
	}
	if v, ok := body["archived"]; ok {
		sets = append(sets, "archived = ?")
		args = append(args, v)
	}
	if v, ok := body["due_date"]; ok {
		sets = append(sets, "due_date = ?")
		args = append(args, v)
	}
	if v, ok := body["image_url"]; ok {
		sets = append(sets, "image_url = ?")
		args = append(args, v)
	}
	if v, ok := body["repeat"]; ok {
		sets = append(sets, "repeat = ?")
		args = append(args, v)
	}
	if v, ok := body["sort_order"]; ok {
		sets = append(sets, "sort_order = ?")
		args = append(args, v)
	}
	if v, ok := body["agent_session_id"]; ok {
		sets = append(sets, "agent_session_id = ?")
		args = append(args, v)
	}

	if len(sets) > 0 {
		sets = append(sets, "updated_at = ?")
		args = append(args, db.UtcNow())
		args = append(args, noteID)
		r.db.Exec("UPDATE notes SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	}

	updated, _ := r.db.GetNote(noteID)
	if updated != nil {
		writeJSON(w, http.StatusOK, noteToJSON(updated))
	} else {
		writeJSON(w, http.StatusOK, noteToJSON(note))
	}
}

func (r *noteRoutes) deleteNote(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	noteID := req.PathValue("id")
	note, err := r.db.GetNote(noteID)
	if err != nil || note == nil {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	if !r.ownerCheck(note, user) {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	r.db.DeleteNote(noteID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (r *noteRoutes) togglePin(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	noteID := req.PathValue("id")
	note, err := r.db.GetNote(noteID)
	if err != nil || note == nil {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	if !r.ownerCheck(note, user) {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	newVal := !note.Pinned
	r.db.Exec("UPDATE notes SET pinned = ?, updated_at = ? WHERE id = ?", newVal, db.UtcNow(), noteID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pinned": newVal})
}

func (r *noteRoutes) toggleArchive(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	noteID := req.PathValue("id")
	note, err := r.db.GetNote(noteID)
	if err != nil || note == nil {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	if !r.ownerCheck(note, user) {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	newVal := !note.Archived
	r.db.Exec("UPDATE notes SET archived = ?, updated_at = ? WHERE id = ?", newVal, db.UtcNow(), noteID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "archived": newVal})
}

func (r *noteRoutes) toggleItem(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	noteID := req.PathValue("id")
	indexStr := req.PathValue("index")
	idx, err := strconv.Atoi(indexStr)
	if err != nil || idx < 0 {
		jsonError(w, "Invalid item index", http.StatusBadRequest)
		return
	}
	note, err := r.db.GetNote(noteID)
	if err != nil || note == nil {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	if !r.ownerCheck(note, user) {
		jsonError(w, "Note not found", http.StatusNotFound)
		return
	}
	if !note.Items.Valid || note.Items.String == "" {
		jsonError(w, "Note has no checklist items", http.StatusBadRequest)
		return
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(note.Items.String), &items); err != nil {
		jsonError(w, "Invalid items data", http.StatusInternalServerError)
		return
	}
	if idx >= len(items) {
		jsonError(w, "Item index out of range", http.StatusBadRequest)
		return
	}
	done, _ := items[idx]["done"].(bool)
	items[idx]["done"] = !done
	data, _ := json.Marshal(items)
	r.db.Exec("UPDATE notes SET items = ?, updated_at = ? WHERE id = ?", string(data), db.UtcNow(), noteID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

func (r *noteRoutes) reorderNotes(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	r.db.UpdateNoteSortOrders(body.IDs, user)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(body.IDs)})
}

func nullStrPtr(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}
