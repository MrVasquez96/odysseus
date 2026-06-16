package routes

import (
	"net/http"

	"odysseus/internal/db"
)

type emailRoutes struct {
	db *db.DB
}

// EmailRoutes registers email read routes served by Go.
// All IMAP/SMTP, send/reply, AI, and password-encrypted writes stay proxied to Python.
func EmailRoutes(mux *http.ServeMux, database *db.DB) {
	r := &emailRoutes{db: database}
	mux.HandleFunc("GET /api/email/accounts", r.listAccounts)
}

func (r *emailRoutes) listAccounts(w http.ResponseWriter, req *http.Request) {
	user := currentUser(req)
	accounts, err := r.db.ListEmailAccounts(user)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	result := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		imapPort := int64(993)
		if a.ImapPort.Valid {
			imapPort = a.ImapPort.Int64
		}
		smtpPort := int64(465)
		if a.SmtpPort.Valid {
			smtpPort = a.SmtpPort.Int64
		}
		smtpSecurity := smtpSecurityMode(db.StringVal(a.SmtpSecurity), smtpPort)

		result = append(result, map[string]any{
			"id":                a.ID,
			"name":              a.Name,
			"is_default":        a.IsDefault,
			"enabled":           a.Enabled,
			"imap_host":         db.StringVal(a.ImapHost),
			"imap_port":         imapPort,
			"imap_user":         db.StringVal(a.ImapUser),
			"imap_starttls":     a.ImapStarttls.Valid && a.ImapStarttls.Bool,
			"smtp_host":         db.StringVal(a.SmtpHost),
			"smtp_port":         smtpPort,
			"smtp_security":     smtpSecurity,
			"smtp_user":         db.StringVal(a.SmtpUser),
			"from_address":      db.StringVal(a.FromAddress),
			"has_imap_password": a.ImapPassword.Valid && a.ImapPassword.String != "",
			"has_smtp_password": a.SmtpPassword.Valid && a.SmtpPassword.String != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": result})
}

// smtpSecurityMode normalizes the SMTP security mode,
// matching Python's _smtp_security_mode helper.
func smtpSecurityMode(security string, port int64) string {
	switch security {
	case "ssl", "starttls", "none":
		return security
	default:
		if port == 587 {
			return "starttls"
		}
		return "ssl"
	}
}
