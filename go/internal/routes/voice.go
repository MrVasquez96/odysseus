package routes

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"odysseus/internal/db"
	"odysseus/internal/voice"
)

type voiceRoutes struct {
	db        *db.DB
	apiKey    string
	apiSecret string
	livekitURL string
}

// VoiceRoutes registers the voice/LiveKit endpoints.
func VoiceRoutes(mux *http.ServeMux, database *db.DB) {
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")
	livekitURL := os.Getenv("LIVEKIT_URL")

	// Use the public-facing WS URL for the browser. Inside Docker,
	// LIVEKIT_URL is ws://livekit:7880 (container-to-container),
	// but the browser needs the host-reachable address.
	publicURL := os.Getenv("LIVEKIT_PUBLIC_URL")
	if publicURL == "" {
		publicURL = "ws://localhost:7880"
	}

	if apiKey == "" || apiSecret == "" {
		return // voice disabled — no LiveKit credentials
	}

	r := &voiceRoutes{
		db:         database,
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		livekitURL: publicURL,
	}

	_ = livekitURL // internal URL for agent connections (used by Python)

	mux.HandleFunc("POST /api/voice/token", r.issueToken)
	mux.HandleFunc("POST /api/voice/transcript", r.saveTranscript)
	mux.HandleFunc("GET /api/voice/status", r.status)
}

func (r *voiceRoutes) issueToken(w http.ResponseWriter, req *http.Request) {
	user := effectiveUser(req)
	if user == "" {
		jsonError(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	var body voice.TokenRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if body.SessionID == "" {
		jsonError(w, "session_id is required", http.StatusBadRequest)
		return
	}

	// Room name is scoped to the user and session for isolation.
	room := "odysseus-" + user + "-" + body.SessionID
	identity := user

	// Include user metadata so the voice agent knows who is speaking.
	metadata := `{"username":"` + user + `","session_id":"` + body.SessionID + `"}`

	token, err := voice.GenerateToken(
		r.apiKey, r.apiSecret,
		room, identity, user, metadata,
		4*time.Hour,
	)
	if err != nil {
		jsonError(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, voice.TokenResponse{
		Token:     token,
		URL:       r.livekitURL,
		Room:      room,
		Identity:  identity,
		SessionID: body.SessionID,
	})
}

// transcriptRequest is the body for saving voice transcripts to the chat session.
type transcriptRequest struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`      // "user" or "assistant"
	Content   string `json:"content"`
	Source    string `json:"source"`    // "voice"
}

func (r *voiceRoutes) saveTranscript(w http.ResponseWriter, req *http.Request) {
	user := effectiveUser(req)
	if user == "" {
		jsonError(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	var body transcriptRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if body.SessionID == "" || body.Content == "" || body.Role == "" {
		jsonError(w, "session_id, role, and content are required", http.StatusBadRequest)
		return
	}

	if body.Source == "" {
		body.Source = "voice"
	}

	err := r.db.SaveVoiceTranscript(user, body.SessionID, body.Role, body.Content, body.Source)
	if err != nil {
		jsonError(w, "Failed to save transcript", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (r *voiceRoutes) status(w http.ResponseWriter, _ *http.Request) {
	voiceMode := os.Getenv("VOICE_MODE")
	if voiceMode == "" {
		voiceMode = "local"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     true,
		"livekit_url": r.livekitURL,
		"voice_mode":  voiceMode,
	})
}
