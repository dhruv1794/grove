// Package llm is grove's model-calling core. It exposes one LLM interface
// over an OpenAI-compatible transport (OpenAI, Ollama, llama.cpp, vLLM,
// Together, Groq) and an Anthropic adapter. Build and query each pick their
// own model independently — no model is ever baked in, and a model is
// contacted only when a caller explicitly constructs an LLM for it.
//
// It depends only on core and the standard library; it does not import
// connectors or cli.
package llm

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}

type Request struct {
	Messages    []Message
	Temperature float64
	MaxTokens   int
	Stop        []string
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Cost is the USD billed for one call. Estimated is true when the figure is
// not authoritative — either the model has no pricing-table entry, or the
// provider returned no token usage and the count was approximated. The CLI
// must flag estimated costs rather than present them as exact.
type Cost struct {
	USD       float64 `json:"usd"`
	Estimated bool    `json:"estimated"`
}

type Response struct {
	Content      string
	Model        string
	FinishReason string
	Usage        Usage
	Cost         Cost
}

// LLM is the contract every provider adapter implements. Complete and Stream
// return identical Response data; Stream additionally invokes onChunk for
// each token delta as it arrives.
type LLM interface {
	Complete(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request, onChunk func(string) error) (*Response, error)
	CountTokens(ctx context.Context, req Request) (int, error)
	Model() ModelSpec
}

// Tally aggregates Usage and Cost across many LLM calls — e.g. every node
// generated in one build run. Callers surface it in the CLI and persist it
// to build_history rows.
type Tally struct {
	Calls     int     `json:"calls"`
	Usage     Usage   `json:"usage"`
	USD       float64 `json:"usd"`
	Estimated bool    `json:"estimated"` // true if any constituent call's cost was estimated
}

func (t *Tally) Add(r *Response) {
	if r == nil {
		return
	}
	t.Calls++
	t.Usage.PromptTokens += r.Usage.PromptTokens
	t.Usage.CompletionTokens += r.Usage.CompletionTokens
	t.Usage.TotalTokens += r.Usage.TotalTokens
	t.USD += r.Cost.USD
	if r.Cost.Estimated {
		t.Estimated = true
	}
}

// approxTokens approximates a token count from a character count
// (~4 chars/token). The chars-per-token ratio lives only here.
func approxTokens(chars int) int { return (chars + 3) / 4 }

// estimateTokens approximates a request's prompt token count from character
// length (plus per-message framing). It is used for pre-call budgeting and as
// a fallback when a provider omits usage; Response.Usage carries the
// authoritative count whenever the provider reports it.
func estimateTokens(req Request) int {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Content) + len(m.Role) + 4
	}
	return approxTokens(n)
}
