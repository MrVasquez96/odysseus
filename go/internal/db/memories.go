package db

// MemoryRow represents a row from the memories table.
type MemoryRow struct {
	ID        string
	Text      string
	Category  string
	Source    string
	Owner     string
	Timestamp int64
}

// ListMemoriesForGraph returns all memories for a given owner,
// ordered by timestamp descending. Used by the bubble graph endpoint.
func (d *DB) ListMemoriesForGraph(owner string) ([]MemoryRow, error) {
	q := `SELECT id, text, COALESCE(category, 'fact'), COALESCE(source, 'user'),
	             COALESCE(owner, ''), COALESCE(timestamp, 0)
	      FROM memories
	      WHERE owner = ? OR owner IS NULL
	      ORDER BY timestamp DESC`
	rows, err := d.Query(q, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MemoryRow
	for rows.Next() {
		var m MemoryRow
		if err := rows.Scan(&m.ID, &m.Text, &m.Category, &m.Source, &m.Owner, &m.Timestamp); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
