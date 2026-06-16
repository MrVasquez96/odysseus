// Package llm provides an OpenAI-compatible LLM client with SSE streaming.
// Handles multiple providers: OpenAI-compatible, Anthropic, Ollama.
package llm

// ChatMessage is a single message in a chat conversation.
type ChatMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall represents a tool invocation from the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// UsageData tracks token consumption.
type UsageData struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	GenTPS       float64 `json:"gen_tps,omitempty"`
	PrefillTPS   float64 `json:"prefill_tps,omitempty"`
	Model        string  `json:"model,omitempty"`
}

// StreamEvent is a typed event yielded during SSE streaming.
type StreamEvent struct {
	// Exactly one of these is set per event.
	Delta     string     `json:"delta,omitempty"`     // text content chunk
	Thinking  bool       `json:"thinking,omitempty"`  // true if delta is thinking/reasoning
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     *UsageData `json:"usage,omitempty"`
	Error     *StreamErr `json:"error,omitempty"`
	Done      bool       `json:"done,omitempty"` // stream finished
	// ModelActual is set when the upstream reports a different model than requested.
	ModelActual string `json:"model_actual,omitempty"`
}

// StreamErr describes an upstream error during streaming.
type StreamErr struct {
	Message string `json:"error"`
	Status  int    `json:"status"`
}

// CompletionRequest holds parameters for a chat completion.
type CompletionRequest struct {
	URL         string            `json:"url"`
	Model       string            `json:"model"`
	Messages    []ChatMessage     `json:"messages"`
	Temperature float64           `json:"temperature"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Stream      bool              `json:"stream"`
	Tools       []any             `json:"tools,omitempty"`
	Headers     map[string]string `json:"-"` // extra headers (e.g. Authorization)
	TimeoutSec  int               `json:"-"` // stream read timeout
}
