package db

import (
	"database/sql"
	"time"
)

// Session matches Python's core/database.py Session model.
type Session struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	EndpointURL      string         `json:"endpoint_url"`
	Model            string         `json:"model"`
	Owner            sql.NullString `json:"-"`
	RAG              bool           `json:"rag"`
	Archived         bool           `json:"archived"`
	Folder           sql.NullString `json:"-"`
	Headers          sql.NullString `json:"-"` // JSON
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	LastAccessed     sql.NullTime   `json:"last_accessed"`
	LastMessageAt    sql.NullTime   `json:"last_message_at"`
	IsImportant      bool           `json:"is_important"`
	MessageCount     int            `json:"message_count"`
	TotalInputTokens int            `json:"total_input_tokens"`
	TotalOutputTokens int           `json:"total_output_tokens"`
	Mode             sql.NullString `json:"-"`
	CrewMemberID     sql.NullString `json:"-"`
}

// ToDict returns the JSON-serializable map matching Python's Session.to_dict().
func (s *Session) ToDict() map[string]any {
	m := map[string]any{
		"id":                  s.ID,
		"name":                s.Name,
		"model":               s.Model,
		"endpoint_url":        s.EndpointURL,
		"rag":                 s.RAG,
		"archived":            s.Archived,
		"created_at":          formatTime(s.CreatedAt),
		"updated_at":          formatTime(s.UpdatedAt),
		"last_accessed":       formatNullTime(s.LastAccessed),
		"last_message_at":     formatNullTime(s.LastMessageAt),
		"message_count":       s.MessageCount,
		"is_important":        s.IsImportant,
		"folder":              StringVal(s.Folder),
		"total_input_tokens":  s.TotalInputTokens,
		"total_output_tokens": s.TotalOutputTokens,
		"crew_member_id":      nullStringPtr(s.CrewMemberID),
	}
	return m
}

// ChatMessage matches Python's ChatMessage model.
type ChatMessage struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	MetaData  sql.NullString `json:"-"` // JSON string
	Timestamp time.Time `json:"timestamp"`
}

