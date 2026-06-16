package db

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

// SessionListRow is the lightweight projection used by GET /api/sessions.
// Matches the column set in Python's list_sessions DB query.
type SessionListRow struct {
	ID                string
	Name              string
	Model             string
	EndpointURL       string
	RAG               bool
	Archived          bool
	Folder            sql.NullString
	TotalInputTokens  int
	TotalOutputTokens int
	IsImportant       bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
	LastMessageAt     sql.NullTime
	LastAccessed      sql.NullTime
	Mode              sql.NullString
	MessageCount      int
	CrewMemberID      sql.NullString
}

// ListActiveSessions returns non-archived sessions for a user.
func (d *DB) ListActiveSessions(owner string) ([]SessionListRow, error) {
	q := `SELECT id, name, model, endpoint_url, rag, archived, folder,
	             COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0),
	             COALESCE(is_important, 0), created_at, updated_at,
	             last_message_at, last_accessed, mode,
	             COALESCE(message_count, 0), crew_member_id
	      FROM sessions
	      WHERE archived = 0
	        AND (owner = ? OR owner IS NULL)
	        AND COALESCE(TRIM(name), '') NOT IN ('Nobody', 'Incognito')
	      ORDER BY COALESCE(last_message_at, updated_at, created_at) DESC`
	return d.scanSessionRows(d.Query(q, owner))
}

