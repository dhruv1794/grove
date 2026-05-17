package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	anthropicVersion          = "2023-06-01"
	anthropicDefaultMaxTokens = 4096
)

// anthropicClient speaks the Anthropic Messages API. Its wire shape differs
// from OpenAI's: system text is a top-level field, max_tokens is required,
// and token usage is split into input_tokens / output_tokens.
type anthropicClient struct {
	spec    ModelSpec
	baseURL string
	apiKey  string
	hc      *http.Client
}

func (c *anthropicClient) Model() ModelSpec { return c.spec }

func (c *anthropicClient) CountTokens(_ context.Context, req Request) (int, error) {
	return estimateTokens(req), nil
}

func (c *anthropicClient) endpoint() string { return c.baseURL + "/v1/messages" }

func (c *anthropicClient) headers() map[string]string {
	h := map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": anthropicVersion,
	}
	if c.apiKey != "" {
		h["x-api-key"] = c.apiKey
	}
	return h
}

// splitSystem pulls system messages into a single system string and returns
// the remaining turns as the Messages-API messages array.
func splitSystem(req Request) (string, []map[string]string) {
	var system []string
	var msgs []map[string]string
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			system = append(system, m.Content)
			continue
		}
		msgs = append(msgs, map[string]string{"role": string(m.Role), "content": m.Content})
	}
	return strings.Join(system, "\n\n"), msgs
}

func (c *anthropicClient) buildBody(req Request, stream bool) ([]byte, error) {
	system, msgs := splitSystem(req)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	body := map[string]any{
		"model":      c.spec.Name,
		"max_tokens": maxTokens,
		"messages":   msgs,
	}
	if system != "" {
		body["system"] = system
	}
	if req.Temperature != 0 {
		body["temperature"] = req.Temperature
	}
	if len(req.Stop) > 0 {
		body["stop_sequences"] = req.Stop
	}
	if stream {
		body["stream"] = true
	}
	return json.Marshal(body)
}

// anthropicUsage carries the Messages-API split token counts. Streaming
// reports input on message_start and output on message_delta.
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (c *anthropicClient) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := c.buildBody(req, false)
	if err != nil {
		return nil, fmt.Errorf("encode anthropic request: %w", err)
	}
	httpResp, err := httpPost(ctx, c.hc, c.spec, c.endpoint(), c.headers(), body)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read anthropic response: %w", err)
	}

	var out struct {
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}

	var text strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	resp := &Response{
		Content:      text.String(),
		Model:        out.Model,
		FinishReason: out.StopReason,
		Usage: Usage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
			TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
		},
	}
	resp.Cost = costFor(c.spec, resp.Usage)
	return resp, nil
}

func (c *anthropicClient) Stream(ctx context.Context, req Request, onChunk func(string) error) (*Response, error) {
	body, err := c.buildBody(req, true)
	if err != nil {
		return nil, fmt.Errorf("encode anthropic request: %w", err)
	}
	httpResp, err := httpPost(ctx, c.hc, c.spec, c.endpoint(), c.headers(), body)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	resp := &Response{Model: c.spec.Name}
	var content strings.Builder

	err = scanSSE(c.spec, httpResp.Body, func(payload string) (bool, error) {
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Message struct {
				Model string         `json:"model"`
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Usage anthropicUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return false, fmt.Errorf("decode anthropic stream event: %w", err)
		}
		switch ev.Type {
		case "message_start":
			if ev.Message.Model != "" {
				resp.Model = ev.Message.Model
			}
			resp.Usage.PromptTokens = ev.Message.Usage.InputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				content.WriteString(ev.Delta.Text)
				if err := onChunk(ev.Delta.Text); err != nil {
					return false, err
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				resp.FinishReason = ev.Delta.StopReason
			}
			if ev.Usage.OutputTokens != 0 {
				resp.Usage.CompletionTokens = ev.Usage.OutputTokens
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	resp.Content = content.String()
	resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	resp.Cost = costFor(c.spec, resp.Usage)
	return resp, nil
}
