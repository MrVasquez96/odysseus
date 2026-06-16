// Package mlclient provides a typed Go client for the Python ML worker.
//
// The Python worker handles: memory/embeddings (ChromaDB), cookbook/hwfit,
// deep research, document extraction, STT, the agent loop, and chat streaming.
// Go calls these endpoints over localhost HTTP with the internal auth token.
package mlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a typed HTTP client for the Python ML worker.
type Client struct {
	baseURL       string
	internalToken string
	http          *http.Client
}

// New creates a new ML client for the Python worker.
func New(baseURL, internalToken string) *Client {
	return &Client{
		baseURL:       baseURL,
		internalToken: internalToken,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ─── Memory / Embeddings ─────────────────────────────────────────────

// MemorySearchResult represents a single memory search hit.
type MemorySearchResult struct {
	ID       string  `json:"id"`
	Content  string  `json:"content"`
	Metadata any     `json:"metadata,omitempty"`
	Distance float64 `json:"distance,omitempty"`
}

// SearchMemory queries the vector memory store.
func (c *Client) SearchMemory(ctx context.Context, owner, query string, limit int) ([]MemorySearchResult, error) {
	body := map[string]any{
		"query": query,
		"limit": limit,
	}
	var resp struct {
		Results []MemorySearchResult `json:"results"`
	}
	if err := c.post(ctx, "/api/memory/search", owner, body, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// ─── Document Extraction ─────────────────────────────────────────────

// ExtractText extracts text from an uploaded file via the Python worker.
func (c *Client) ExtractText(ctx context.Context, owner, filePath string) (string, error) {
	body := map[string]any{"file_path": filePath}
	var resp struct {
		Text string `json:"text"`
	}
	if err := c.post(ctx, "/api/extract", owner, body, &resp); err != nil {
		return "", err
	}
	return resp.Text, nil
}

// ─── Health ──────────────────────────────────────────────────────────

// Ping checks if the Python worker is reachable.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/health", nil)
	if err != nil {
		return err
	}
	c.setHeaders(req, "")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("python worker unreachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("python worker unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ─── Internal helpers ────────────────────────────────────────────────

func (c *Client) setHeaders(req *http.Request, owner string) {
	req.Header.Set("Content-Type", "application/json")
	if c.internalToken != "" {
		req.Header.Set("X-Odysseus-Internal-Token", c.internalToken)
	}
	if owner != "" {
		req.Header.Set("X-Odysseus-Owner", owner)
	}
}

func (c *Client) post(ctx context.Context, path, owner string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	c.setHeaders(req, owner)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ml worker request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ml worker error: HTTP %d: %s", resp.StatusCode, string(raw))
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}
