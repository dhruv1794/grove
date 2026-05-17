package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestOllamaSmoke makes a real call to a local Ollama. It is skipped unless
// GROVE_LLM_SMOKE=1, so the default `go test ./...` run stays offline.
// Override the model with GROVE_LLM_SMOKE_MODEL (default ollama/llama3.1:8b).
func TestOllamaSmoke(t *testing.T) {
	if os.Getenv("GROVE_LLM_SMOKE") != "1" {
		t.Skip("set GROVE_LLM_SMOKE=1 to run the live Ollama smoke test")
	}
	model := os.Getenv("GROVE_LLM_SMOKE_MODEL")
	if model == "" {
		model = "ollama/llama3.1:8b"
	}
	spec, err := ParseModel(model)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(spec, OptionsFromEnv())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := Request{
		Messages: []Message{
			{Role: RoleSystem, Content: "Reply with exactly one word."},
			{Role: RoleUser, Content: "Say the word grove."},
		},
		MaxTokens: 16,
	}

	resp, err := c.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Content) == "" {
		t.Error("Complete returned empty content")
	}
	t.Logf("Complete: %q usage=%+v cost=%+v", resp.Content, resp.Usage, resp.Cost)

	var streamed strings.Builder
	sResp, err := c.Stream(ctx, req, func(s string) error {
		streamed.WriteString(s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamed.Len() == 0 {
		t.Error("Stream produced no chunks")
	}
	t.Logf("Stream: %q usage=%+v cost=%+v", sResp.Content, sResp.Usage, sResp.Cost)
}
