package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout is the default streaming read timeout.
const DefaultTimeout = 300 * time.Second

// Client is an LLM HTTP client that streams chat completions.
type Client struct {
	http *http.Client
}

// NewClient creates a new LLM client.
func NewClient() *Client {
	return &Client{
		http: &http.Client{
			// No overall timeout — streaming can run for minutes.
			// Per-request timeouts are set via context.
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		},
	}
}

// StreamChat opens a streaming chat completion and sends events to the
// returned channel. The channel is closed when the stream ends.
// Cancel the context to abort.
func (c *Client) StreamChat(ctx context.Context, req CompletionRequest) <-chan StreamEvent {
	ch := make(chan StreamEvent, 64)
	go func() {
		defer close(ch)
		c.doStream(ctx, req, ch)
	}()
	return ch
}

func (c *Client) doStream(ctx context.Context, req CompletionRequest, ch chan<- StreamEvent) {
	provider := DetectProvider(req.URL)
	timeout := DefaultTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build request body and target URL based on provider.
	var body []byte
	var targetURL string
	var headers map[string]string
	var err error

	switch provider {
	case ProviderAnthropic:
		targetURL, body, headers, err = buildAnthropicRequest(req)
	case ProviderOllama:
		if !isOllamaOpenAICompat(req.URL) {
			targetURL, body, headers, err = buildOllamaRequest(req)
			if err != nil {
				ch <- StreamEvent{Error: &StreamErr{Message: err.Error(), Status: 500}}
				return
			}
			c.streamOllama(ctx, targetURL, body, headers, ch)
			return
		}
		// Ollama /v1 endpoint — treat as OpenAI-compatible
		targetURL, body, headers, err = buildOpenAIRequest(req)
	default:
		targetURL, body, headers, err = buildOpenAIRequest(req)
	}
	if err != nil {
		ch <- StreamEvent{Error: &StreamErr{Message: err.Error(), Status: 500}}
		return
	}

	switch provider {
	case ProviderAnthropic:
		c.streamAnthropic(ctx, targetURL, body, headers, ch)
	default:
		c.streamOpenAI(ctx, targetURL, body, headers, req.Model, ch)
	}
}

// ─── OpenAI-compatible streaming ─────────────────────────────────────

func buildOpenAIRequest(req CompletionRequest) (string, []byte, map[string]string, error) {
	targetURL := NormalizeChatURL(req.URL)
	payload := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		payload["tools"] = req.Tools
	}
	// Request usage in stream.
	provider := DetectProvider(req.URL)
	if provider != ProviderOpenRouter && provider != ProviderGroq {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, nil, err
	}
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range req.Headers {
		headers[k] = v
	}
	return targetURL, body, headers, nil
}

