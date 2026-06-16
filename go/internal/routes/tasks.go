package routes

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"odysseus/internal/db"
)

// Housekeeping task defaults — matches Python's HOUSEKEEPING_DEFAULTS.
// Used for is_builtin/is_modified detection.
var housekeepingDefaults = map[string]housekeepingDef{
	"tidy_sessions":        {Name: "Chat Sessions Tidy", Schedule: "", ScheduledTime: "", CronExpression: "", LegacyNames: []string{"Tidy Chat Sessions"}},
	"tidy_documents":       {Name: "Documents Tidy", Schedule: "", ScheduledTime: "", CronExpression: "", LegacyNames: []string{"Tidy Documents"}},
	"consolidate_memory":   {Name: "Memory Tidy", Schedule: "", ScheduledTime: "", CronExpression: "", LegacyNames: []string{"Tidy Memory"}},
	"tidy_research":        {Name: "Research Tidy", Schedule: "", ScheduledTime: "", CronExpression: "", LegacyNames: []string{"Tidy Research"}},
	"summarize_emails":     {Name: "Email (Summary)", Schedule: "cron", ScheduledTime: "", CronExpression: "0 */2 * * *", LegacyNames: []string{"Tidy Email (Summary)"}},
	"draft_email_replies":  {Name: "Email AI Auto Reply", Schedule: "cron", ScheduledTime: "", CronExpression: "0 */2 * * *", LegacyNames: []string{"Tidy Email (Replies)", "AI Auto Reply"}},
	"extract_email_events": {Name: "Email Calendar Events", Schedule: "cron", ScheduledTime: "", CronExpression: "0 */1 * * *", LegacyNames: []string{"Email → Calendar Events"}},
	"classify_events":      {Name: "Calendar Classify Events", Schedule: "cron", ScheduledTime: "", CronExpression: "0 6,18 * * *", LegacyNames: []string{"Classify Calendar Events"}},
	"check_email_urgency":  {Name: "Email Tags", Schedule: "cron", ScheduledTime: "", CronExpression: "0 * * * *", LegacyNames: []string{"Email Triage", "Urgent Email"}},
	"audit_skills":         {Name: "Skills Audit", Schedule: "", ScheduledTime: "", CronExpression: "", LegacyNames: []string{"Audit Skills"}},
}

type housekeepingDef struct {
	Name           string
	Schedule       string
	ScheduledTime  string
	CronExpression string
	LegacyNames    []string
}

type taskRoutes struct {
	db *db.DB
}

// TaskRoutes registers task read routes served by Go.
// Create/update/delete/run/pause/resume stay proxied to Python (they need
// compute_next_run, ensure_defaults, LLM name generation, webhook tokens, etc.).
func TaskRoutes(mux *http.ServeMux, database *db.DB) {
	r := &taskRoutes{db: database}

	mux.HandleFunc("GET /api/tasks", r.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}/runs", r.listRuns)
	mux.HandleFunc("GET /api/tasks/runs/recent", r.recentRuns)
}

