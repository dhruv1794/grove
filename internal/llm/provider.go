package llm

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"grove/internal/core"
)

// ModelSpec is a parsed "provider/name" model identifier.
type ModelSpec struct {
	Provider string
	Name     string
}

func (m ModelSpec) String() string { return m.Provider + "/" + m.Name }

// ParseModel splits a "provider/name" model identifier. A model name may
// itself contain slashes (e.g. together/meta-llama/Llama-3-70b), so only the
// first segment is taken as the provider.
func ParseModel(s string) (ModelSpec, error) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return ModelSpec{}, core.NewError(core.KindMisuse,
			fmt.Sprintf("invalid model %q", s),
			"use the form provider/name, e.g. ollama/qwen2.5:32b or anthropic/claude-haiku-4-5")
	}
	return ModelSpec{Provider: s[:i], Name: s[i+1:]}, nil
}

// Options carries credentials and transport configuration for New. The CLI
// populates it from flags, env vars (OptionsFromEnv), and config.toml.
type Options struct {
	OpenAIKey    string // GROVE_OPENAI_API_KEY
	AnthropicKey string // GROVE_ANTHROPIC_API_KEY
	APIKey       string // generic key for other OpenAI-compatible providers (together, groq, vllm)
	OllamaHost   string // GROVE_OLLAMA_HOST, e.g. http://localhost:11434
	BaseURL      string // overrides the provider's default endpoint (required for vllm, llamacpp)
	HTTPClient   *http.Client
}

// OptionsFromEnv reads the GROVE_* credential env vars. Other Options fields
// stay zero; the caller layers flag and config values on top.
func OptionsFromEnv() Options {
	return Options{
		OpenAIKey:    os.Getenv("GROVE_OPENAI_API_KEY"),
		AnthropicKey: os.Getenv("GROVE_ANTHROPIC_API_KEY"),
		OllamaHost:   os.Getenv("GROVE_OLLAMA_HOST"),
	}
}

// openAIBaseURLs maps a known OpenAI-compatible provider to its base URL.
// Providers that need a user-supplied endpoint map to "".
var openAIBaseURLs = map[string]string{
	"openai":   "https://api.openai.com/v1",
	"together": "https://api.together.xyz/v1",
	"groq":     "https://api.groq.com/openai/v1",
	"ollama":   "http://localhost:11434/v1",
	"vllm":     "",
	"llamacpp": "",
}

const anthropicDefaultBaseURL = "https://api.anthropic.com"

// New constructs an LLM for spec, dispatching on the provider. anthropic uses
// the Messages API adapter; every other known provider uses the
// OpenAI-compatible transport. No network call is made here.
func New(spec ModelSpec, opts Options) (LLM, error) {
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}

	if spec.Provider == "anthropic" {
		base := anthropicDefaultBaseURL
		if opts.BaseURL != "" {
			base = strings.TrimRight(opts.BaseURL, "/")
		}
		return &anthropicClient{spec: spec, baseURL: base, apiKey: opts.AnthropicKey, hc: hc}, nil
	}

	base, known := openAIBaseURLs[spec.Provider]
	if !known {
		return nil, core.NewError(core.KindMisuse,
			fmt.Sprintf("unknown model provider %q", spec.Provider),
			"supported providers: openai, anthropic, ollama, together, groq, vllm, llamacpp")
	}
	if spec.Provider == "ollama" && opts.OllamaHost != "" {
		base = strings.TrimRight(opts.OllamaHost, "/") + "/v1"
	}
	if opts.BaseURL != "" {
		base = strings.TrimRight(opts.BaseURL, "/")
	}
	if base == "" {
		return nil, core.NewError(core.KindMisuse,
			fmt.Sprintf("provider %q has no default endpoint", spec.Provider),
			"set the base URL via Options.BaseURL (or --config)")
	}

	key := opts.APIKey
	if spec.Provider == "openai" && key == "" {
		key = opts.OpenAIKey
	}
	return &openaiClient{spec: spec, baseURL: base, apiKey: key, hc: hc}, nil
}

// httpStatusError maps a non-200 provider response to a structured grove
// error. Most map to KindModelUnreachable (exit code 5).
func httpStatusError(spec ModelSpec, status int, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 300 {
		snippet = snippet[:300] + "…"
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return core.NewError(core.KindModelUnreachable,
			fmt.Sprintf("model %s rejected the request (HTTP %d)", spec, status),
			"check the API key for this provider is set and valid")
	case http.StatusNotFound:
		return core.NewError(core.KindModelUnreachable,
			fmt.Sprintf("model %s not found (HTTP 404)", spec),
			"check the model name; for ollama, pull it first with `ollama pull <name>`")
	case http.StatusTooManyRequests:
		return core.NewError(core.KindModelUnreachable,
			fmt.Sprintf("model %s is rate-limited (HTTP 429)", spec),
			"wait and retry, or lower --concurrency")
	default:
		return core.NewError(core.KindModelUnreachable,
			fmt.Sprintf("model %s returned HTTP %d: %s", spec, status, snippet),
			"check the provider status and the model name")
	}
}
