package db

import "database/sql"

// ApiTokenRow is a lightweight projection for bearer token validation.
type ApiTokenRow struct {
	ID          string
	TokenHash   string
	TokenPrefix string
	Owner       sql.NullString
	Scopes      string
	IsActive    bool
}

// ListActiveTokensByPrefix returns active API tokens matching a prefix.
func (d *DB) ListActiveTokensByPrefix(prefix string) ([]ApiTokenRow, error) {
	rows, err := d.Query(
		`SELECT id, token_hash, token_prefix, owner, COALESCE(scopes, 'chat')
		 FROM api_tokens WHERE is_active = 1 AND token_prefix = ?`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ApiTokenRow
	for rows.Next() {
		var t ApiTokenRow
		if err := rows.Scan(&t.ID, &t.TokenHash, &t.TokenPrefix, &t.Owner, &t.Scopes); err != nil {
			return nil, err
		}
		t.IsActive = true
		result = append(result, t)
	}
	return result, rows.Err()
}

// TouchTokenLastUsed updates last_used_at for an API token.
func (d *DB) TouchTokenLastUsed(tokenID string) {
	d.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, utcNow(), tokenID)
}
