package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"grove/internal/core"
)

// openAITestClient builds an openaiClient pointed at a test server.
func openAITestClient(t *testing.T, h http.HandlerFunc) (*openaiClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	spec := ModelSpec{Provider: "openai", Name: "gpt-4o-mini"}
	c, err := New(spec, Options{OpenAIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*openaiClient), srv
}

func TestOpenAIComplete(t *testing.T) {
	var gotBody map[string]any
	c, _ := openAITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("Authorization = %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("request body: %v", err)
		}
		w.Write([]byte(`{
			"model": "gpt-4o-mini",
			"choices": [{"message": {"content": "hello there"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1000000, "completion_tokens": 1000000, "total_tokens": 2000000}
		}`))
	})

	resp, err := c.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello there" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.Usage.TotalTokens != 2000000 {
		t.Errorf("TotalTokens = %d", resp.Usage.TotalTokens)
	}
	// 1M prompt @ $0.15 + 1M completion @ $0.60.
	if resp.Cost.USD != 0.75 || resp.Cost.Estimated {
		t.Errorf("Cost = %+v, want {0.75 false}", resp.Cost)
	}
	if gotBody["model"] != "gpt-4o-mini" {
		t.Errorf("request model = %v", gotBody["model"])
	}
}

func TestOpenAIStream(t *testing.T) {
	c, _ := openAITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
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
	if resp.Usage.TotalTokens != 5 || resp.FinishReason != "stop" {
		t.Errorf("usage=%+v finish=%q", resp.Usage, resp.FinishReason)
	}
	if resp.Cost.Estimated {
		t.Error("cost should not be estimated when usage was streamed")
	}
}

func TestOpenAIStreamWithoutUsageIsEstimated(t *testing.T) {
	c, _ := openAITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	resp, err := c.Stream(context.Background(),
		Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}},
		func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Cost.Estimated {
		t.Error("cost should be estimated when no usage block streamed")
	}
	if resp.Usage.TotalTokens == 0 {
		t.Error("expected estimated token counts")
	}
}

func TestOpenAIHTTPErrorIsModelUnreachable(t *testing.T) {
	c, _ := openAITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key"}`)
	})
	_, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindModelUnreachable {
		t.Fatalf("expected model_unreachable, got %v", err)
	}
}

func TestOpenAIUnreachableHost(t *testing.T) {
	spec := ModelSpec{Provider: "openai", Name: "gpt-4o-mini"}
	// Port 1 on loopback — connection refused immediately, no hang.
	c, err := New(spec, Options{OpenAIKey: "k", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindModelUnreachable {
		t.Fatalf("expected model_unreachable, got %v", err)
	}
}
