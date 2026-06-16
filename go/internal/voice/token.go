// Package voice provides LiveKit token generation for voice sessions.
package voice

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenRequest is the JSON body for POST /api/voice/token.
type TokenRequest struct {
	SessionID string `json:"session_id"`
	Identity  string `json:"identity"`
}

// TokenResponse is the JSON body returned by POST /api/voice/token.
type TokenResponse struct {
	Token     string `json:"token"`
	URL       string `json:"url"`
	Room      string `json:"room"`
	Identity  string `json:"identity"`
	SessionID string `json:"session_id"`
}

// VideoGrant represents LiveKit video grant claims.
type VideoGrant struct {
	RoomJoin   bool   `json:"roomJoin"`
	Room       string `json:"room"`
	RoomCreate bool   `json:"roomCreate,omitempty"`
}

// AgentDispatchConfig is the agent dispatch entry in roomConfig.
type AgentDispatchConfig struct {
	AgentName string `json:"agentName"`
	Metadata  string `json:"metadata,omitempty"`
}

// RoomConfiguration is the room creation config embedded in the token.
type RoomConfiguration struct {
	Agents []AgentDispatchConfig `json:"agents,omitempty"`
}

// Claims represents the JWT claims for a LiveKit access token.
// roomConfig is a top-level claim (not nested inside video) per the LiveKit spec.
type Claims struct {
	ISS        string             `json:"iss"`                      // API key
	SUB        string             `json:"sub"`                      // participant identity
	IAT        int64              `json:"iat"`                      // issued at
	NBF        int64              `json:"nbf"`                      // not before
	EXP        int64              `json:"exp"`                      // expiration
	Video      VideoGrant         `json:"video"`                    // room permissions
	Name       string             `json:"name,omitempty"`           // display name
	Meta       string             `json:"metadata,omitempty"`       // participant metadata
	RoomConfig *RoomConfiguration `json:"roomConfig,omitempty"`     // room creation config with agent dispatch
}

// GenerateToken creates a LiveKit access token for the given room and identity.
// The token includes roomConfig with agent dispatch so the voice agent
// automatically joins when the room is created.
func GenerateToken(apiKey, apiSecret, room, identity, displayName string, metadata string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		ISS: apiKey,
		SUB: identity,
		IAT: now.Unix(),
		NBF: now.Unix(),
		EXP: now.Add(ttl).Unix(),
		Video: VideoGrant{
			RoomJoin:   true,
			RoomCreate: true,
			Room:       room,
		},
		Name: displayName,
		Meta: metadata,
		RoomConfig: &RoomConfiguration{
			Agents: []AgentDispatchConfig{
				{
					// Empty agent name matches any registered worker.
					AgentName: "",
					Metadata:  metadata,
				},
			},
		},
	}

	header := map[string]string{"alg": "HS256", "typ": "JWT"}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}

	headerB64 := base64URLEncode(headerJSON)
	claimsB64 := base64URLEncode(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write([]byte(signingInput))
	signature := base64URLEncode(mac.Sum(nil))

	return signingInput + "." + signature, nil
}

func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}
