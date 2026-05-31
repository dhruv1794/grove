package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grove/internal/core"
	"grove/internal/grove"
)

// newTestHandler builds the routed handler over a fresh workspace with one
// connected local source (not built, so the forest is empty).
func newTestHandler(t *testing.T, auth string) http.Handler {
	t.Helper()
	ctx := context.Background()
	layout := core.NewLayout(filepath.Join(t.TempDir(), "ws"))
	if err := grove.Init(ctx, layout); err != nil {
		t.Fatalf("Init: %v", err)
	}
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "a.md"), []byte("# Hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := grove.Open(ctx, grove.Options{Layout: layout})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	if _, err := g.ConnectLocal(ctx, grove.ConnectOpts{Path: docs, Name: "notes"}); err != nil {
		t.Fatalf("ConnectLocal: %v", err)
	}
	h, err := New(g, Options{Auth: auth}).handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, path string, header map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestReadEndpoints(t *testing.T) {
	h := newTestHandler(t, "")

	if code, body := get(t, h, "/api/status", nil); code != 200 || !strings.Contains(body, `"notes"`) {
		t.Errorf("/api/status = %d %s, want 200 with source notes", code, body)
	}
	if code, body := get(t, h, "/api/sources", nil); code != 200 || strings.Contains(body, "ConfigJSON") {
		t.Errorf("/api/sources = %d %s, want 200 without ConfigJSON leak", code, body)
	}
	if code, body := get(t, h, "/api/trees", nil); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("/api/trees = %d %s, want 200 [] (nothing built)", code, body)
	}
	if code, _ := get(t, h, "/api/tree/nope", nil); code != http.StatusBadRequest {
		t.Errorf("/api/tree/nope = %d, want 400 misuse", code)
	}
	if code, body := get(t, h, "/", nil); code != 200 || !strings.Contains(body, "<html") {
		t.Errorf("/ = %d, want 200 serving the SPA", code)
	}
	// SPA fallback: an unknown client-side route serves index.html, not 404.
	if code, body := get(t, h, "/some/spa/route", nil); code != 200 || !strings.Contains(body, "<html") {
		t.Errorf("/some/spa/route = %d, want 200 index.html fallback", code)
	}
}

func TestAuthGate(t *testing.T) {
	h := newTestHandler(t, "tok")

	if code, _ := get(t, h, "/api/status", nil); code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", code)
	}
	if code, _ := get(t, h, "/api/status", map[string]string{"Authorization": "Bearer tok"}); code != 200 {
		t.Errorf("bearer token: got %d, want 200", code)
	}
	if code, _ := get(t, h, "/api/status?token=tok", nil); code != 200 {
		t.Errorf("query token: got %d, want 200", code)
	}
	if code, _ := get(t, h, "/", nil); code != 200 {
		t.Errorf("SPA without token: got %d, want 200 (localhost, open)", code)
	}
}
