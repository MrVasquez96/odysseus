package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"odysseus/internal/auth"
	"odysseus/internal/db"
	"odysseus/internal/routes"
)

// AuthConfig holds auth middleware configuration from environment.
type AuthConfig struct {
	Enabled         bool
	LocalhostBypass bool
	Manager         *auth.Manager
	// InternalToken is the random token for agent loopback requests.
	// Matches core/middleware.py INTERNAL_TOOL_TOKEN.
	InternalToken string
	// Database for bearer token validation (Phase 3+). Nil = pass through to Python.
	Database *db.DB
}

// authExemptExact matches app.py AUTH_EXEMPT_EXACT.
var authExemptExact = map[string]bool{
	"/api/auth/setup":                true,
	"/api/auth/signup":               true,
	"/api/auth/login":                true,
	"/api/auth/logout":               true,
	"/api/auth/status":               true,
	"/api/auth/features":             true,
	"/api/auth/settings":             true,
	"/api/auth/integrations/presets": true,
	"/api/auth/password-score":       true,
	"/api/health":                    true,
	"/api/version":                   true,
	"/login":                         true,
}

// authExemptPrefixes matches app.py AUTH_EXEMPT_PREFIXES.
var authExemptPrefixes = []string{"/static"}

// authExemptPatterns matches app.py AUTH_EXEMPT_PATTERNS.
var authExemptPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^/api/tasks/[^/]+/webhook/[^/]+/?$`),
}

// proxyForwardHeaders that indicate the request came through a tunnel/proxy.
// Matches app.py _PROXY_FWD_HEADERS — a loopback IP with any of these
// headers is NOT trusted (it's a tunneled remote client, not a local call).
var proxyForwardHeaders = []string{
	"cf-connecting-ip", "cf-ray", "cf-visitor",
	"x-forwarded-for", "x-forwarded-host", "x-real-ip", "forwarded",
}

const internalToolHeader = "X-Odysseus-Internal-Token"

func isAuthExempt(path string) bool {
	if authExemptExact[path] {
		return true
	}
	for _, prefix := range authExemptPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	for _, pat := range authExemptPatterns {
		if pat.MatchString(path) {
			return true
		}
	}
	return false
}

// isTrustedLoopback matches app.py _is_trusted_loopback: direct loopback
// with NO proxy/tunnel forwarding headers.
func isTrustedLoopback(r *http.Request) bool {
	host := r.RemoteAddr
	// Extract host from host:port.
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]") // IPv6
	if host != "127.0.0.1" && host != "::1" {
		return false
	}
	for _, h := range proxyForwardHeaders {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	return true
}

// isCORSPreflight matches core/middleware.py is_cors_preflight.
func isCORSPreflight(method string, headers http.Header) bool {
	return method == http.MethodOptions && headers.Get("Access-Control-Request-Method") != ""
}

// AuthMiddleware creates the auth middleware matching app.py AuthMiddleware.
// When auth is disabled, it passes all requests through.
func AuthMiddleware(cfg AuthConfig, next http.Handler) http.Handler {
	if !cfg.Enabled {
		return next
	}

	mgr := cfg.Manager
	localhostBypass := cfg.LocalhostBypass
	internalToken := cfg.InternalToken
	database := cfg.Database

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// CORS preflight: must reach CORS middleware, never carries credentials.
		if isCORSPreflight(r.Method, r.Header) {
			next.ServeHTTP(w, r)
			return
		}

		// Auth-exempt paths: still resolve user identity (for settings scrub,
		// etc.) but don't reject unauthenticated requests.
		if isAuthExempt(path) {
			if cookie, err := r.Cookie(auth.SessionCookie); err == nil && cookie.Value != "" {
				sessions := mgr.Sessions()
				if sessions != nil && sessions.ValidateToken(cookie.Value) {
					username := sessions.GetUsernameForToken(cookie.Value)
					if username != "" {
						ctx := context.WithValue(r.Context(), routes.CtxKeyCurrentUser, username)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		// Internal tool token bypass (agent loopback).
		if internalToken != "" {
			hdr := r.Header.Get(internalToolHeader)
			if hdr != "" && subtle.ConstantTimeCompare([]byte(hdr), []byte(internalToken)) == 1 && isTrustedLoopback(r) {
				username := "internal-tool"
				if impersonate := strings.TrimSpace(r.Header.Get("X-Odysseus-Owner")); impersonate != "" {
					if mgr.UserExists(impersonate) {
						username = impersonate
					}
				}
				ctx := context.WithValue(r.Context(), routes.CtxKeyCurrentUser, username)
				ctx = context.WithValue(ctx, routes.CtxKeyAPIToken, false)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// Localhost bypass.
		if localhostBypass && isTrustedLoopback(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Not configured yet — redirect to login.
		if !mgr.IsConfigured() {
			if !strings.HasPrefix(path, "/api/") {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "Setup required"})
			return
		}

		// Bearer token auth (API tokens: "Bearer ody_...").
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ody_") {
			rawToken := authHeader[7:] // strip "Bearer "
			if len(rawToken) < 12 || len(rawToken) > 100 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "Invalid API token"})
				return
			}
			if database != nil {
				prefix := rawToken[:8]
				candidates, _ := database.ListActiveTokensByPrefix(prefix)
				var matchedID, matchedOwner, matchedScopes string
				for _, t := range candidates {
					if bcrypt.CompareHashAndPassword([]byte(t.TokenHash), []byte(rawToken)) == nil {
						matchedID = t.ID
						matchedOwner = db.StringVal(t.Owner)
						matchedScopes = t.Scopes
						// Normalize owner to a known auth username.
						if matchedOwner != "" && !mgr.UserExists(matchedOwner) {
							continue // skip tokens for deleted users
						}
						break
					}
				}
				if matchedID != "" {
					// Touch last_used_at asynchronously.
					go database.TouchTokenLastUsed(matchedID)

					ctx := context.WithValue(r.Context(), routes.CtxKeyCurrentUser, "api")
					ctx = context.WithValue(ctx, routes.CtxKeyAPIToken, true)
					ctx = context.WithValue(ctx, routes.CtxKeyAPITokenOwner, matchedOwner)
					ctx = context.WithValue(ctx, routes.CtxKeyAPITokenScopes, matchedScopes)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			} else {
				// No database — pass through to Python (Phase 2 compat).
				next.ServeHTTP(w, r)
				return
			}
			// Invalid bearer token — reject.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid API token"})
			return
		}

		// Cookie-based session auth.
		cookie, err := r.Cookie(auth.SessionCookie)
		if err != nil || !mgr.Sessions().ValidateToken(cookie.Value) {
			if strings.HasPrefix(path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "Not authenticated"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// Attach current username to context.
		username := mgr.Sessions().GetUsernameForToken(cookie.Value)
		ctx := context.WithValue(r.Context(), routes.CtxKeyCurrentUser, username)
		ctx = context.WithValue(ctx, routes.CtxKeyAPIToken, false)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// LoadAuthConfig reads auth-related env vars and creates the auth manager.
func LoadAuthConfig(dataDir string) AuthConfig {
	authEnabled := !strings.EqualFold(os.Getenv("AUTH_ENABLED"), "false")
	localhostBypass := strings.EqualFold(os.Getenv("LOCALHOST_BYPASS"), "true")

	authPath := dataDir + "/auth.json"
	mgr := auth.New(authPath)

	// Read the shared internal tool token from env or file (written by entrypoint.sh).
	// Falls back to generating one if running outside Docker (dev mode).
	internalToken := os.Getenv("ODYSSEUS_INTERNAL_TOKEN")
	if internalToken == "" {
		if data, err := os.ReadFile(dataDir + "/.internal_token"); err == nil {
			internalToken = strings.TrimSpace(string(data))
		}
	}
	if internalToken == "" {
		internalToken = auth.GenerateInternalToken()
		log.Println("auth: generated new internal token (no env/file found)")
	} else {
		log.Printf("auth: loaded internal token from file/env (%d chars)", len(internalToken))
	}

	return AuthConfig{
		Enabled:         authEnabled,
		LocalhostBypass: localhostBypass,
		Manager:         mgr,
		InternalToken:   internalToken,
	}
}
