package db

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SaveVoiceTranscript inserts a voice transcript as a chat message in the
// given session. The source field (e.g. "voice") is stored in the metadata
// JSON column so the frontend can distinguish voice from text messages.
func (d *DB) SaveVoiceTranscript(owner, sessionID, role, content, source string) error {
	id := uuid.New().String()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	metadata := fmt.Sprintf(`{"source":"%s"}`, source)

	_, err := d.Exec(`INSERT INTO chat_messages (id, session_id, role, content, metadata, timestamp)
	                   VALUES (?, ?, ?, ?, ?, ?)`,
		id, sessionID, role, content, metadata, now)
	return err
}
