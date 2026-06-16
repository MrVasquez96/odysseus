// Package auth implements multi-user password + session-token authentication.
//
// User records are stored in data/auth.json (bcrypt-hashed passwords).
// Session tokens are stored in data/sessions.json (64-char hex, 7-day TTL).
// This is a direct port of core/auth.py's AuthManager.
package auth

import (
	cryptoRand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// DefaultPrivileges for regular (non-admin) users.
var DefaultPrivileges = map[string]any{
	"can_use_agent":             true,
	"can_use_browser":           true,
	"can_use_bash":              false,
	"can_use_documents":         true,
	"can_use_research":          true,
	"can_generate_images":       true,
	"can_manage_memory":         true,
	"max_messages_per_day":      0,
	"allowed_models":            []string{},
	"allowed_models_restricted": false,
	"block_all_models":          false,
}

// AdminPrivileges — admins get everything enabled.
var AdminPrivileges = map[string]any{
	"can_use_agent":             true,
	"can_use_browser":           true,
	"can_use_bash":              true,
	"can_use_documents":         true,
	"can_use_research":          true,
	"can_generate_images":       true,
	"can_manage_memory":         true,
	"max_messages_per_day":      0,
	"allowed_models":            []string{},
	"allowed_models_restricted": false,
	"block_all_models":          false,
}

// ReservedUsernames that cannot be created or renamed into.
// Matches core/auth.py RESERVED_USERNAMES.
var ReservedUsernames = map[string]bool{
	"internal-tool": true,
	"api":           true,
	"demo":          true,
	"system":        true,
}

const (
	// SessionCookie is the cookie name used for browser auth.
	// Matches routes/auth_routes.py SESSION_COOKIE.
	SessionCookie = "odysseus_session"

	// BcryptCost for password hashing.
	BcryptCost = bcrypt.DefaultCost
)

// UserRecord mirrors the per-user entry in auth.json.
type UserRecord struct {
	PasswordHash    string         `json:"password_hash"`
	Created         float64        `json:"created"`
	IsAdmin         bool           `json:"is_admin"`
	Privileges      map[string]any `json:"privileges,omitempty"`
	TOTPSecret      string         `json:"totp_secret,omitempty"`
	TOTPPending     string         `json:"totp_secret_pending,omitempty"`
	TOTPEnabled     bool           `json:"totp_enabled,omitempty"`
	TOTPBackupCodes []string       `json:"totp_backup_codes,omitempty"`
}

// authConfig is the on-disk auth.json structure.
type authConfig struct {
	Users         map[string]*UserRecord `json:"users,omitempty"`
	SignupEnabled bool                   `json:"signup_enabled,omitempty"`
}

// Manager handles multi-user password + session-token auth.
// Thread-safe for concurrent HTTP requests.
type Manager struct {
	authPath     string
	sessionsPath string

	mu     sync.RWMutex // guards config
	config authConfig

	sessions *SessionStore
}

// New creates a Manager, loading from the given auth.json path.
// Sessions are stored in sessions.json in the same directory.
func New(authPath string) *Manager {
	m := &Manager{
		authPath:     authPath,
		sessionsPath: filepath.Join(filepath.Dir(authPath), "sessions.json"),
	}
	m.load()
	m.sessions = newSessionStore(m.sessionsPath, m)
	m.migrateSingleUser()
	m.dropReservedUsers()
	m.migrateLegacyAdminRole()
	return m
}

// IsConfigured returns true if at least one user exists.
func (m *Manager) IsConfigured() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.config.Users) > 0
}

// SignupEnabled returns whether open registration is on.
func (m *Manager) SignupEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.SignupEnabled
}

// SetSignupEnabled toggles open registration.
func (m *Manager) SetSignupEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.SignupEnabled = enabled
	m.save()
}

// Users returns a snapshot of all usernames.
func (m *Manager) Users() map[string]*UserRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*UserRecord, len(m.config.Users))
	for k, v := range m.config.Users {
		out[k] = v
	}
	return out
}

// UserExists checks if a username exists (case-insensitive).
func (m *Manager) UserExists(username string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.config.Users[strings.ToLower(strings.TrimSpace(username))]
	return ok
}

// ── Account management ───────────────────────────────────────────────

// Setup creates the initial admin account. Only works if no users exist.
func (m *Manager) Setup(username, password string) bool {
	m.mu.Lock()
	if len(m.config.Users) > 0 {
		m.mu.Unlock()
		return false
	}
	m.mu.Unlock()
	return m.CreateUser(username, password, true)
}