func taskToJSON(t *db.TaskRow, includeLastRun bool, lastRun *db.TaskRunRow) map[string]any {
	action := db.StringVal(t.Action)
	taskType := db.StringVal(t.TaskType)
	if taskType == "" {
		taskType = "llm"
	}
	triggerType := db.StringVal(t.TriggerType)
	if triggerType == "" {
		triggerType = "schedule"
	}

	// Display name: if action is a built-in and name is a legacy name, show current name.
	displayName := t.Name
	defs, isBuiltin := housekeepingDefaults[action]
	if isBuiltin {
		for _, legacy := range defs.LegacyNames {
			if t.Name == legacy {
				displayName = defs.Name
				break
			}
		}
	}

	d := map[string]any{
		"id":                    t.ID,
		"name":                  displayName,
		"prompt":                db.StringVal(t.Prompt),
		"task_type":             taskType,
		"action":                action,
		"schedule":              db.StringVal(t.Schedule),
		"scheduled_time":        db.StringVal(t.ScheduledTime),
		"scheduled_day":         nullInt(t.ScheduledDay),
		"scheduled_date":        fmtNullDTZ(t.ScheduledDate),
		"cron_expression":       db.StringVal(t.CronExpression),
		"trigger_type":          triggerType,
		"trigger_event":         db.StringVal(t.TriggerEvent),
		"trigger_count":         nullInt(t.TriggerCount),
		"trigger_counter":       t.TriggerCounter.Int64,
		"next_run":              fmtNullDTZ(t.NextRun),
		"last_run":              fmtNullDTZ(t.LastRun),
		"status":                db.StringVal(t.Status),
		"output_target":         db.StringVal(t.OutputTarget),
		"session_id":            db.StringVal(t.SessionID),
		"crew_member_id":        db.StringVal(t.CrewMemberID),
		"model":                 db.StringVal(t.Model),
		"endpoint_url":          db.StringVal(t.EndpointURL),
		"run_count":             t.RunCount.Int64,
		"then_task_id":          db.StringVal(t.ThenTaskID),
		"notifications_enabled": t.NotificationsEnabled.Valid && t.NotificationsEnabled.Bool,
		"created_at":            fmtNullDTZ(t.CreatedAt),
		"updated_at":            fmtNullDTZ(t.UpdatedAt),
		"is_builtin":            isBuiltin,
	}

	// Webhook token: only expose for webhook-triggered tasks.
	if triggerType == "webhook" {
		d["webhook_token"] = db.StringVal(t.WebhookToken)
	}

	// is_modified detection for built-in tasks.
	if isBuiltin {
		defaultNames := map[string]bool{defs.Name: true}
		for _, ln := range defs.LegacyNames {
			defaultNames[ln] = true
		}
		d["is_modified"] = !defaultNames[t.Name] ||
			db.StringVal(t.Schedule) != defs.Schedule ||
			db.StringVal(t.ScheduledTime) != defs.ScheduledTime ||
			db.StringVal(t.CronExpression) != defs.CronExpression
	} else {
		d["is_modified"] = false
	}

	if includeLastRun && lastRun != nil {
		d["last_run_status"] = db.StringVal(lastRun.Status)
		result := db.StringVal(lastRun.Result)
		if result == "" {
			result = db.StringVal(lastRun.Error)
		}
		if len(result) > 500 {
			result = result[:500]
		}
		d["last_run_result"] = result
	}

	return d
}

func runToJSON(r *db.TaskRunRow) map[string]any {
	return map[string]any{
		"id":          r.ID,
		"task_id":     r.TaskID,
		"started_at":  fmtNullDTZ(r.StartedAt),
		"finished_at": fmtNullDTZ(r.FinishedAt),
		"status":      db.StringVal(r.Status),
		"result":      db.StringVal(r.Result),
		"error":       db.StringVal(r.Error),
		"tokens_used": nullInt(r.TokensUsed),
		"model":       db.StringVal(r.Model),
	}
}

func (r *taskRoutes) listTasks(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	status := req.URL.Query().Get("status")
	includeLastRun := req.URL.Query().Get("include_last_run") == "true"

	tasks, err := r.db.ListTasks(user, status)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	result := make([]map[string]any, 0, len(tasks))
	for i := range tasks {
		var lastRun *db.TaskRunRow
		if includeLastRun {
			lastRun = r.db.GetLastRunForTask(tasks[i].ID)
		}
		result = append(result, taskToJSON(&tasks[i], includeLastRun, lastRun))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": result})
}

func (r *taskRoutes) listRuns(w http.ResponseWriter, req *http.Request) {
	taskID := req.PathValue("id")
	limit := 50
	if v := req.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := r.db.ListTaskRuns(taskID, limit)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	result := make([]map[string]any, 0, len(runs))
	for i := range runs {
		result = append(result, runToJSON(&runs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": result})
}

func (r *taskRoutes) recentRuns(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	limit := 20
	if v := req.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := r.db.ListRecentRuns(user, limit)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	result := make([]map[string]any, 0, len(runs))
	for i := range runs {
		result = append(result, runToJSON(&runs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": result})
}

// fmtNullDTZ formats a nullable datetime string with Z suffix (UTC).
func fmtNullDTZ(ns sql.NullString) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	s := ns.String
	if !strings.HasSuffix(s, "Z") && !strings.Contains(s, "+") {
		s += "Z"
	}
	return &s
}

func nullInt(ni sql.NullInt64) any {
	if !ni.Valid {
		return nil
	}
	return ni.Int64
}