// ListArchivedSessions returns archived sessions with search/pagination.
func (d *DB) ListArchivedSessions(owner, search, sort, modelFilter string, offset, limit int) ([]SessionListRow, int, error) {
	baseWhere := `WHERE archived = 1 AND (owner = ? OR owner IS NULL)`
	args := []any{owner}

	if search != "" {
		baseWhere += ` AND name LIKE ?`
		args = append(args, "%"+search+"%")
	}
	if modelFilter != "" {
		baseWhere += ` AND model = ?`
		args = append(args, modelFilter)
	}

	// Count total.
	var total int
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	err := d.QueryRow(`SELECT COUNT(*) FROM sessions `+baseWhere, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	orderBy := "ORDER BY updated_at DESC"
	switch sort {
	case "oldest":
		orderBy = "ORDER BY updated_at ASC"
	case "messages":
		orderBy = "ORDER BY COALESCE(message_count, 0) DESC"
	case "name":
		orderBy = "ORDER BY name ASC"
	}

	q := `SELECT id, name, model, endpoint_url, rag, archived, folder,
	             COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0),
	             COALESCE(is_important, 0), created_at, updated_at,
	             last_message_at, last_accessed, mode,
	             COALESCE(message_count, 0), crew_member_id
	      FROM sessions ` + baseWhere + ` ` + orderBy + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := d.scanSessionRows(d.Query(q, args...))
	return rows, total, err
}

// GetSession returns a single session by ID.
func (d *DB) GetSession(id string) (*Session, error) {
	s := &Session{}
	err := d.QueryRow(`SELECT id, name, endpoint_url, model, owner, rag, archived,
	                          folder, headers, created_at, updated_at, last_accessed,
	                          last_message_at, COALESCE(is_important, 0),
	                          COALESCE(message_count, 0),
	                          COALESCE(total_input_tokens, 0),
	                          COALESCE(total_output_tokens, 0),
	                          mode, crew_member_id
	                   FROM sessions WHERE id = ?`, id).Scan(
		&s.ID, &s.Name, &s.EndpointURL, &s.Model, &s.Owner,
		&s.RAG, &s.Archived, &s.Folder, &s.Headers,
		&s.CreatedAt, &s.UpdatedAt, &s.LastAccessed,
		&s.LastMessageAt, &s.IsImportant, &s.MessageCount,
		&s.TotalInputTokens, &s.TotalOutputTokens,
		&s.Mode, &s.CrewMemberID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// SessionOwner returns the owner of a session, or "" if not found.
func (d *DB) SessionOwner(id string) string {
	var owner sql.NullString
	d.QueryRow(`SELECT owner FROM sessions WHERE id = ?`, id).Scan(&owner)
	return StringVal(owner)
}

// UpdateSessionName updates the session name.
func (d *DB) UpdateSessionName(id, name string) error {
	_, err := d.Exec(`UPDATE sessions SET name = ?, updated_at = ? WHERE id = ?`,
		name, utcNow(), id)
	return err
}

// UpdateSessionFolder sets the folder for a session.
func (d *DB) UpdateSessionFolder(id string, folder *string) error {
	var f any
	if folder != nil && *folder != "" {
		f = *folder
	}
	_, err := d.Exec(`UPDATE sessions SET folder = ?, updated_at = ? WHERE id = ?`,
		f, utcNow(), id)
	return err
}

// UpdateSessionModel switches the model and endpoint of a session.
func (d *DB) UpdateSessionModel(id, model, endpointURL string, headers *string) error {
	_, err := d.Exec(`UPDATE sessions SET model = ?, endpoint_url = ?, headers = ?, updated_at = ? WHERE id = ?`,
		model, endpointURL, headers, utcNow(), id)
	return err
}

// ArchiveSession sets archived = true.
func (d *DB) ArchiveSession(id string) error {
	_, err := d.Exec(`UPDATE sessions SET archived = 1, updated_at = ? WHERE id = ?`,
		utcNow(), id)
	return err
}

// UnarchiveSession sets archived = false.
func (d *DB) UnarchiveSession(id string) error {
	_, err := d.Exec(`UPDATE sessions SET archived = 0, updated_at = ? WHERE id = ?`,
		utcNow(), id)
	return err
}

// SetSessionImportant sets the is_important flag.
func (d *DB) SetSessionImportant(id string, important bool) error {
	_, err := d.Exec(`UPDATE sessions SET is_important = ?, updated_at = ? WHERE id = ?`,
		important, utcNow(), id)
	return err
}

// SessionHasDocuments checks if a user has any active documents in the session.
func (d *DB) SessionHasDocuments(owner string) (map[string]bool, error) {
	rows, err := d.Query(`SELECT DISTINCT session_id FROM documents
	                       WHERE is_active = 1 AND current_content IS NOT NULL
	                         AND TRIM(current_content) != ''
	                         AND (owner = ? OR owner IS NULL)`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]bool)
	for rows.Next() {
		var sid sql.NullString
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		if sid.Valid {
			m[sid.String] = true
		}
	}
	return m, rows.Err()
}

// SessionHasImages checks if a user has gallery images linked to sessions.
func (d *DB) SessionHasImages(owner string) (map[string]bool, error) {
	rows, err := d.Query(`SELECT DISTINCT session_id FROM gallery_images
	                       WHERE session_id IS NOT NULL
	                         AND (owner = ? OR owner IS NULL)`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]bool)
	for rows.Next() {
		var sid sql.NullString
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		if sid.Valid {
			m[sid.String] = true
		}
	}
	return m, rows.Err()
}

// GetSessionMessages returns all messages for a session ordered by timestamp.
func (d *DB) GetSessionMessages(sessionID string) ([]ChatMessage, error) {
	rows, err := d.Query(`SELECT id, session_id, role, content, metadata, timestamp
	                       FROM chat_messages WHERE session_id = ?
	                       ORDER BY timestamp ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.MetaData, &m.Timestamp); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// PurgeStaleIncognito deletes Nobody/Incognito sessions older than the cutoff.
func (d *DB) PurgeStaleIncognito(cutoff time.Time) (int64, error) {
	// Delete messages first (FK cascade might handle this, but be explicit).
	d.Exec(`DELETE FROM chat_messages WHERE session_id IN (
	           SELECT id FROM sessions WHERE name IN ('Nobody', 'Incognito')
	           AND created_at < ?)`, cutoff)
	res, err := d.Exec(`DELETE FROM sessions WHERE name IN ('Nobody', 'Incognito')
	                     AND created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- helpers ---

func (d *DB) scanSessionRows(rows *sql.Rows, err error) ([]SessionListRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionListRow
	for rows.Next() {
		var r SessionListRow
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Model, &r.EndpointURL,
			&r.RAG, &r.Archived, &r.Folder,
			&r.TotalInputTokens, &r.TotalOutputTokens,
			&r.IsImportant, &r.CreatedAt, &r.UpdatedAt,
			&r.LastMessageAt, &r.LastAccessed, &r.Mode,
			&r.MessageCount, &r.CrewMemberID,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func utcNow() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

// UtcNow returns the current UTC time as a string (exported for other packages).
func UtcNow() string {
	return utcNow()
}

// generateUUID returns a random UUID v4 hex string (32 chars, no dashes).
// Matches Python's uuid.uuid4().hex format.
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%x", b)
}