// Document matches Python's Document model.
type Document struct {
	ID                    string         `json:"id"`
	SessionID             sql.NullString `json:"-"`
	Title                 string         `json:"title"`
	Language              sql.NullString `json:"-"`
	CurrentContent        string         `json:"current_content"`
	VersionCount          int            `json:"version_count"`
	IsActive              bool           `json:"is_active"`
	Archived              bool           `json:"archived"`
	Owner                 sql.NullString `json:"-"`
	TidyVerdict           sql.NullString `json:"-"`
	SourceEmailUID        sql.NullString `json:"-"`
	SourceEmailFolder     sql.NullString `json:"-"`
	SourceEmailAccountID  sql.NullString `json:"-"`
	SourceEmailMessageID  sql.NullString `json:"-"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

// DocumentVersion matches Python's DocumentVersion model.
type DocumentVersion struct {
	ID            string         `json:"id"`
	DocumentID    string         `json:"document_id"`
	VersionNumber int            `json:"version_number"`
	Content       string         `json:"content"`
	Summary       sql.NullString `json:"-"`
	Source        string         `json:"source"`
	CreatedAt     time.Time      `json:"created_at"`
}

// GalleryAlbum matches Python's GalleryAlbum model.
type GalleryAlbum struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	CoverID     sql.NullString `json:"-"`
	Owner       sql.NullString `json:"-"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// GalleryImage matches Python's GalleryImage model.
type GalleryImage struct {
	ID          string         `json:"id"`
	Filename    string         `json:"filename"`
	Prompt      string         `json:"prompt"`
	Model       sql.NullString `json:"-"`
	Size        sql.NullString `json:"-"`
	Quality     sql.NullString `json:"-"`
	Tags        sql.NullString `json:"-"`
	AITags      sql.NullString `json:"-"`
	SessionID   sql.NullString `json:"-"`
	AlbumID     sql.NullString `json:"-"`
	Owner       sql.NullString `json:"-"`
	IsActive    bool           `json:"is_active"`
	Favorite    bool           `json:"favorite"`
	FileHash    sql.NullString `json:"-"`
	TakenAt     sql.NullTime   `json:"-"`
	CameraMake  sql.NullString `json:"-"`
	CameraModel sql.NullString `json:"-"`
	GPSLat      sql.NullString `json:"-"`
	GPSLng      sql.NullString `json:"-"`
	Width       sql.NullInt64  `json:"-"`
	Height      sql.NullInt64  `json:"-"`
	FileSize    sql.NullInt64  `json:"-"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// ModelEndpoint matches Python's ModelEndpoint model.
type ModelEndpoint struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	BaseURL              string         `json:"base_url"`
	APIKey               sql.NullString `json:"-"` // encrypted at rest
	IsEnabled            bool           `json:"is_enabled"`
	HiddenModels         sql.NullString `json:"-"` // JSON
	CachedModels         sql.NullString `json:"-"` // JSON
	PinnedModels         sql.NullString `json:"-"` // JSON
	ModelType            sql.NullString `json:"-"`
	EndpointKind         sql.NullString `json:"-"`
	ModelRefreshMode     sql.NullString `json:"-"`
	ModelRefreshInterval sql.NullInt64  `json:"-"`
	ModelRefreshTimeout  sql.NullInt64  `json:"-"`
	SupportsTools        sql.NullBool   `json:"-"`
	Owner                sql.NullString `json:"-"`
	ProviderAuthID       sql.NullString `json:"-"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

// McpServer matches Python's McpServer model.
type McpServer struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Transport     string         `json:"transport"`
	Command       sql.NullString `json:"-"`
	Args          sql.NullString `json:"-"` // JSON
	Env           sql.NullString `json:"-"` // JSON
	URL           sql.NullString `json:"-"`
	IsEnabled     bool           `json:"is_enabled"`
	OAuthConfig   sql.NullString `json:"-"` // JSON
	DisabledTools sql.NullString `json:"-"` // JSON
	OAuthTokens   sql.NullString `json:"-"` // encrypted
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// ApiToken matches Python's ApiToken model.
type ApiToken struct {
	ID          string       `json:"id"`
	Owner       sql.NullString `json:"-"`
	Name        string       `json:"name"`
	TokenHash   string       `json:"-"`
	TokenPrefix string       `json:"token_prefix"`
	Scopes      string       `json:"scopes"`
	IsActive    bool         `json:"is_active"`
	LastUsedAt  sql.NullTime `json:"-"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// Note matches Python's Note model.
type Note struct {
	ID               string         `json:"id"`
	Owner            sql.NullString `json:"-"`
	Title            string         `json:"title"`
	Content          sql.NullString `json:"-"`
	Items            sql.NullString `json:"-"` // JSON
	NoteType         string         `json:"note_type"`
	Color            sql.NullString `json:"-"`
	Label            sql.NullString `json:"-"`
	Pinned           bool           `json:"pinned"`
	Archived         bool           `json:"archived"`
	DueDate          sql.NullString `json:"-"`
	Source           string         `json:"source"`
	SessionID        sql.NullString `json:"-"`
	SortOrder        int            `json:"sort_order"`
	ImageURL         sql.NullString `json:"-"`
	Repeat           string         `json:"repeat"`
	AIClassification sql.NullString `json:"-"` // JSON
	AIContentHash    sql.NullString `json:"-"`
	AgentSessionID   sql.NullString `json:"-"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// ScheduledTask matches Python's ScheduledTask model.
type ScheduledTask struct {
	ID                    string         `json:"id"`
	Owner                 sql.NullString `json:"-"`
	Name                  string         `json:"name"`
	Prompt                sql.NullString `json:"-"`
	TaskType              string         `json:"task_type"`
	Action                sql.NullString `json:"-"`
	Schedule              sql.NullString `json:"-"`
	ScheduledTime         sql.NullString `json:"-"`
	ScheduledDay          sql.NullInt64  `json:"-"`
	ScheduledDate         sql.NullTime   `json:"-"`
	TriggerType           string         `json:"trigger_type"`
	TriggerEvent          sql.NullString `json:"-"`
	TriggerCount          sql.NullInt64  `json:"-"`
	TriggerCounter        int            `json:"trigger_counter"`
	NextRun               sql.NullTime   `json:"-"`
	LastRun               sql.NullTime   `json:"-"`
	Status                string         `json:"status"`
	OutputTarget          string         `json:"output_target"`
	SessionID             sql.NullString `json:"-"`
	Model                 sql.NullString `json:"-"`
	EndpointURL           sql.NullString `json:"-"`
	RunCount              int            `json:"run_count"`
	CronExpression        sql.NullString `json:"-"`
	ThenTaskID            sql.NullString `json:"-"`
	WebhookToken          sql.NullString `json:"-"`
	CrewMemberID          sql.NullString `json:"-"`
	CharacterID           sql.NullString `json:"-"`
	MaxSteps              sql.NullInt64  `json:"-"`
	EmailResults          bool           `json:"email_results"`
	NotificationsEnabled  bool           `json:"notifications_enabled"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

// TaskRun matches Python's TaskRun model.
type TaskRun struct {
	ID         string         `json:"id"`
	TaskID     string         `json:"task_id"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt sql.NullTime   `json:"-"`
	Status     string         `json:"status"`
	Result     sql.NullString `json:"-"`
	Error      sql.NullString `json:"-"`
	TokensUsed sql.NullInt64  `json:"-"`
	Steps      sql.NullString `json:"-"` // JSON
	Model      sql.NullString `json:"-"`
}

// Webhook matches Python's Webhook model.
type Webhook struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	URL             string         `json:"url"`
	Secret          sql.NullString `json:"-"`
	Events          string         `json:"events"`
	IsActive        bool           `json:"is_active"`
	LastTriggeredAt sql.NullTime   `json:"-"`
	LastStatusCode  sql.NullInt64  `json:"-"`
	LastError       sql.NullString `json:"-"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// UserTool matches Python's UserTool model.
type UserTool struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description sql.NullString `json:"-"`
	Icon        sql.NullString `json:"-"`
	HTMLContent string         `json:"html_content"`
	Scope       string         `json:"scope"`
	SessionID   sql.NullString `json:"-"`
	Owner       sql.NullString `json:"-"`
	IsPinned    bool           `json:"is_pinned"`
	IsActive    bool           `json:"is_active"`
	Version     int            `json:"version"`
	Author      sql.NullString `json:"-"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// CrewMember matches Python's CrewMember model.
type CrewMember struct {
	ID                 string         `json:"id"`
	Owner              sql.NullString `json:"-"`
	Name               string         `json:"name"`
	Avatar             sql.NullString `json:"-"`
	UserName           sql.NullString `json:"-"`
	Personality        sql.NullString `json:"-"`
	Model              sql.NullString `json:"-"`
	EndpointURL        sql.NullString `json:"-"`
	Greeting           sql.NullString `json:"-"`
	EnabledTools       sql.NullString `json:"-"` // JSON
	SessionID          sql.NullString `json:"-"`
	IsActive           bool           `json:"is_active"`
	SortOrder          int            `json:"sort_order"`
	IsDefaultAssistant bool           `json:"is_default_assistant"`
	Timezone           sql.NullString `json:"-"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// Memory matches Python's Memory model.
type Memory struct {
	ID        string         `json:"id"`
	Text      string         `json:"text"`
	Category  string         `json:"category"`
	Source    string         `json:"source"`
	Owner     sql.NullString `json:"-"`
	SessionID sql.NullString `json:"-"`
	Timestamp int64          `json:"timestamp"`
}

// Signature matches Python's Signature model.
type Signature struct {
	ID        string         `json:"id"`
	Owner     sql.NullString `json:"-"`
	Name      string         `json:"name"`
	DataPNG   string         `json:"-"` // encrypted
	Width     sql.NullInt64  `json:"-"`
	Height    sql.NullInt64  `json:"-"`
	SVG       sql.NullString `json:"-"` // encrypted
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Comparison matches Python's Comparison model.
type Comparison struct {
	ID           string         `json:"id"`
	SessionID    sql.NullString `json:"-"`
	Owner        sql.NullString `json:"-"`
	Prompt       string         `json:"prompt"`
	ModelA       string         `json:"model_a"`
	ModelB       string         `json:"model_b"`
	EndpointA    string         `json:"endpoint_a"`
	EndpointB    string         `json:"endpoint_b"`
	ResponseA    sql.NullString `json:"-"`
	ResponseB    sql.NullString `json:"-"`
	MetricsA     sql.NullString `json:"-"` // JSON
	MetricsB     sql.NullString `json:"-"` // JSON
	Winner       sql.NullString `json:"-"`
	IsBlind      bool           `json:"is_blind"`
	BlindMapping sql.NullString `json:"-"` // JSON
	VotedAt      sql.NullTime   `json:"-"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// EmailAccount matches Python's EmailAccount model.
type EmailAccount struct {
	ID           string         `json:"id"`
	Owner        sql.NullString `json:"-"`
	Name         string         `json:"name"`
	IsDefault    bool           `json:"is_default"`
	Enabled      bool           `json:"enabled"`
	IMAPHost     string         `json:"imap_host"`
	IMAPPort     int            `json:"imap_port"`
	IMAPUser     string         `json:"imap_user"`
	IMAPPassword string         `json:"-"` // encrypted
	IMAPStartTLS bool           `json:"imap_starttls"`
	SMTPHost     string         `json:"smtp_host"`
	SMTPPort     int            `json:"smtp_port"`
	SMTPSecurity string         `json:"smtp_security"`
	SMTPUser     string         `json:"smtp_user"`
	SMTPPassword string         `json:"-"` // encrypted
	FromAddress  string         `json:"from_address"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// CalendarCal matches Python's CalendarCal model.
type CalendarCal struct {
	ID        string         `json:"id"`
	Owner     sql.NullString `json:"-"`
	Name      string         `json:"name"`
	Color     string         `json:"color"`
	Source    string         `json:"source"`
	AccountID sql.NullString `json:"-"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// CalendarEvent matches Python's CalendarEvent model.
type CalendarEvent struct {
	UID         string         `json:"uid"`
	CalendarID  string         `json:"calendar_id"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	Location    string         `json:"location"`
	DTStart     time.Time      `json:"dtstart"`
	DTEnd       time.Time      `json:"dtend"`
	AllDay      bool           `json:"all_day"`
	IsUTC       bool           `json:"is_utc"`
	RRule       string         `json:"rrule"`
	Color       sql.NullString `json:"-"`
	Status      string         `json:"status"`
	Importance  string         `json:"importance"`
	EventType   sql.NullString `json:"-"`
	LastPinged  sql.NullTime   `json:"-"`
	Origin      sql.NullString `json:"-"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// EditorDraft matches Python's EditorDraft model.
type EditorDraft struct {
	ID            string         `json:"id"`
	Owner         sql.NullString `json:"-"`
	Name          string         `json:"name"`
	SourceImageID sql.NullString `json:"-"`
	Width         sql.NullInt64  `json:"-"`
	Height        sql.NullInt64  `json:"-"`
	Payload       string         `json:"-"`
	Thumbnail     sql.NullString `json:"-"`
	IsActive      bool           `json:"is_active"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// --- Time formatting helpers ---

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05")
}

func formatNullTime(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format("2006-01-02T15:04:05")
	return &s
}

func nullStringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.String
}