// CreateUser creates a new user account. Returns false if username taken or reserved.
func (m *Manager) CreateUser(username, password string, isAdmin bool) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" || ReservedUsernames[username] {
		return false
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		log.Printf("auth: bcrypt hash failed: %v", err)
		return false
	}

	privs := make(map[string]any)
	if isAdmin {
		for k, v := range AdminPrivileges {
			privs[k] = v
		}
	} else {
		for k, v := range DefaultPrivileges {
			privs[k] = v
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.config.Users[username]; exists {
		return false
	}
	if m.config.Users == nil {
		m.config.Users = make(map[string]*UserRecord)
	}
	m.config.Users[username] = &UserRecord{
		PasswordHash: string(hash),
		Created:      float64(time.Now().Unix()),
		IsAdmin:      isAdmin,
		Privileges:   privs,
	}
	m.save()
	log.Printf("auth: created user '%s' (admin=%v)", username, isAdmin)
	return true
}

// DeleteUser removes a user. Only admins can delete, can't delete self.
// Revokes all sessions for the deleted user.
func (m *Manager) DeleteUser(username, requestingUser string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	requestingUser = strings.ToLower(strings.TrimSpace(requestingUser))

	m.mu.Lock()
	if _, exists := m.config.Users[username]; !exists {
		m.mu.Unlock()
		return false
	}
	if username == requestingUser {
		m.mu.Unlock()
		return false
	}
	reqUser := m.config.Users[requestingUser]
	if reqUser == nil || !reqUser.IsAdmin {
		m.mu.Unlock()
		return false
	}
	delete(m.config.Users, username)
	m.save()
	m.mu.Unlock()

	// Purge all sessions belonging to this user.
	m.sessions.RevokeUserSessions(username, "")
	log.Printf("auth: deleted user '%s' (by %s)", username, requestingUser)
	return true
}

// RenameUser renames a user in auth config and active sessions.
func (m *Manager) RenameUser(oldUsername, newUsername, requestingUser string) bool {
	oldUsername = strings.ToLower(strings.TrimSpace(oldUsername))
	newUsername = strings.ToLower(strings.TrimSpace(newUsername))
	requestingUser = strings.ToLower(strings.TrimSpace(requestingUser))

	if oldUsername == "" || newUsername == "" || ReservedUsernames[newUsername] {
		return false
	}

	m.mu.Lock()
	if _, exists := m.config.Users[oldUsername]; !exists {
		m.mu.Unlock()
		return false
	}
	if _, exists := m.config.Users[newUsername]; exists {
		m.mu.Unlock()
		return false
	}
	reqUser := m.config.Users[requestingUser]
	if reqUser == nil || !reqUser.IsAdmin {
		m.mu.Unlock()
		return false
	}
	m.config.Users[newUsername] = m.config.Users[oldUsername]
	delete(m.config.Users, oldUsername)
	m.save()
	m.mu.Unlock()

	// Update session usernames.
	renamed := m.sessions.RenameUser(oldUsername, newUsername)
	log.Printf("auth: renamed '%s' -> '%s' (by %s); updated %d session(s)",
		oldUsername, newUsername, requestingUser, renamed)
	return true
}

// IsAdmin checks if a user is an admin.
func (m *Manager) IsAdmin(username string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u := m.config.Users[strings.ToLower(strings.TrimSpace(username))]
	return u != nil && u.IsAdmin
}

// ListUsers returns user info for the admin panel.
func (m *Manager) ListUsers() []map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []map[string]any
	for username, rec := range m.config.Users {
		result = append(result, map[string]any{
			"username":   username,
			"is_admin":   rec.IsAdmin,
			"privileges": m.getPrivilegesLocked(username),
		})
	}
	return result
}

// GetPrivileges returns effective privileges for a user.
func (m *Manager) GetPrivileges(username string) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getPrivilegesLocked(username)
}

func (m *Manager) getPrivilegesLocked(username string) map[string]any {
	user := m.config.Users[strings.ToLower(strings.TrimSpace(username))]
	if user == nil {
		return nil
	}
	if user.IsAdmin {
		out := make(map[string]any, len(AdminPrivileges))
		for k, v := range AdminPrivileges {
			out[k] = v
		}
		return out
	}
	// Merge stored with defaults.
	out := make(map[string]any, len(DefaultPrivileges))
	for k, v := range DefaultPrivileges {
		out[k] = v
	}
	for k, v := range user.Privileges {
		out[k] = v
	}
	return out
}

// SetPrivileges updates privileges for a non-admin user.
func (m *Manager) SetPrivileges(username string, privileges map[string]any) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	m.mu.Lock()
	defer m.mu.Unlock()
	user := m.config.Users[username]
	if user == nil || user.IsAdmin {
		return false
	}
	current := m.getPrivilegesLocked(username)
	for k, v := range privileges {
		if _, known := DefaultPrivileges[k]; known {
			current[k] = v
		}
	}
	user.Privileges = current
	m.save()
	return true
}

// ── Password verification ────────────────────────────────────────────

// VerifyPassword checks a username/password pair.
func (m *Manager) VerifyPassword(username, password string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	m.mu.RLock()
	user := m.config.Users[username]
	m.mu.RUnlock()
	if user == nil {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	return err == nil
}

// ChangePassword changes a user's password after verifying the current one.
func (m *Manager) ChangePassword(username, currentPassword, newPassword string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	m.mu.RLock()
	user := m.config.Users[username]
	m.mu.RUnlock()
	if user == nil {
		return false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); err != nil {
		return false
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), BcryptCost)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.Users[username].PasswordHash = string(hash)
	m.save()
	return true
}