func (c *Client) streamOpenAI(ctx context.Context, url string, body []byte, headers map[string]string, model string, ch chan<- StreamEvent) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		ch <- StreamEvent{Error: &StreamErr{Message: err.Error(), Status: 500}}
		return
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		ch <- StreamEvent{Error: &StreamErr{Message: fmt.Sprintf("Connect error: %v", err), Status: 502}}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		ch <- StreamEvent{Error: &StreamErr{Message: string(raw), Status: resp.StatusCode}}
		return
	}

	// Accumulate tool calls across chunks.
	tcAcc := map[int]*ToolCall{}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			// Emit accumulated tool calls.
			if len(tcAcc) > 0 {
				calls := make([]ToolCall, 0, len(tcAcc))
				for i := 0; i < len(tcAcc); i++ {
					if tc, ok := tcAcc[i]; ok {
						calls = append(calls, *tc)
					}
				}
				ch <- StreamEvent{ToolCalls: calls}
			}
			ch <- StreamEvent{Done: true}
			return
		}
		if data == "" || data[0] != '{' {
			continue
		}

		var j map[string]any
		if json.Unmarshal([]byte(data), &j) != nil {
			continue
		}

		// Check for usage (typically on the final chunk).
		if u, ok := j["usage"].(map[string]any); ok {
			usage := &UsageData{
				InputTokens:  intFromAny(u["prompt_tokens"]),
				OutputTokens: intFromAny(u["completion_tokens"]),
			}
			if tm, ok := j["timings"].(map[string]any); ok {
				if v, ok := tm["predicted_per_second"].(float64); ok {
					usage.GenTPS = v
				}
				if v, ok := tm["prompt_per_second"].(float64); ok {
					usage.PrefillTPS = v
				}
			}
			ch <- StreamEvent{Usage: usage}
		}

		choices, _ := j["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c0, _ := choices[0].(map[string]any)
		if c0 == nil {
			continue
		}
		delta, _ := c0["delta"].(map[string]any)
		if delta == nil {
			continue
		}

		// Text content.
		if content, ok := delta["content"].(string); ok && content != "" {
			ch <- StreamEvent{Delta: content}
		}
		// Reasoning/thinking content.
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			ch <- StreamEvent{Delta: reasoning, Thinking: true}
		} else if reasoning, ok := delta["reasoning"].(string); ok && reasoning != "" {
			ch <- StreamEvent{Delta: reasoning, Thinking: true}
		} else if thinking, ok := delta["thinking"].(string); ok && thinking != "" {
			ch <- StreamEvent{Delta: thinking, Thinking: true}
		}

		// Tool calls — accumulate across chunks.
		if tcs, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				idx := intFromAny(tcMap["index"])
				fn, _ := tcMap["function"].(map[string]any)
				if fn == nil {
					fn = map[string]any{}
				}
				if _, exists := tcAcc[idx]; !exists {
					tcAcc[idx] = &ToolCall{}
				}
				if id, ok := tcMap["id"].(string); ok && id != "" {
					tcAcc[idx].ID = id
				}
				if name, ok := fn["name"].(string); ok && name != "" {
					tcAcc[idx].Name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					tcAcc[idx].Arguments += args
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("SSE scan error", "err", err)
	}
	// If we didn't get [DONE], still close cleanly.
	ch <- StreamEvent{Done: true}
}

// ─── Anthropic streaming ─────────────────────────────────────────────

func buildAnthropicRequest(req CompletionRequest) (string, []byte, map[string]string, error) {
	base := strings.TrimRight(req.URL, "/")
	if !strings.HasSuffix(base, "/v1/messages") {
		base += "/v1/messages"
	}

	// Convert messages: extract system, convert roles.
	var system string
	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
			continue
		}
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}

	payload := map[string]any{
		"model":      req.Model,
		"messages":   msgs,
		"stream":     true,
		"max_tokens": max(req.MaxTokens, 4096),
	}
	if system != "" {
		payload["system"] = system
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, nil, err
	}

	headers := map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": "2023-06-01",
	}
	for k, v := range req.Headers {
		headers[k] = v
	}
	return base, body, headers, nil
}

