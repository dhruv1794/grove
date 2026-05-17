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

func TestOllamaHostOverride(t *testing.T) {
	c, err := New(ModelSpec{Provider: "ollama", Name: "x"}, Options{OllamaHost: "http://box:9999"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.(*openaiClient).baseURL; got != "http://box:9999/v1" {
		t.Errorf("baseURL = %q, want http://box:9999/v1", got)
	}
}
