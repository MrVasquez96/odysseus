package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// TokenTTL matches core/auth.py TOKEN_TTL (7 days).
	TokenTTL = 7 * 24 * time.Hour

	// tokenBytes is the number of random bytes for a session token.
	// secrets.token_hex(32) produces 32 bytes → 64 hex chars.
	tokenBytes = 32
)

// Session represents an active login session.
type Session struct {
	Username string  `json:"username"`
	Expiry   float64 `json:"expiry"` // Unix timestamp, matches Python's time.time()
}

// SessionStore manages session tokens with thread-safe operations.
type SessionStore struct {
	path     string
	mu       sync.RWMutex
	sessions map[string]*Session // token → session
	auth     *Manager            // back-ref for user existence checks
}

func newSessionStore(path string, auth *Manager) *SessionStore {
	s := &SessionStore{
		path:     path,
		sessions: make(map[string]*Session),
		auth:     auth,
	}
	s.load()
	return s
}

// CreateSession issues a new session token for an already-verified user.
// Matches AuthManager.create_session_trusted in Python.
func (s *SessionStore) CreateSession(username string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	token := generateToken()
	sess := &Session{
		Username: username,
		Expiry:   float64(time.Now().Add(TokenTTL).Unix()),
	}
	s.mu.Lock()
	s.sessions[token] = sess
	s.mu.Unlock()
	s.save()
	return token
}

// ValidateToken checks if a session token is valid (exists, not expired, user still exists).
func (s *SessionStore) ValidateToken(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return false
	}
	now := float64(time.Now().Unix())
	if now > sess.Expiry {
		delete(s.sessions, token)
		go s.save()
		return false
	}
	// Check user still exists (security: deleted user's cookie must not authenticate).
	if !s.auth.UserExists(sess.Username) {
		delete(s.sessions, token)
		go s.save()
		return false
	}
	return true
}

// GetUsernameForToken returns the username for a valid session token.
func (s *SessionStore) GetUsernameForToken(token string) string {
	if token == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return ""
	}
	now := float64(time.Now().Unix())
	if now > sess.Expiry {
		delete(s.sessions, token)
		go s.save()
		return ""
	}
	if !s.auth.UserExists(sess.Username) {
		delete(s.sessions, token)
		go s.save()
		return ""
	}
	return sess.Username
}

// RevokeToken removes a single session token.
func (s *SessionStore) RevokeToken(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
	s.save()
}

// RevokeUserSessions revokes all sessions for a user, optionally preserving one token.
// Returns the number of sessions revoked.
func (s *SessionStore) RevokeUserSessions(username, exceptToken string) int {
	username = strings.ToLower(strings.TrimSpace(username))
	s.mu.Lock()
	var toDrop []string
	for token, sess := range s.sessions {
		if sess.Username == username && token != exceptToken {
			toDrop = append(toDrop, token)
		}
	}
	for _, token := range toDrop {
		delete(s.sessions, token)
	}
	s.mu.Unlock()
	if len(toDrop) > 0 {
		s.save()
	}
	return len(toDrop)
}

// RenameUser updates the username in all sessions for a renamed user.
// Returns the number of sessions updated.
func (s *SessionStore) RenameUser(oldUsername, newUsername string) int {
	s.mu.Lock()
	count := 0
	for _, sess := range s.sessions {
		if sess.Username == oldUsername {
			sess.Username = newUsername
			count++
		}
	}
	s.mu.Unlock()
	if count > 0 {
		s.save()
	}
	return count
}

// ── Persistence ──────────────────────────────────────────────────────

func (s *SessionStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("auth/sessions: failed to load %s: %v", s.path, err)
		}
		return
	}
	var sessions map[string]*Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		log.Printf("auth/sessions: failed to parse %s: %v", s.path, err)
		return
	}
	// Prune expired sessions on load.
	now := float64(time.Now().Unix())
	pruned := 0
	for token, sess := range sessions {
		if sess.Expiry <= now {
			delete(sessions, token)
			pruned++
		}
	}
	s.sessions = sessions
	if pruned > 0 {
		s.save()
	}
	log.Printf("auth/sessions: loaded %d session(s)", len(s.sessions))
}

func (s *SessionStore) save() {
	s.mu.RLock()
	snapshot := make(map[string]*Session, len(s.sessions))
	for k, v := range s.sessions {
		snapshot[k] = v
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		log.Printf("auth/sessions: marshal failed: %v", err)
		return
	}
	if err := atomicWrite(s.path, data); err != nil {
		log.Printf("auth/sessions: save failed: %v", err)
	}
}

// ── Token generation ─────────────────────────────────────────────────

// generateToken produces a 64-char hex token matching Python's secrets.token_hex(32).
func generateToken() string {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
