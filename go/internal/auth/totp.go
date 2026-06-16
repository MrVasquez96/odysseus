package auth

import (
	"crypto/hmac"
	cryptoRand "crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"time"
)

// TOTP implements RFC 6238 Time-Based One-Time Passwords.
// Matches Python pyotp behavior used in core/auth.py.

const (
	totpDigits = 6
	totpPeriod = 30
	// validWindow matches pyotp's valid_window=1 used in core/auth.py.
	totpValidWindow = 1
)

// TOTPEnabled checks if 2FA is enabled for a user.
func (m *Manager) TOTPEnabled(username string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	user := m.config.Users[strings.ToLower(strings.TrimSpace(username))]
	return user != nil && user.TOTPEnabled
}

// TOTPGenerateSecret creates a new TOTP secret for a user (pending confirmation).
func (m *Manager) TOTPGenerateSecret(username string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	secret := generateBase32Secret()
	m.mu.Lock()
	defer m.mu.Unlock()
	user := m.config.Users[username]
	if user == nil {
		return ""
	}
	user.TOTPPending = secret
	m.save()
	return secret
}

// TOTPGetProvisioningURI returns the otpauth:// URI for QR code generation.
func (m *Manager) TOTPGetProvisioningURI(username, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=%d&period=%d",
		url.PathEscape("Odysseus"),
		url.PathEscape(username),
		secret,
		url.QueryEscape("Odysseus"),
		totpDigits,
		totpPeriod,
	)
}

// TOTPConfirmEnable verifies a code against the pending secret, then enables 2FA.
// Returns backup codes on success, nil on failure.
func (m *Manager) TOTPConfirmEnable(username, code string) []string {
	username = strings.ToLower(strings.TrimSpace(username))
	m.mu.Lock()
	defer m.mu.Unlock()
	user := m.config.Users[username]
	if user == nil || user.TOTPPending == "" {
		return nil
	}
	if !verifyTOTP(user.TOTPPending, code, totpValidWindow) {
		return nil
	}
	user.TOTPSecret = user.TOTPPending
	user.TOTPEnabled = true
	user.TOTPPending = ""
	// Generate 8 backup codes (matching Python: secrets.token_hex(4) → 8 hex chars).
	backup := make([]string, 8)
	for i := range backup {
		b := make([]byte, 4)
		_, _ = randRead(b)
		backup[i] = fmt.Sprintf("%x", b)
	}
	user.TOTPBackupCodes = backup
	m.save()
	log.Printf("auth: 2FA enabled for '%s'", username)
	return backup
}

// TOTPVerify checks a TOTP code for login (including backup codes).
func (m *Manager) TOTPVerify(username, code string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	m.mu.Lock()
	defer m.mu.Unlock()
	user := m.config.Users[username]
	if user == nil {
		return false
	}
	if !user.TOTPEnabled {
		return true // 2FA not enabled — always pass
	}
	if user.TOTPSecret == "" {
		// Enabled but no secret (corrupt state) — fail closed.
		return false
	}
	// Check backup codes first.
	for i, bc := range user.TOTPBackupCodes {
		if bc == code {
			// Consume the backup code.
			user.TOTPBackupCodes = append(user.TOTPBackupCodes[:i], user.TOTPBackupCodes[i+1:]...)
			m.save()
			log.Printf("auth: backup code used for '%s' (%d remaining)", username, len(user.TOTPBackupCodes))
			return true
		}
	}
	return verifyTOTP(user.TOTPSecret, code, totpValidWindow)
}

// TOTPDisable disables 2FA after password confirmation.
func (m *Manager) TOTPDisable(username, password string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	if !m.VerifyPassword(username, password) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	user := m.config.Users[username]
	if user == nil {
		return false
	}
	user.TOTPSecret = ""
	user.TOTPPending = ""
	user.TOTPBackupCodes = nil
	user.TOTPEnabled = false
	m.save()
	log.Printf("auth: 2FA disabled for '%s'", username)
	return true
}

// ── TOTP implementation (RFC 6238) ───────────────────────────────────

// verifyTOTP checks if code matches the secret within the given time window.
func verifyTOTP(secret, code string, window int) bool {
	now := time.Now().Unix()
	counter := now / totpPeriod
	for i := -window; i <= window; i++ {
		expected := generateTOTPCode(secret, counter+int64(i))
		if expected == code {
			return true
		}
	}
	return false
}

// generateTOTPCode generates a TOTP code for a given counter value.
func generateTOTPCode(secret string, counter int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(
		strings.ToUpper(strings.TrimRight(secret, "=")),
	)
	if err != nil {
		return ""
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff
	code := truncated % uint32(math.Pow10(totpDigits))
	return fmt.Sprintf("%0*d", totpDigits, code)
}

// generateBase32Secret creates a random base32 secret matching pyotp.random_base32().
func generateBase32Secret() string {
	b := make([]byte, 20) // 160 bits, standard for TOTP
	_, _ = randRead(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// randRead wraps crypto/rand.Read, split out for testability.
var randRead = func(b []byte) (int, error) {
	return cryptoRandRead(b)
}

// cryptoRandRead is the actual crypto/rand.Read, used by default.
func cryptoRandRead(b []byte) (int, error) {
	return cryptoRand.Read(b)
}
