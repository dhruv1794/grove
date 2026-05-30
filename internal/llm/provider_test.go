package llm

import (
	"errors"
	"testing"

	"grove/internal/core"
)

func TestParseModel(t *testing.T) {
	ok := []struct {
		in       string
		provider string
		name     string
	}{
		{"ollama/qwen2.5:32b", "ollama", "qwen2.5:32b"},
		{"anthropic/claude-haiku-4-5", "anthropic", "claude-haiku-4-5"},
		{"together/meta-llama/Llama-3-70b", "together", "meta-llama/Llama-3-70b"},
	}
	for _, c := range ok {
		spec, err := ParseModel(c.in)
		if err != nil {
			t.Fatalf("ParseModel(%q): unexpected error %v", c.in, err)
		}
		if spec.Provider != c.provider || spec.Name != c.name {
			t.Errorf("ParseModel(%q) = %+v, want {%s %s}", c.in, spec, c.provider, c.name)
		}
		if spec.String() != c.in {
			t.Errorf("String() = %q, want %q", spec.String(), c.in)
		}
	}

	for _, bad := range []string{"", "noprovider", "/name", "provider/"} {
		if _, err := ParseModel(bad); err == nil {
			t.Errorf("ParseModel(%q): expected error", bad)
		}
	}
}

func TestNewDispatch(t *testing.T) {
	cases := []struct {
		model      string
		opts       Options
		wantOpenAI bool
		wantErr    bool
	}{
		{model: "openai/gpt-4o-mini", opts: Options{OpenAIKey: "k"}, wantOpenAI: true},
		{model: "ollama/llama3.1:8b", wantOpenAI: true},
		{model: "groq/llama-3", opts: Options{APIKey: "k"}, wantOpenAI: true},
		{model: "anthropic/claude-haiku-4-5", opts: Options{AnthropicKey: "k"}, wantOpenAI: false},
		{model: "vllm/local", wantErr: true}, // needs an explicit BaseURL
		{model: "vllm/local", opts: Options{BaseURL: "http://localhost:8000/v1"}, wantOpenAI: true},
		{model: "bogus/x", wantErr: true},
	}
	for _, c := range cases {
		spec, err := ParseModel(c.model)
		if err != nil {
			t.Fatalf("ParseModel(%q): %v", c.model, err)
		}
		got, err := New(spec, c.opts)
		if c.wantErr {
			if err == nil {
				t.Errorf("New(%q): expected error", c.model)
			}
			continue
		}
		if err != nil {
			t.Fatalf("New(%q): unexpected error %v", c.model, err)
		}
		_, isOpenAI := got.(*openaiClient)
		if isOpenAI != c.wantOpenAI {
			t.Errorf("New(%q): isOpenAI=%v, want %v", c.model, isOpenAI, c.wantOpenAI)
		}
		if got.Model() != spec {
			t.Errorf("New(%q): Model()=%v, want %v", c.model, got.Model(), spec)
		}
	}
}

func TestNewUnknownProviderIsMisuse(t *testing.T) {
	_, err := New(ModelSpec{Provider: "bogus", Name: "x"}, Options{})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindMisuse {
		t.Fatalf("expected core misuse error, got %v", err)
	}
}

// OptionsFromEnv must read exactly the documented GROVE_* credential vars; a
// rename of any of them would silently break credential pickup for build/ask.
func TestOptionsFromEnv(t *testing.T) {
	t.Setenv("GROVE_OPENAI_API_KEY", "sk-openai")
	t.Setenv("GROVE_ANTHROPIC_API_KEY", "sk-anthropic")
	t.Setenv("GROVE_OLLAMA_HOST", "http://box:11434")

	opts := OptionsFromEnv()
	if opts.OpenAIKey != "sk-openai" {
		t.Errorf("OpenAIKey = %q, want sk-openai", opts.OpenAIKey)
	}
	if opts.AnthropicKey != "sk-anthropic" {
		t.Errorf("AnthropicKey = %q, want sk-anthropic", opts.AnthropicKey)
	}
	if opts.OllamaHost != "http://box:11434" {
		t.Errorf("OllamaHost = %q, want http://box:11434", opts.OllamaHost)
	}
}

// Unset env vars leave the corresponding Options fields empty.
func TestOptionsFromEnv_Unset(t *testing.T) {
	t.Setenv("GROVE_OPENAI_API_KEY", "")
	t.Setenv("GROVE_ANTHROPIC_API_KEY", "")
	t.Setenv("GROVE_OLLAMA_HOST", "")

	if opts := OptionsFromEnv(); opts != (Options{}) {
		t.Errorf("OptionsFromEnv with unset env = %+v, want zero Options", opts)
	}
}

func TestOllamaHostOverride(t *testing.T) {
	c, err := New(ModelSpec{Provider: "ollama", Name: "x"}, Options{OllamaHost: "http://box:9999"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.(*openaiClient).baseURL; got != "http://box:9999/v1" {
		t.Errorf("baseURL = %q, want http://box:9999/v1", got)
	}
}

func TestHTTPStatusError_ContextLength(t *testing.T) {
	spec := ModelSpec{Provider: "ollama", Name: "bge-m3"}
	// A 400 describing a context overflow → ErrContextLength (so embed can retry).
	err := httpStatusError(spec, 400, []byte(`{"error":"input length exceeds context length"}`))
	if !errors.Is(err, ErrContextLength) {
		t.Errorf("400 context body: got %v, want ErrContextLength", err)
	}
	// A 400 that is not a context overflow → not ErrContextLength.
	other := httpStatusError(spec, 400, []byte(`{"error":"bad request: unknown field"}`))
	if errors.Is(other, ErrContextLength) {
		t.Errorf("generic 400 misclassified as ErrContextLength: %v", other)
	}
	// Non-400 stays unaffected.
	if errors.Is(httpStatusError(spec, 404, []byte("not found")), ErrContextLength) {
		t.Error("404 misclassified as ErrContextLength")
	}
}

func TestIsContextLengthError(t *testing.T) {
	hits := []string{
		"input length exceeds context length",
		"This model's maximum context length is 512 tokens",
		"prompt is too long: 215263 tokens > 200000 maximum",
		"requested 9000 tokens, longer than the model's context window",
	}
	for _, s := range hits {
		if !isContextLengthError(s) {
			t.Errorf("isContextLengthError(%q) = false, want true", s)
		}
	}
	// Ambiguous phrases without a token/context cue must NOT classify as overflow.
	for _, s := range []string{
		"unauthorized", "model not found", "rate limited",
		"field 'name' value is too long",
		"input length must be a positive integer",
	} {
		if isContextLengthError(s) {
			t.Errorf("isContextLengthError(%q) = true, want false", s)
		}
	}
}