func (c *Client) streamAnthropic(ctx context.Context, url string, body []byte, headers map[string]string, ch chan<- StreamEvent) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		ch <- StreamEvent{Error: &StreamErr{Message: err.Error(), Status: 500}}
		return
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		ch <- StreamEvent{Error: &StreamErr{Message: fmt.Sprintf("Connect error: %v", err), Status: 502}}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		ch <- StreamEvent{Error: &StreamErr{Message: string(raw), Status: resp.StatusCode}}
		return
	}

	var inputTokens, outputTokens int
	toolBlocks := map[int]*ToolCall{}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" || data[0] != '{' {
			continue
		}
		var j map[string]any
		if json.Unmarshal([]byte(data), &j) != nil {
			continue
		}

		evt, _ := j["type"].(string)
		switch evt {
		case "message_start":
			if msg, ok := j["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					inputTokens = intFromAny(u["input_tokens"])
				}
			}
		case "content_block_start":
			idx := intFromAny(j["index"])
			if cb, ok := j["content_block"].(map[string]any); ok {
				if cbType, _ := cb["type"].(string); cbType == "tool_use" {
					toolBlocks[idx] = &ToolCall{
						ID:   stringFromAny(cb["id"]),
						Name: stringFromAny(cb["name"]),
					}
				}
			}
		case "content_block_delta":
			if delta, ok := j["delta"].(map[string]any); ok {
				deltaType, _ := delta["type"].(string)
				switch deltaType {
				case "text_delta":
					if text, ok := delta["text"].(string); ok && text != "" {
						ch <- StreamEvent{Delta: text}
					}
				case "input_json_delta":
					idx := intFromAny(j["index"])
					if tb, ok := toolBlocks[idx]; ok {
						if partial, ok := delta["partial_json"].(string); ok {
							tb.Arguments += partial
						}
					}
				}
			}
		case "message_delta":
			if u, ok := j["usage"].(map[string]any); ok {
				outputTokens = intFromAny(u["output_tokens"])
			}
		case "message_stop":
			if len(toolBlocks) > 0 {
				calls := make([]ToolCall, 0, len(toolBlocks))
				for i := 0; i < len(toolBlocks)+10; i++ {
					if tb, ok := toolBlocks[i]; ok {
						calls = append(calls, *tb)
					}
				}
				ch <- StreamEvent{ToolCalls: calls}
			}
			if inputTokens > 0 || outputTokens > 0 {
				ch <- StreamEvent{Usage: &UsageData{InputTokens: inputTokens, OutputTokens: outputTokens}}
			}
			ch <- StreamEvent{Done: true}
			return
		case "error":
			if errObj, ok := j["error"].(map[string]any); ok {
				msg, _ := errObj["message"].(string)
				ch <- StreamEvent{Error: &StreamErr{Message: msg, Status: 400}}
			}
			return
		}
	}
	ch <- StreamEvent{Done: true}
}

// ─── Ollama native streaming ─────────────────────────────────────────

func buildOllamaRequest(req CompletionRequest) (string, []byte, map[string]string, error) {
	base := strings.TrimRight(req.URL, "/")
	if !strings.HasSuffix(base, "/api/chat") {
		base += "/api/chat"
	}

	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}

	payload := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
	}
	if req.Temperature > 0 {
		payload["options"] = map[string]any{"temperature": req.Temperature}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, nil, err
	}
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range req.Headers {
		headers[k] = v
	}
	return base, body, headers, nil
}

func (c *Client) streamOllama(ctx context.Context, url string, body []byte, headers map[string]string, ch chan<- StreamEvent) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		ch <- StreamEvent{Error: &StreamErr{Message: err.Error(), Status: 500}}
		return
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		ch <- StreamEvent{Error: &StreamErr{Message: fmt.Sprintf("Connect error: %v", err), Status: 502}}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		ch <- StreamEvent{Error: &StreamErr{Message: string(raw), Status: resp.StatusCode}}
		return
	}

	// Ollama sends newline-delimited JSON, not SSE.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var j map[string]any
		if json.Unmarshal([]byte(line), &j) != nil {
			continue
		}

		if msg, ok := j["message"].(map[string]any); ok {
			if content, ok := msg["content"].(string); ok && content != "" {
				ch <- StreamEvent{Delta: content}
			}
		}

		if done, _ := j["done"].(bool); done {
			usage := &UsageData{}
			if v, ok := j["prompt_eval_count"].(float64); ok {
				usage.InputTokens = int(v)
			}
			if v, ok := j["eval_count"].(float64); ok {
				usage.OutputTokens = int(v)
			}
			if v, ok := j["eval_duration"].(float64); ok && v > 0 {
				if count, ok := j["eval_count"].(float64); ok && count > 0 {
					usage.GenTPS = count / (v / 1e9)
				}
			}
			ch <- StreamEvent{Usage: usage}
			ch <- StreamEvent{Done: true}
			return
		}
	}
	ch <- StreamEvent{Done: true}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func isOllamaOpenAICompat(rawURL string) bool {
	return strings.Contains(strings.ToLower(rawURL), "/v1")
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}
