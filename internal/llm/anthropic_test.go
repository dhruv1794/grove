package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func anthropicTestClient(t *testing.T, h http.HandlerFunc) *anthropicClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	spec := ModelSpec{Provider: "anthropic", Name: "claude-haiku-4-5"}
	c, err := New(spec, Options{AnthropicKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*anthropicClient)
}

func TestAnthropicComplete(t *testing.T) {
	var gotBody map[string]any
	c := anthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if k := r.Header.Get("x-api-key"); k != "test-key" {
			t.Errorf("x-api-key = %q", k)
		}
		if v := r.Header.Get("anthropic-version"); v != anthropicVersion {
			t.Errorf("anthropic-version = %q", v)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("request body: %v", err)
		}
		w.Write([]byte(`{
			"model": "claude-haiku-4-5",
			"stop_reason": "end_turn",
			"content": [{"type": "text", "text": "answer"}],
			"usage": {"input_tokens": 1000000, "output_tokens": 1000000}
		}`))
	})

	resp, err := c.Complete(context.Background(), Request{
		Messages: []Message{
			{Role: RoleSystem, Content: "be terse"},
			{Role: RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "answer" || resp.FinishReason != "end_turn" {
		t.Errorf("content=%q finish=%q", resp.Content, resp.FinishReason)
	}
	if resp.Usage.TotalTokens != 2000000 {
		t.Errorf("TotalTokens = %d", resp.Usage.TotalTokens)
	}
	// 1M input @ $1.00 + 1M output @ $5.00.
	if resp.Cost.USD != 6.0 || resp.Cost.Estimated {
		t.Errorf("Cost = %+v, want {6 false}", resp.Cost)
	}

	// system message must be lifted out of messages into the top-level field.
	if gotBody["system"] != "be terse" {
		t.Errorf("system = %v", gotBody["system"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1 (system excluded)", len(msgs))
	}
	if _, ok := gotBody["max_tokens"]; !ok {
		t.Error("max_tokens must always be sent to the Messages API")
	}
}

func TestAnthropicStream(t *testing.T) {
	c := anthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})

	var chunks []string
	resp, err := c.Stream(context.Background(),
		Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}},
		func(s string) error { chunks = append(chunks, s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(chunks, "") != "Hello" || resp.Content != "Hello" {
		t.Errorf("chunks=%v content=%q", chunks, resp.Content)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 11 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
}

func TestAnthropicDefaultMaxTokens(t *testing.T) {
	var gotBody map[string]any
	c := anthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"model":"m","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	})
	if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := gotBody["max_tokens"].(float64); int(got) != anthropicDefaultMaxTokens {
		t.Errorf("max_tokens = %v, want default %d", gotBody["max_tokens"], anthropicDefaultMaxTokens)
	}
}
