package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"odysseus/internal/tunnel"

	zxcvbn "github.com/nbutton23/zxcvbn-go"
)

type tunnelRoutes struct {
	mgr       *tunnel.Manager
	localAddr string
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
}

// TunnelRoutes registers the Cloudflare tunnel management endpoints.
// These are admin-only — the middleware should gate access.
func TunnelRoutes(mux *http.ServeMux, localAddr string) *tunnel.Manager {
	mgr := tunnel.NewManager()
	ctx, cancel := context.WithCancel(context.Background())

	r := &tunnelRoutes{
		mgr:       mgr,
		localAddr: localAddr,
		ctx:       ctx,
		cancel:    cancel,
	}

	mux.HandleFunc("GET /api/tunnel/status", r.status)
	mux.HandleFunc("POST /api/tunnel/start", r.start)
	mux.HandleFunc("POST /api/tunnel/stop", r.stop)

	return mgr
}

func (r *tunnelRoutes) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, r.mgr.Status())
}

type tunnelStartRequest struct {
	Password string `json:"password"`
}

func (r *tunnelRoutes) start(w http.ResponseWriter, req *http.Request) {
	// Admin check
	user := effectiveUser(req)
	if user == "" {
		jsonError(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	var body tunnelStartRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// zxcvbn password strength gate — score must be >= 3 (out of 0-4)
	if body.Password == "" {
		jsonError(w, "Password required to enable tunnel", http.StatusBadRequest)
		return
	}

	result := zxcvbn.PasswordStrength(body.Password, nil)
	if result.Score < 3 {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":          "Password too weak to enable public tunnel",
			"score":          result.Score,
			"required_score": 3,
			"feedback":       result.CrackTimeDisplay,
		})
		return
	}

	// Start tunnel in background
	r.mu.Lock()
	defer r.mu.Unlock()

	status := r.mgr.Status()
	if status.Running {
		writeJSON(w, http.StatusOK, status)
		return
	}

	go func() {
		if err := r.mgr.Start(r.ctx, r.localAddr); err != nil {
			// Error is captured in manager status
			_ = err
		}
	}()

	// Return immediately — client should poll /api/tunnel/status
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "starting",
	})
}

func (r *tunnelRoutes) stop(w http.ResponseWriter, req *http.Request) {
	user := effectiveUser(req)
	if user == "" {
		jsonError(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	if err := r.mgr.Stop(); err != nil {
		jsonError(w, "Failed to stop tunnel: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// StopTunnel is called during graceful shutdown.
func StopTunnel(mgr *tunnel.Manager) {
	if mgr != nil {
		mgr.Stop()
	}
}
