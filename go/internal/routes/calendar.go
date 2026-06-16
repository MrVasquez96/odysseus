package routes

import (
	"encoding/json"
	"net/http"
	"strings"

	"odysseus/internal/db"

	"github.com/google/uuid"
)

type calendarRoutes struct {
	db *db.DB
}

// CalendarRoutes registers calendar CRUD routes served by Go.
// CalDAV sync, event CRUD (with CalDAV writeback), RRULE expansion, and
// import/export stay proxied to Python.
func CalendarRoutes(mux *http.ServeMux, database *db.DB) {
	r := &calendarRoutes{db: database}

	mux.HandleFunc("GET /api/calendar/calendars", r.listCalendars)
	mux.HandleFunc("POST /api/calendar/calendars", r.createCalendar)
	mux.HandleFunc("PUT /api/calendar/calendars/{id}", r.updateCalendar)
	mux.HandleFunc("DELETE /api/calendar/calendars/{id}", r.deleteCalendar)
}

func (r *calendarRoutes) listCalendars(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	if user == "" {
		jsonError(w, "Auth required", http.StatusUnauthorized)
		return
	}

	r.db.EnsureDefaultCalendar(user)

	cals, err := r.db.ListCalendars(user)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	result := make([]map[string]any, 0, len(cals))
	for _, c := range cals {
		result = append(result, map[string]any{
			"name":   c.Name,
			"href":   c.ID,
			"color":  c.Color,
			"source": c.Source,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"calendars": result})
}

func (r *calendarRoutes) createCalendar(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	if user == "" {
		jsonError(w, "Auth required", http.StatusUnauthorized)
		return
	}

	name := strings.TrimSpace(req.URL.Query().Get("name"))
	if name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}
	color := strings.TrimSpace(req.URL.Query().Get("color"))
	if color == "" {
		color = "#5b8abf"
	}

	id := uuid.New().String()
	if err := r.db.CreateCalendar(id, user, name, color); err != nil {
		jsonError(w, "Failed to create calendar", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"id":    id,
		"name":  name,
		"color": color,
	})
}

func (r *calendarRoutes) updateCalendar(w http.ResponseWriter, req *http.Request) {
	calID := req.PathValue("id")
	user := currentUser(req)
	if user == "" {
		jsonError(w, "Auth required", http.StatusUnauthorized)
		return
	}

	owner, err := r.db.GetCalendarOwner(calID)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if owner == "" {
		jsonError(w, "Calendar not found", http.StatusNotFound)
		return
	}
	if owner != user {
		jsonError(w, "Not your calendar", http.StatusForbidden)
		return
	}

	// Python uses query params for name/color.
	var name, color *string
	if v := strings.TrimSpace(req.URL.Query().Get("name")); v != "" {
		name = &v
	}
	if v := strings.TrimSpace(req.URL.Query().Get("color")); v != "" {
		color = &v
	}

	// Also accept JSON body.
	if name == nil && color == nil {
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err == nil {
			if v := strings.TrimSpace(body["name"]); v != "" {
				name = &v
			}
			if v := strings.TrimSpace(body["color"]); v != "" {
				color = &v
			}
		}
	}

	if err := r.db.UpdateCalendar(calID, name, color); err != nil {
		jsonError(w, "Failed to update calendar", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (r *calendarRoutes) deleteCalendar(w http.ResponseWriter, req *http.Request) {
	calID := req.PathValue("id")
	user := currentUser(req)
	if user == "" {
		jsonError(w, "Auth required", http.StatusUnauthorized)
		return
	}

	owner, err := r.db.GetCalendarOwner(calID)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if owner == "" {
		jsonError(w, "Calendar not found", http.StatusNotFound)
		return
	}
	if owner != user {
		jsonError(w, "Not your calendar", http.StatusForbidden)
		return
	}

	if err := r.db.DeleteCalendarWithEvents(calID); err != nil {
		jsonError(w, "Failed to delete calendar", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
