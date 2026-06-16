package llm

import (
	"net/url"
	"strings"
)

// Provider identifies a known LLM endpoint type.
type Provider string

const (
	ProviderOpenAI     Provider = "openai"
	ProviderAnthropic  Provider = "anthropic"
	ProviderOllama     Provider = "ollama"
	ProviderOpenRouter Provider = "openrouter"
	ProviderGroq       Provider = "groq"
	ProviderGeneric    Provider = "generic" // generic OpenAI-compatible
)

// DetectProvider guesses the provider from a base URL.
// Matches Python's _detect_provider in llm_core.py.
func DetectProvider(baseURL string) Provider {
	lower := strings.ToLower(baseURL)

	switch {
	case strings.Contains(lower, "api.anthropic.com"):
		return ProviderAnthropic
	case strings.Contains(lower, "openrouter.ai"):
		return ProviderOpenRouter
	case strings.Contains(lower, "api.groq.com"):
		return ProviderGroq
	case isOllamaURL(lower):
		return ProviderOllama
	case strings.Contains(lower, "api.openai.com"):
		return ProviderOpenAI
	default:
		return ProviderGeneric
	}
}

// isOllamaURL checks for Ollama's native endpoint pattern.
func isOllamaURL(lower string) bool {
	// Ollama runs on :11434 by default and doesn't have /v1 unless
	// the user specifically hits the OpenAI compat layer.
	if strings.Contains(lower, ":11434") && !strings.Contains(lower, "/v1") {
		return true
	}
	if strings.Contains(lower, "/api/chat") || strings.Contains(lower, "/api/generate") {
		return true
	}
	return false
}

// NormalizeChatURL ensures the URL ends with /chat/completions for
// OpenAI-compatible providers. Returns the URL unchanged for Ollama/Anthropic.
func NormalizeChatURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")

	// Strip common trailing paths to get the base.
	for _, suffix := range []string{"/chat/completions", "/completions", "/v1/messages", "/models"} {
		if strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
			break
		}
	}

	provider := DetectProvider(base)
	switch provider {
	case ProviderOllama:
		// Ollama native: /api/chat
		if !strings.HasSuffix(base, "/api/chat") {
			return strings.TrimRight(base, "/") + "/api/chat"
		}
		return base
	case ProviderAnthropic:
		if !strings.HasSuffix(base, "/v1/messages") {
			return strings.TrimRight(base, "/") + "/v1/messages"
		}
		return base
	default:
		// OpenAI-compatible
		if !strings.HasSuffix(base, "/v1") {
			// Check if it already has /v1 in the path.
			u, err := url.Parse(base)
			if err == nil && !strings.HasPrefix(u.Path, "/v1") {
				base += "/v1"
			}
		}
		return base + "/chat/completions"
	}
}
