package db

import "database/sql"

// FullApiTokenRow is used for admin CRUD (not the lightweight auth row).
type FullApiTokenRow struct {
	ID          string
	Name        string
	Owner       sql.NullString
	TokenHash   string
	TokenPrefix string
	Scopes      string
	IsActive    bool
	LastUsedAt  sql.NullString
	CreatedAt   sql.NullString
}

func (d *DB) ListAllTokens() ([]FullApiTokenRow, error) {
	rows, err := d.Query(
		`SELECT id, name, owner, token_hash, token_prefix,
			COALESCE(scopes, 'chat'), is_active,
			last_used_at, created_at
		FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FullApiTokenRow
	for rows.Next() {
		var t FullApiTokenRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Owner, &t.TokenHash, &t.TokenPrefix,
			&t.Scopes, &t.IsActive, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (d *DB) GetToken(tokenID string) (*FullApiTokenRow, error) {
	row := d.QueryRow(
		`SELECT id, name, owner, token_hash, token_prefix,
			COALESCE(scopes, 'chat'), is_active,
			last_used_at, created_at
		FROM api_tokens WHERE id = ?`, tokenID)
	var t FullApiTokenRow
	err := row.Scan(&t.ID, &t.Name, &t.Owner, &t.TokenHash, &t.TokenPrefix,
		&t.Scopes, &t.IsActive, &t.LastUsedAt, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) CreateToken(id, name, owner, tokenHash, tokenPrefix, scopes string) error {
	now := utcNow()
	_, err := d.Exec(
		`INSERT INTO api_tokens (id, name, owner, token_hash, token_prefix, scopes, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, name, owner, tokenHash, tokenPrefix, scopes, now, now)
	return err
}

func (d *DB) UpdateTokenName(tokenID, name string) error {
	_, err := d.Exec("UPDATE api_tokens SET name = ? WHERE id = ?", name, tokenID)
	return err
}

func (d *DB) UpdateTokenScopes(tokenID, scopes string) error {
	_, err := d.Exec("UPDATE api_tokens SET scopes = ? WHERE id = ?", scopes, tokenID)
	return err
}

func (d *DB) DeleteToken(tokenID string) error {
	_, err := d.Exec("DELETE FROM api_tokens WHERE id = ?", tokenID)
	return err
}
