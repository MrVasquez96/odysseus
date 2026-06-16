package db

import (
	"database/sql"
	"encoding/json"
)

// ModelEndpointRow represents a row from the model_endpoints table.
type ModelEndpointRow struct {
	ID                   string
	Name                 string
	BaseURL              string
	APIKey               sql.NullString // encrypted, never exposed
	IsEnabled            bool
	HiddenModels         sql.NullString // JSON list
	CachedModels         sql.NullString // JSON list
	PinnedModels         sql.NullString // JSON list
	ModelType            sql.NullString // "llm" or "image"
	EndpointKind         sql.NullString // "auto", "local", "api", "proxy"
	ModelRefreshMode     sql.NullString
	ModelRefreshInterval sql.NullInt64
	ModelRefreshTimeout  sql.NullInt64
	SupportsTools        sql.NullBool
	Owner                sql.NullString
	ProviderAuthID       sql.NullString
	CreatedAt            sql.NullString
	UpdatedAt            sql.NullString
}

const modelEndpointCols = `id, name, base_url, api_key, is_enabled,
	hidden_models, cached_models, pinned_models, model_type,
	endpoint_kind, model_refresh_mode, model_refresh_interval,
	model_refresh_timeout, supports_tools, owner, provider_auth_id,
	created_at, updated_at`

func scanModelEndpoint(row interface{ Scan(...any) error }) (ModelEndpointRow, error) {
	var r ModelEndpointRow
	err := row.Scan(
		&r.ID, &r.Name, &r.BaseURL, &r.APIKey, &r.IsEnabled,
		&r.HiddenModels, &r.CachedModels, &r.PinnedModels, &r.ModelType,
		&r.EndpointKind, &r.ModelRefreshMode, &r.ModelRefreshInterval,
		&r.ModelRefreshTimeout, &r.SupportsTools, &r.Owner, &r.ProviderAuthID,
		&r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

// ListEnabledEndpoints returns enabled model endpoints visible to the given owner.
// Admin sees all; regular users see their own + null-owner (shared/legacy) endpoints.
func (d *DB) ListEnabledEndpoints(owner string, isAdmin bool) ([]ModelEndpointRow, error) {
	q := "SELECT " + modelEndpointCols + " FROM model_endpoints WHERE is_enabled = 1"
	args := []any{}
	if owner != "" && !isAdmin {
		q += " AND (owner = ? OR owner IS NULL OR owner = '')"
		args = append(args, owner)
	}
	q += " ORDER BY created_at ASC"
	return d.queryEndpoints(q, args...)
}

// ListAllEndpoints returns all endpoints (admin view).
func (d *DB) ListAllEndpoints() ([]ModelEndpointRow, error) {
	q := "SELECT " + modelEndpointCols + " FROM model_endpoints ORDER BY created_at ASC"
	return d.queryEndpoints(q)
}

// GetEndpoint returns a single endpoint by ID.
func (d *DB) GetEndpoint(id string) (ModelEndpointRow, error) {
	q := "SELECT " + modelEndpointCols + " FROM model_endpoints WHERE id = ?"
	return scanModelEndpoint(d.QueryRow(q, id))
}

// GetEnabledEndpoint returns an enabled endpoint by ID, optionally scoped to an owner.
func (d *DB) GetEnabledEndpoint(id, owner string, isAdmin bool) (ModelEndpointRow, bool, error) {
	q := "SELECT " + modelEndpointCols + " FROM model_endpoints WHERE id = ? AND is_enabled = 1"
	args := []any{id}
	if owner != "" && !isAdmin {
		q += " AND (owner = ? OR owner IS NULL OR owner = '')"
		args = append(args, owner)
	}
	r, err := scanModelEndpoint(d.QueryRow(q, args...))
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	return r, err == nil, err
}

// FirstEnabledEndpoint returns the first enabled endpoint for the user.
func (d *DB) FirstEnabledEndpoint(owner string, isAdmin bool) (ModelEndpointRow, bool, error) {
	q := "SELECT " + modelEndpointCols + " FROM model_endpoints WHERE is_enabled = 1"
	args := []any{}
	if owner != "" && !isAdmin {
		q += " AND (owner = ? OR owner IS NULL OR owner = '')"
		args = append(args, owner)
	}
	q += " ORDER BY created_at ASC LIMIT 1"
	r, err := scanModelEndpoint(d.QueryRow(q, args...))
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	return r, err == nil, err
}

func (d *DB) queryEndpoints(q string, args ...any) ([]ModelEndpointRow, error) {
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ModelEndpointRow
	for rows.Next() {
		r, err := scanModelEndpoint(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ParseModelList parses a JSON model list string.
func ParseModelList(raw sql.NullString) []string {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var list []string
	if json.Unmarshal([]byte(raw.String), &list) != nil {
		return nil
	}
	return list
}
