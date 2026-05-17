package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"grove/internal/core"
)

// openaiClient speaks the OpenAI Chat Completions API. The same wire format
// is served by Ollama, llama.cpp, vLLM, Together and Groq, so this one
// adapter covers every provider except Anthropic.
type openaiClient struct {
	spec    ModelSpec
	baseURL string
	apiKey  string
	hc      *http.Client
}

func (c *openaiClient) Model() ModelSpec { return c.spec }

func (c *openaiClient) CountTokens(_ context.Context, req Request) (int, error) {
	return estimateTokens(req), nil
}

func (c *openaiClient) endpoint() string { return c.baseURL + "/chat/completions" }

func (c *openaiClient) headers() map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	if c.apiKey != "" {
		h["Authorization"] = "Bearer " + c.apiKey
	}
	return h
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u openaiUsage) toUsage() Usage {
	return Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

func (c *openaiClient) buildBody(req Request, stream bool) ([]byte, error) {
	msgs := make([]map[string]string, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = map[string]string{"role": string(m.Role), "content": m.Content}
	}
	body := map[string]any{
		"model":    c.spec.Name,
		"messages": msgs,
	}
	if req.Temperature != 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(body)
}

func (c *openaiClient) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := c.buildBody(req, false)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}
	httpResp, err := httpPost(ctx, c.hc, c.spec, c.endpoint(), c.headers(), body)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage openaiUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, core.NewError(core.KindModelUnreachable,
			fmt.Sprintf("model %s returned no choices", c.spec),
			"check the model name and the request")
	}

	resp := &Response{
		Content:      out.Choices[0].Message.Content,
		Model:        out.Model,
		FinishReason: out.Choices[0].FinishReason,
		Usage:        out.Usage.toUsage(),
	}
	resp.Cost = costFor(c.spec, resp.Usage)
	return resp, nil
}

func (c *openaiClient) Stream(ctx context.Context, req Request, onChunk func(string) error) (*Response, error) {
	body, err := c.buildBody(req, true)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}
	httpResp, err := httpPost(ctx, c.hc, c.spec, c.endpoint(), c.headers(), body)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	resp := &Response{Model: c.spec.Name}
	var content strings.Builder
	var gotUsage bool

	err = scanSSE(c.spec, httpResp.Body, func(payload string) (bool, error) {
		if payload == "[DONE]" {
			return true, nil
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *openaiUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return false, fmt.Errorf("decode openai stream chunk: %w", err)
		}
		if chunk.Model != "" {
			resp.Model = chunk.Model
		}
		if chunk.Usage != nil {
			resp.Usage = chunk.Usage.toUsage()
			gotUsage = true
		}
		if len(chunk.Choices) > 0 {
			if d := chunk.Choices[0].Delta.Content; d != "" {
				content.WriteString(d)
				if err := onChunk(d); err != nil {
					return false, err
				}
			}
			if fr := chunk.Choices[0].FinishReason; fr != "" {
				resp.FinishReason = fr
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	resp.Content = content.String()
	if gotUsage {
		resp.Cost = costFor(c.spec, resp.Usage)
		return resp, nil
	}
	// Provider streamed no usage block — approximate so the caller still
	// gets token/cost numbers, flagged as estimated.
	resp.Usage = Usage{
		PromptTokens:     estimateTokens(req),
		CompletionTokens: approxTokens(len(resp.Content)),
	}
	resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	resp.Cost = costFor(c.spec, resp.Usage)
	resp.Cost.Estimated = true
	return resp, nil
}
