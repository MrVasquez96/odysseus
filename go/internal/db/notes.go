package db

import (
	"database/sql"
	"time"
)

type NoteRow struct {
	ID               string
	Owner            sql.NullString
	Title            string
	Content          sql.NullString
	Items            sql.NullString // JSON array
	NoteType         string
	Color            sql.NullString
	Label            sql.NullString
	Pinned           bool
	Archived         bool
	DueDate          sql.NullString
	Source           string
	SessionID        sql.NullString
	SortOrder        int
	ImageURL         sql.NullString
	Repeat           string
	AIClassification sql.NullString // JSON
	AIContentHash    sql.NullString
	AgentSessionID   sql.NullString
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const noteSelectCols = `id, owner, title, content, items, note_type, color, label,
	pinned, archived, due_date, source, session_id,
	COALESCE(sort_order, 0), image_url, COALESCE(repeat, 'none'),
	ai_classification, ai_content_hash, agent_session_id,
	COALESCE(created_at, ''), COALESCE(updated_at, '')`

func scanNote(row interface{ Scan(...any) error }) (NoteRow, error) {
	var n NoteRow
	var createdStr, updatedStr string
	err := row.Scan(
		&n.ID, &n.Owner, &n.Title, &n.Content, &n.Items,
		&n.NoteType, &n.Color, &n.Label, &n.Pinned, &n.Archived,
		&n.DueDate, &n.Source, &n.SessionID, &n.SortOrder,
		&n.ImageURL, &n.Repeat, &n.AIClassification, &n.AIContentHash,
		&n.AgentSessionID, &createdStr, &updatedStr,
	)
	if err != nil {
		return n, err
	}
	n.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdStr)
	n.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedStr)
	return n, nil
}

func (d *DB) ListNotes(owner string, archived *bool, label string) ([]NoteRow, error) {
	q := "SELECT " + noteSelectCols + " FROM notes WHERE 1=1"
	args := []any{}
	if owner != "" {
		q += " AND owner = ?"
		args = append(args, owner)
	}
	if archived != nil {
		q += " AND archived = ?"
		if *archived {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	} else {
		q += " AND archived = 0"
	}
	if label != "" {
		q += " AND label = ?"
		args = append(args, label)
	}
	if archived != nil && *archived {
		q += " ORDER BY updated_at DESC"
	} else {
		q += " ORDER BY pinned DESC, sort_order ASC, updated_at DESC"
	}

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []NoteRow
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

func (d *DB) GetNote(noteID string) (*NoteRow, error) {
	row := d.QueryRow("SELECT "+noteSelectCols+" FROM notes WHERE id = ?", noteID)
	n, err := scanNote(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (d *DB) CreateNote(n *NoteRow) error {
	_, err := d.Exec(
		`INSERT INTO notes (id, owner, title, content, items, note_type, color, label,
			pinned, archived, due_date, source, session_id, sort_order, image_url, repeat,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, NullStr(n.Owner), n.Title, NullStr(n.Content), NullStr(n.Items),
		n.NoteType, NullStr(n.Color), NullStr(n.Label), n.Pinned,
		NullStr(n.DueDate), n.Source, NullStr(n.SessionID), n.SortOrder,
		NullStr(n.ImageURL), n.Repeat, utcNow(), utcNow(),
	)
	return err
}

func (d *DB) DeleteNote(noteID string) error {
	_, err := d.Exec("DELETE FROM notes WHERE id = ?", noteID)
	return err
}

func (d *DB) UpdateNoteSortOrders(ids []string, owner string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if owner != "" {
			tx.Exec("UPDATE notes SET sort_order = ? WHERE id = ? AND owner = ?", i, id, owner)
		} else {
			tx.Exec("UPDATE notes SET sort_order = ? WHERE id = ?", i, id)
		}
	}
	return tx.Commit()
}

// NullStr returns a sql.NullString from another NullString (pass-through helper).
func NullStr(ns sql.NullString) sql.NullString {
	return ns
}
