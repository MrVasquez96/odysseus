package db

import "database/sql"

type CalendarRow struct {
	ID        string
	Owner     sql.NullString
	Name      string
	Color     string
	Source    string
	AccountID sql.NullString
	CreatedAt sql.NullString
	UpdatedAt sql.NullString
}

func (d *DB) ListCalendars(owner string) ([]CalendarRow, error) {
	q := `SELECT id, owner, name, color, source, account_id, created_at, updated_at
		FROM calendars WHERE owner = ? ORDER BY created_at ASC`
	rows, err := d.Query(q, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CalendarRow
	for rows.Next() {
		var c CalendarRow
		if err := rows.Scan(&c.ID, &c.Owner, &c.Name, &c.Color, &c.Source,
			&c.AccountID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// EnsureDefaultCalendar creates a "Personal" calendar if the owner has none.
// Returns true if a default was created.
func (d *DB) EnsureDefaultCalendar(owner string) (bool, error) {
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM calendars WHERE owner = ?", owner).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	now := utcNow()
	id := generateUUID()
	_, err = d.Exec(
		`INSERT INTO calendars (id, owner, name, color, source, created_at, updated_at)
		VALUES (?, ?, 'Personal', '#5b8abf', 'local', ?, ?)`,
		id, owner, now, now)
	return err == nil, err
}

func (d *DB) CreateCalendar(id, owner, name, color string) error {
	now := utcNow()
	_, err := d.Exec(
		`INSERT INTO calendars (id, owner, name, color, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'local', ?, ?)`,
		id, owner, name, color, now, now)
	return err
}

func (d *DB) UpdateCalendar(id string, name, color *string) error {
	if name != nil {
		if _, err := d.Exec("UPDATE calendars SET name = ?, updated_at = ? WHERE id = ?",
			*name, utcNow(), id); err != nil {
			return err
		}
	}
	if color != nil {
		if _, err := d.Exec("UPDATE calendars SET color = ?, updated_at = ? WHERE id = ?",
			*color, utcNow(), id); err != nil {
			return err
		}
	}
	return nil
}

// GetCalendarOwner returns the owner of a calendar, for authorization checks.
func (d *DB) GetCalendarOwner(calID string) (string, error) {
	var owner sql.NullString
	err := d.QueryRow("SELECT owner FROM calendars WHERE id = ?", calID).Scan(&owner)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return StringVal(owner), err
}

// DeleteCalendarWithEvents deletes all events for the calendar, then the calendar itself.
func (d *DB) DeleteCalendarWithEvents(calID string) error {
	return d.Tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM calendar_events WHERE calendar_id = ?", calID); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM calendars WHERE id = ?", calID)
		return err
	})
}
