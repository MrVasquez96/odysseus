package db

import "database/sql"

type TaskRow struct {
	ID                   string
	Owner                sql.NullString
	Name                 string
	Prompt               sql.NullString
	TaskType             sql.NullString
	Action               sql.NullString
	Schedule             sql.NullString
	ScheduledTime        sql.NullString
	ScheduledDay         sql.NullInt64
	ScheduledDate        sql.NullString
	TriggerType          sql.NullString
	TriggerEvent         sql.NullString
	TriggerCount         sql.NullInt64
	TriggerCounter       sql.NullInt64
	NextRun              sql.NullString
	LastRun              sql.NullString
	Status               sql.NullString
	OutputTarget         sql.NullString
	SessionID            sql.NullString
	Model                sql.NullString
	EndpointURL          sql.NullString
	RunCount             sql.NullInt64
	CronExpression       sql.NullString
	ThenTaskID           sql.NullString
	WebhookToken         sql.NullString
	CrewMemberID         sql.NullString
	NotificationsEnabled sql.NullBool
	CreatedAt            sql.NullString
	UpdatedAt            sql.NullString
}

const taskSelectCols = `id, owner, name, prompt, task_type, action,
	schedule, scheduled_time, scheduled_day, scheduled_date,
	trigger_type, trigger_event, trigger_count, COALESCE(trigger_counter, 0),
	next_run, last_run, status, output_target, session_id,
	model, endpoint_url, COALESCE(run_count, 0), cron_expression,
	then_task_id, webhook_token, crew_member_id,
	notifications_enabled, created_at, updated_at`

func scanTask(row interface{ Scan(...any) error }) (TaskRow, error) {
	var t TaskRow
	err := row.Scan(
		&t.ID, &t.Owner, &t.Name, &t.Prompt, &t.TaskType, &t.Action,
		&t.Schedule, &t.ScheduledTime, &t.ScheduledDay, &t.ScheduledDate,
		&t.TriggerType, &t.TriggerEvent, &t.TriggerCount, &t.TriggerCounter,
		&t.NextRun, &t.LastRun, &t.Status, &t.OutputTarget, &t.SessionID,
		&t.Model, &t.EndpointURL, &t.RunCount, &t.CronExpression,
		&t.ThenTaskID, &t.WebhookToken, &t.CrewMemberID,
		&t.NotificationsEnabled, &t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}

func (d *DB) ListTasks(owner string, status string) ([]TaskRow, error) {
	q := "SELECT " + taskSelectCols + " FROM scheduled_tasks WHERE 1=1"
	args := []any{}
	if owner != "" {
		q += " AND owner = ?"
		args = append(args, owner)
	}
	if status != "" {
		q += " AND status = ?"
		args = append(args, status)
	}
	q += " ORDER BY created_at DESC"

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskRow
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

type TaskRunRow struct {
	ID         string
	TaskID     string
	StartedAt  sql.NullString
	FinishedAt sql.NullString
	Status     sql.NullString
	Result     sql.NullString
	Error      sql.NullString
	TokensUsed sql.NullInt64
	Model      sql.NullString
}

func (d *DB) ListTaskRuns(taskID string, limit int) ([]TaskRunRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(
		`SELECT id, task_id, started_at, finished_at, status, result, error, tokens_used, model
		 FROM task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT ?`,
		taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskRunRow
	for rows.Next() {
		var r TaskRunRow
		if err := rows.Scan(&r.ID, &r.TaskID, &r.StartedAt, &r.FinishedAt,
			&r.Status, &r.Result, &r.Error, &r.TokensUsed, &r.Model); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (d *DB) ListRecentRuns(owner string, limit int) ([]TaskRunRow, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT r.id, r.task_id, r.started_at, r.finished_at, r.status, r.result, r.error, r.tokens_used, r.model
		  FROM task_runs r JOIN scheduled_tasks t ON r.task_id = t.id`
	args := []any{}
	if owner != "" {
		q += " WHERE t.owner = ?"
		args = append(args, owner)
	}
	q += " ORDER BY r.started_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskRunRow
	for rows.Next() {
		var r TaskRunRow
		if err := rows.Scan(&r.ID, &r.TaskID, &r.StartedAt, &r.FinishedAt,
			&r.Status, &r.Result, &r.Error, &r.TokensUsed, &r.Model); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetLastRunForTask returns the most recent TaskRunRow for a task.
func (d *DB) GetLastRunForTask(taskID string) *TaskRunRow {
	row := d.QueryRow(
		`SELECT id, task_id, started_at, finished_at, status, result, error, tokens_used, model
		 FROM task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT 1`, taskID)
	var r TaskRunRow
	if err := row.Scan(&r.ID, &r.TaskID, &r.StartedAt, &r.FinishedAt,
		&r.Status, &r.Result, &r.Error, &r.TokensUsed, &r.Model); err != nil {
		return nil
	}
	return &r
}
