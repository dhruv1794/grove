package llm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"grove/internal/core"
)

// httpPost sends body as a JSON POST to url with the given headers. A
// transport failure or a non-200 status becomes a structured *core.Error
// (KindModelUnreachable). On success the caller owns and must close resp.Body.
func httpPost(ctx context.Context, hc *http.Client, spec ModelSpec, url string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", spec, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, core.WrapError(core.KindModelUnreachable, err,
			fmt.Sprintf("cannot reach model %s", spec),
			"check the provider endpoint is running and reachable")
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body) // best-effort: body only feeds the error message
		resp.Body.Close()
		return nil, httpStatusError(spec, resp.StatusCode, data)
	}
	return resp, nil
}

// scanSSE reads a Server-Sent Events response, calling onData with each
// non-empty `data:` payload. onData returns stop=true to end early (the
// OpenAI "[DONE]" sentinel). A scanner failure is wrapped as
// KindModelUnreachable.
func scanSSE(spec ModelSpec, body io.Reader, onData func(payload string) (stop bool, err error)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" {
			continue
		}
		stop, err := onData(payload)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return core.WrapError(core.KindModelUnreachable, err,
			fmt.Sprintf("model %s stream interrupted", spec),
			"check the provider is reachable and retry")
	}
	return nil
}
