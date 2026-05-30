package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"grove/internal/core"
)

// Embedder produces dense vector embeddings via an OpenAI-compatible
// /embeddings endpoint (Ollama, OpenAI, …). It backs grove's semantic
// retriever — a small embedding model (e.g. nomic-embed-text) does the
// meaning-based matching that keyword search misses, with negligible GPU cost.
type Embedder struct {
	spec    ModelSpec
	baseURL string
	apiKey  string
	hc      *http.Client
}

// NewEmbedder builds an Embedder for an embedding model spec (provider/name),
// resolving the endpoint the same way New does. No network call is made here.
func NewEmbedder(spec ModelSpec, opts Options) (*Embedder, error) {
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	base, known := openAIBaseURLs[spec.Provider]
	if !known {
		return nil, core.NewError(core.KindMisuse,
			fmt.Sprintf("unknown embedding provider %q", spec.Provider),
			"use an OpenAI-compatible provider, e.g. ollama/nomic-embed-text")
	}
	if spec.Provider == "ollama" && opts.OllamaHost != "" {
		base = strings.TrimRight(opts.OllamaHost, "/") + "/v1"
	}
	if opts.BaseURL != "" {
		base = strings.TrimRight(opts.BaseURL, "/")
	}
	key := opts.APIKey
	if spec.Provider == "openai" && key == "" {
		key = opts.OpenAIKey
	}
	return &Embedder{spec: spec, baseURL: base, apiKey: key, hc: hc}, nil
}

func (e *Embedder) Model() ModelSpec { return e.spec }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per input string, in order.
func (e *Embedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: e.spec.Name, Input: inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		return nil, core.NewError(core.KindModelUnreachable,
			fmt.Sprintf("embedding model %s unreachable: %v", e.spec, err),
			"check the model is pulled (ollama pull) and the host is reachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, httpStatusError(e.spec, resp.StatusCode, raw)
	}
	var er embedResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("decode embeddings response: %w", err)
	}
	if len(er.Data) != len(inputs) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(er.Data), len(inputs))
	}
	out := make([][]float32, len(er.Data))
	for i, d := range er.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
