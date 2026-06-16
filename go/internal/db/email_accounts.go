package db

import "database/sql"

type EmailAccountRow struct {
	ID           string
	Owner        sql.NullString
	Name         string
	IsDefault    bool
	Enabled      bool
	ImapHost     sql.NullString
	ImapPort     sql.NullInt64
	ImapUser     sql.NullString
	ImapPassword sql.NullString
	ImapStarttls sql.NullBool
	SmtpHost     sql.NullString
	SmtpPort     sql.NullInt64
	SmtpSecurity sql.NullString
	SmtpUser     sql.NullString
	SmtpPassword sql.NullString
	FromAddress  sql.NullString
	CreatedAt    sql.NullString
	UpdatedAt    sql.NullString
}

const emailAccountCols = `id, owner, name, is_default, enabled,
	imap_host, imap_port, imap_user, imap_password, imap_starttls,
	smtp_host, smtp_port, smtp_security, smtp_user, smtp_password,
	from_address, created_at, updated_at`

func scanEmailAccount(row interface{ Scan(...any) error }) (EmailAccountRow, error) {
	var r EmailAccountRow
	err := row.Scan(
		&r.ID, &r.Owner, &r.Name, &r.IsDefault, &r.Enabled,
		&r.ImapHost, &r.ImapPort, &r.ImapUser, &r.ImapPassword, &r.ImapStarttls,
		&r.SmtpHost, &r.SmtpPort, &r.SmtpSecurity, &r.SmtpUser, &r.SmtpPassword,
		&r.FromAddress, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

// ListEmailAccounts returns accounts scoped to the given owner.
// Unowned legacy rows matching the owner's mailbox are also included.
func (d *DB) ListEmailAccounts(owner string) ([]EmailAccountRow, error) {
	q := "SELECT " + emailAccountCols + " FROM email_accounts WHERE 1=1"
	args := []any{}
	if owner != "" {
		q += " AND (owner = ? OR ((owner IS NULL OR owner = '') AND (imap_user = ? OR from_address = ?)))"
		args = append(args, owner, owner, owner)
	}
	q += " ORDER BY is_default DESC, created_at ASC"

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EmailAccountRow
	for rows.Next() {
		r, err := scanEmailAccount(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
