// Package routes contains the HTTP route handlers for the Odysseus Go server.
package routes

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"odysseus/internal/auth"
)

// AuthRoutes registers all /api/auth/* handlers on the given mux.
func AuthRoutes(mux *http.ServeMux, mgr *auth.Manager) {
	r := &authRoutes{
		mgr:           mgr,
		secureCookies: strings.EqualFold(os.Getenv("SECURE_COOKIES"), "true"),
		loginLimiter:  newRateLimiter(15, 60*time.Second),
		signupLimiter: newRateLimiter(3, 5*time.Minute),
		setupLimiter:  newRateLimiter(3, 5*time.Minute),
	}

	mux.HandleFunc("POST /api/auth/setup", r.handleSetup)
	mux.HandleFunc("POST /api/auth/signup", r.handleSignup)
	mux.HandleFunc("POST /api/auth/login", r.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", r.handleLogout)
	mux.HandleFunc("GET /api/auth/status", r.handleStatus)
	mux.HandleFunc("POST /api/auth/change-password", r.handleChangePassword)

	// 2FA
	mux.HandleFunc("POST /api/auth/2fa/setup", r.handleTOTPSetup)
	mux.HandleFunc("POST /api/auth/2fa/confirm", r.handleTOTPConfirm)
	mux.HandleFunc("POST /api/auth/2fa/disable", r.handleTOTPDisable)
	mux.HandleFunc("GET /api/auth/2fa/status", r.handleTOTPStatus)

	// Admin user management
	mux.HandleFunc("GET /api/auth/users", r.handleListUsers)
	mux.HandleFunc("POST /api/auth/users", r.handleCreateUser)
	mux.HandleFunc("DELETE /api/auth/users", r.handleDeleteUser)
	mux.HandleFunc("PUT /api/auth/users/{username}/privileges", r.handleUpdatePrivileges)
	mux.HandleFunc("PUT /api/auth/users/{username}/rename", r.handleRenameUser)

	// Signup toggle
	mux.HandleFunc("POST /api/auth/signup-toggle", r.handleSignupToggle)
	mux.HandleFunc("PUT /api/auth/open-signup", r.handleSetSignup)

	// Password scoring (new capability from goWebCtrl)
	mux.HandleFunc("POST /api/auth/password-score", r.handlePasswordScore)
}

type authRoutes struct {
	mgr           *auth.Manager
	secureCookies bool
	loginLimiter  *rateLimiter
	signupLimiter *rateLimiter
	setupLimiter  *rateLimiter
}

// ── Setup / Signup / Login / Logout ──────────────────────────────────

func (a *authRoutes) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !a.setupLimiter.allow(clientIP(r)) {
		jsonError(w, "Too many requests — try again later", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	if a.mgr.IsConfigured() {
		jsonError(w, "Already configured", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		jsonError(w, "Password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if !a.mgr.Setup(req.Username, req.Password) {
		jsonError(w, "Setup failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "message": "Admin account created"})
}

func (a *authRoutes) handleSignup(w http.ResponseWriter, r *http.Request) {
	if !a.signupLimiter.allow(clientIP(r)) {
		jsonError(w, "Too many requests — try again later", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	if !a.mgr.IsConfigured() {
		jsonError(w, "Run setup first", http.StatusBadRequest)
		return
	}
	if !a.mgr.SignupEnabled() {
		jsonError(w, "Registration is disabled. Ask an admin for an account.", http.StatusForbidden)
		return
	}
	if len(req.Password) < 8 {
		jsonError(w, "Password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if len(strings.TrimSpace(req.Username)) < 1 {
		jsonError(w, "Username is required", http.StatusBadRequest)
		return
	}
	if !a.mgr.CreateUser(req.Username, req.Password, false) {
		jsonError(w, "Username already taken", http.StatusConflict)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "message": "Account created"})
}

func (a *authRoutes) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.loginLimiter.allow(clientIP(r)) {
		jsonError(w, "Too many requests — try again later", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Username string  `json:"username"`
		Password string  `json:"password"`
		Remember bool    `json:"remember"`
		TOTPCode *string `json:"totp_code"`
	}
	req.Remember = true // default
	if !decodeJSON(r, &req, w) {
		return
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if !a.mgr.VerifyPassword(username, req.Password) {
		jsonError(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	// Check 2FA.
	if a.mgr.TOTPEnabled(username) {
		if req.TOTPCode == nil || *req.TOTPCode == "" {
			jsonOK(w, map[string]any{"ok": false, "requires_totp": true, "username": username})
			return
		}
		if !a.mgr.TOTPVerify(username, *req.TOTPCode) {
			jsonError(w, "Invalid 2FA code", http.StatusUnauthorized)
			return
		}
	}
	token := a.mgr.Sessions().CreateSession(username)
	cookie := &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies,
	}
	if req.Remember {
		cookie.MaxAge = 7 * 24 * 60 * 60 // 7 days
	}
	http.SetCookie(w, cookie)
	jsonOK(w, map[string]any{"ok": true, "username": username})
}

func (a *authRoutes) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookie); err == nil && cookie.Value != "" {
		a.mgr.Sessions().RevokeToken(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	jsonOK(w, map[string]any{"ok": true})
}

// ── Status ───────────────────────────────────────────────────────────

func (a *authRoutes) handleStatus(w http.ResponseWriter, r *http.Request) {
	token := cookieToken(r)
	result := a.mgr.Status(token)
	result["signup_enabled"] = a.mgr.SignupEnabled()
	jsonOK(w, result)
}

// ── Password ─────────────────────────────────────────────────────────

func (a *authRoutes) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	if len(req.NewPassword) < 8 {
		jsonError(w, "Password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if !a.mgr.ChangePassword(user, req.CurrentPassword, req.NewPassword) {
		jsonError(w, "Current password is incorrect", http.StatusBadRequest)
		return
	}
	// Revoke other sessions, keep current.
	currentToken := cookieToken(r)
	a.mgr.Sessions().RevokeUserSessions(user, currentToken)
	jsonOK(w, map[string]any{"ok": true})
}

func (a *authRoutes) handlePasswordScore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	score := ScorePassword(req.Password)
	jsonOK(w, map[string]any{"score": score})
}

// ── 2FA ──────────────────────────────────────────────────────────────

func (a *authRoutes) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	if a.mgr.TOTPEnabled(user) {
		jsonError(w, "2FA is already enabled", http.StatusBadRequest)
		return
	}
	secret := a.mgr.TOTPGenerateSecret(user)
	if secret == "" {
		jsonError(w, "Failed to generate secret", http.StatusInternalServerError)
		return
	}
	uri := a.mgr.TOTPGetProvisioningURI(user, secret)
	// QR code generation: the frontend can render from the URI directly,
	// or we can add server-side QR generation later. For now, return the
	// secret and URI — the Python side used qrcode lib for a base64 PNG.
	jsonOK(w, map[string]any{"secret": secret, "uri": uri})
}

func (a *authRoutes) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	backup := a.mgr.TOTPConfirmEnable(user, req.Code)
	if backup == nil {
		jsonError(w, "Invalid code — try again", http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "backup_codes": backup})
}

func (a *authRoutes) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	if !a.mgr.TOTPDisable(user, req.Password) {
		jsonError(w, "Invalid password", http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{"ok": true})
}

func (a *authRoutes) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	jsonOK(w, map[string]any{"enabled": a.mgr.TOTPEnabled(user)})
}

// ── Admin: User Management ───────────────────────────────────────────

func (a *authRoutes) handleListUsers(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" || !a.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}
	jsonOK(w, map[string]any{"users": a.mgr.ListUsers()})
}

func (a *authRoutes) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" || !a.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	if len(req.Password) < 8 {
		jsonError(w, "Password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if !a.mgr.CreateUser(req.Username, req.Password, req.IsAdmin) {
		jsonError(w, "Username already taken", http.StatusConflict)
		return
	}
	jsonOK(w, map[string]any{"ok": true})
}

func (a *authRoutes) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" || !a.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	if !a.mgr.DeleteUser(req.Username, user) {
		jsonError(w, "Cannot delete user", http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{"ok": true})
}

func (a *authRoutes) handleUpdatePrivileges(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" || !a.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}
	username := r.PathValue("username")
	var privileges map[string]any
	if !decodeJSON(r, &privileges, w) {
		return
	}
	if !a.mgr.SetPrivileges(username, privileges) {
		jsonError(w, "User not found or is admin", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "privileges": a.mgr.GetPrivileges(username)})
}

func (a *authRoutes) handleRenameUser(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" || !a.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}
	oldUsername := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	var req struct {
		Username string `json:"username"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	newUsername := strings.ToLower(strings.TrimSpace(req.Username))
	if newUsername == "" {
		jsonError(w, "Username required", http.StatusBadRequest)
		return
	}
	if oldUsername == newUsername {
		jsonOK(w, map[string]any{"ok": true, "username": newUsername, "renamed_self": oldUsername == user})
		return
	}
	if !a.mgr.RenameUser(oldUsername, newUsername, user) {
		jsonError(w, "Cannot rename user", http.StatusBadRequest)
		return
	}
	// Note: The Python side also renames owner references in DB tables,
	// prefs, deep research, memory, skills, etc. In Go Phase 2 those
	// resources are still owned by Python, so the rename_user proxy call
	// to Python handles them. When Go takes over DB in Phase 3, this
	// route will need to do the owner migration directly.
	jsonOK(w, map[string]any{"ok": true, "username": newUsername, "renamed_self": oldUsername == user})
}

func (a *authRoutes) handleSignupToggle(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" || !a.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}
	a.mgr.SetSignupEnabled(!a.mgr.SignupEnabled())
	jsonOK(w, map[string]any{"ok": true, "signup_enabled": a.mgr.SignupEnabled()})
}

func (a *authRoutes) handleSetSignup(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == "" || !a.mgr.IsAdmin(user) {
		jsonError(w, "Admin only", http.StatusForbidden)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(r, &req, w) {
		return
	}
	a.mgr.SetSignupEnabled(req.Enabled)
	jsonOK(w, map[string]any{"ok": true, "signup_enabled": a.mgr.SignupEnabled()})
}

// ── Helpers ──────────────────────────────────────────────────────────

func (a *authRoutes) currentUser(r *http.Request) string {
	// Check if auth middleware already resolved the user.
	if u, ok := r.Context().Value(CtxKeyCurrentUser).(string); ok && u != "" {
		return u
	}
	// Fallback: check cookie directly.
	return a.mgr.Sessions().GetUsernameForToken(cookieToken(r))
}

func cookieToken(r *http.Request) string {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	// Strip port from RemoteAddr.
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

// ── Context keys ─────────────────────────────────────────────────────
// Exported so the auth middleware (in server package) can set them.

type contextKey int

const (
	CtxKeyCurrentUser  contextKey = iota
	CtxKeyAPIToken
	CtxKeyAPITokenOwner
	CtxKeyAPITokenScopes
)

// ── JSON helpers ─────────────────────────────────────────────────────

func decodeJSON(r *http.Request, v any, w http.ResponseWriter) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// ── Rate limiter ─────────────────────────────────────────────────────

type rateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	max      int
	window   time.Duration
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		requests: make(map[string][]time.Time),
		max:      max,
		window:   window,
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Prune old entries.
	times := rl.requests[key]
	n := 0
	for _, t := range times {
		if t.After(cutoff) {
			times[n] = t
			n++
		}
	}
	times = times[:n]

	if len(times) >= rl.max {
		rl.requests[key] = times
		return false
	}
	rl.requests[key] = append(times, now)
	return true
}

// ── Password scoring (zxcvbn) ────────────────────────────────────────

// ScorePassword returns a zxcvbn-style strength score (0-4).
// Uses the zxcvbn-go library from goWebCtrl.
func ScorePassword(password string) int {
	return scorePasswordImpl(password)
}

// scorePasswordImpl is the actual implementation, set at init time
// based on whether zxcvbn is available. Default: simple length heuristic.
var scorePasswordImpl = scorePasswordFallback

func scorePasswordFallback(password string) int {
	// Simple heuristic when zxcvbn is not yet wired in.
	n := len(password)
	switch {
	case n < 4:
		return 0
	case n < 8:
		return 1
	case n < 12:
		return 2
	case n < 16:
		return 3
	default:
		return 4
	}
}