// Sessions returns the session store for external access.
func (m *Manager) Sessions() *SessionStore {
	return m.sessions
}

// Status returns auth state for the /api/auth/status endpoint.
func (m *Manager) Status(token string) map[string]any {
	username := m.sessions.GetUsernameForToken(token)
	authenticated := username != ""
	result := map[string]any{
		"configured":    m.IsConfigured(),
		"authenticated": authenticated,
		"username":      username,
		"is_admin":      false,
	}
	if authenticated {
		result["is_admin"] = m.IsAdmin(username)
		result["privileges"] = m.GetPrivileges(username)
	}
	return result
}

// ── Internal helpers ─────────────────────────────────────────────────

// NormalizeKnownUsername returns a normalized username only if it exists.
// Matches core/auth.py normalize_known_username.
func (m *Manager) NormalizeKnownUsername(username string) string {
	key := strings.ToLower(strings.TrimSpace(username))
	if key == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.config.Users[key]; !ok {
		return ""
	}
	return key
}

// CompareTokens performs constant-time comparison of two token strings.
func CompareTokens(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// GenerateInternalToken creates a random token for agent loopback calls.
// Matches core/middleware.py INTERNAL_TOOL_TOKEN = secrets.token_hex(32).
func GenerateInternalToken() string {
	b := make([]byte, 32)
	if _, err := cryptoRand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ── Persistence ──────────────────────────────────────────────────────

func (m *Manager) load() {
	data, err := os.ReadFile(m.authPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("auth: failed to load %s: %v", m.authPath, err)
		}
		m.config = authConfig{Users: make(map[string]*UserRecord)}
		return
	}
	if err := json.Unmarshal(data, &m.config); err != nil {
		log.Printf("auth: failed to parse %s: %v", m.authPath, err)
		m.config = authConfig{Users: make(map[string]*UserRecord)}
		return
	}
	// Normalize usernames to lowercase.
	if m.config.Users != nil {
		normalized := make(map[string]*UserRecord, len(m.config.Users))
		for k, v := range m.config.Users {
			normalized[strings.ToLower(strings.TrimSpace(k))] = v
		}
		m.config.Users = normalized
	} else {
		m.config.Users = make(map[string]*UserRecord)
	}
	log.Printf("auth: loaded %d user(s) from %s", len(m.config.Users), m.authPath)
}

// save writes auth.json atomically. Caller must hold m.mu write lock.
func (m *Manager) save() {
	data, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		log.Printf("auth: marshal failed: %v", err)
		return
	}
	if err := atomicWrite(m.authPath, data); err != nil {
		log.Printf("auth: save failed: %v", err)
	}
}

// atomicWrite writes data to a temp file then renames, matching core/atomic_io.py.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ── Migrations ───────────────────────────────────────────────────────

func (m *Manager) migrateSingleUser() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for old single-user format: has password_hash at top level, no users map.
	// We unmarshal raw to check for this legacy shape.
	data, err := os.ReadFile(m.authPath)
	if err != nil {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	if _, hasPwHash := raw["password_hash"]; !hasPwHash {
		return
	}
	if _, hasUsers := raw["users"]; hasUsers {
		return
	}
	// Legacy single-user format detected.
	var legacy struct {
		Username     string `json:"username"`
		PasswordHash string `json:"password_hash"`
	}
	json.Unmarshal(data, &legacy)
	username := strings.ToLower(strings.TrimSpace(legacy.Username))
	if username == "" {
		username = "admin"
	}
	if ReservedUsernames[username] {
		log.Printf("auth: migrating legacy reserved username '%s' to 'admin'", username)
		username = "admin"
	}
	m.config = authConfig{
		Users: map[string]*UserRecord{
			username: {
				PasswordHash: legacy.PasswordHash,
				Created:      float64(time.Now().Unix()),
				IsAdmin:      true,
			},
		},
	}
	m.save()
	log.Printf("auth: migrated single-user to multi-user (admin: %s)", username)
}

func (m *Manager) dropReservedUsers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var removed []string
	for username := range m.config.Users {
		if ReservedUsernames[username] {
			removed = append(removed, username)
		}
	}
	if len(removed) > 0 {
		for _, u := range removed {
			delete(m.config.Users, u)
		}
		m.save()
		log.Printf("auth: removed reserved username(s): %s", strings.Join(removed, ", "))
	}
}

func (m *Manager) migrateLegacyAdminRole() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check raw data for role="admin" marker from setup.py.
	data, err := os.ReadFile(m.authPath)
	if err != nil {
		return
	}
	var rawConfig struct {
		Users map[string]map[string]any `json:"users"`
	}
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		return
	}
	changed := false
	for username, rawUser := range rawConfig.Users {
		if rawUser["role"] == "admin" {
			if user := m.config.Users[username]; user != nil && !user.IsAdmin {
				user.IsAdmin = true
				changed = true
				log.Printf("auth: migrated legacy admin role for '%s'", username)
			}
		}
	}
	if changed {
		m.save()
	}
}
