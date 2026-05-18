package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/store"
)

// fakeLLM is a deterministic stand-in: it counts calls and returns a fixed
// title/summary JSON, so tests exercise the indexer without a real model.
type fakeLLM struct{ calls int }

func (f *fakeLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	f.calls++
	return &llm.Response{
		Content: `{"title": "Generated Title", "summary": "Generated summary."}`,
		Model:   "fake/model",
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func (f *fakeLLM) Stream(ctx context.Context, req llm.Request, _ func(string) error) (*llm.Response, error) {
	return f.Complete(ctx, req)
}

func (f *fakeLLM) CountTokens(context.Context, llm.Request) (int, error) { return 0, nil }

func (f *fakeLLM) Model() llm.ModelSpec { return llm.ModelSpec{Provider: "fake", Name: "model"} }

func newTestStore(t *testing.T) (*store.SQLite, core.Layout) {
	t.Helper()
	layout := core.NewLayout(filepath.Join(t.TempDir(), "ws"))
	for _, d := range []string{layout.Root, layout.Trees, layout.Docs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := store.New(layout)
	ctx := context.Background()
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, layout
}

// seedNotes installs a "notes" source with two documents: one at the root,
// one in a "sub/" directory — yielding root + sub + 2 leaves = 4 nodes.
func seedNotes(t *testing.T, s *store.SQLite) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}); err != nil {
		t.Fatal(err)
	}
	docs := []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "a.md", Title: "A", Content: "alpha content", Hash: "hash-a"},
		{ID: "d2", Source: "notes", SourceRef: "sub/b.md", Title: "B", Content: "beta content", Hash: "hash-b", Hierarchy: []string{"sub"}},
	}
	if err := s.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAndCache(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedNotes(t, s)

	f := &fakeLLM{}
	deps := Deps{Store: s, LLM: f, Layout: layout}

	res, err := Build(ctx, deps, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Trees != 1 || res.Nodes != 4 {
		t.Fatalf("res = %+v, want 1 tree / 4 nodes", res)
	}
	if res.CacheHits != 0 || res.CacheMiss != 4 || f.calls != 4 {
		t.Fatalf("first build: hits=%d miss=%d calls=%d, want 0/4/4", res.CacheHits, res.CacheMiss, f.calls)
	}

	nodes, err := s.ListNodesByTree(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("persisted nodes = %d, want 4", len(nodes))
	}

	// Unchanged rebuild: every node served from cache, no model calls.
	res2, err := Build(ctx, deps, Options{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res2.CacheHits != 4 || res2.CacheMiss != 0 || f.calls != 4 {
		t.Fatalf("rebuild: hits=%d miss=%d calls=%d, want 4/0/4", res2.CacheHits, res2.CacheMiss, f.calls)
	}

	// --rebuild forces fresh model calls for every node.
	res3, err := Build(ctx, deps, Options{Rebuild: true})
	if err != nil {
		t.Fatalf("forced rebuild: %v", err)
	}
	if res3.CacheMiss != 4 || f.calls != 8 {
		t.Fatalf("forced rebuild: miss=%d calls=%d, want 4/8", res3.CacheMiss, f.calls)
	}
}

func TestBuildDryRun(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedNotes(t, s)

	f := &fakeLLM{}
	res, err := Build(ctx, Deps{Store: s, LLM: f, Layout: layout}, Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Build: %v", err)
	}
	if f.calls != 0 {
		t.Errorf("dry run made %d model calls, want 0", f.calls)
	}
	if res.Nodes != 4 || res.CacheMiss != 4 {
		t.Errorf("dry-run res = %+v, want 4 nodes / 4 miss", res)
	}
	nodes, err := s.ListNodesByTree(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Errorf("dry run persisted %d nodes, want 0", len(nodes))
	}
}

func TestBuildNoSources(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	_, err := Build(ctx, Deps{Store: s, LLM: &fakeLLM{}, Layout: layout}, Options{})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindNoSources {
		t.Fatalf("want no_sources error, got %v", err)
	}
}

func TestParseSummary(t *testing.T) {
	cases := []struct{ in, title, summary string }{
		{`{"title": "T", "summary": "S"}`, "T", "S"},
		{"```json\n{\"title\":\"T\",\"summary\":\"S\"}\n```", "T", "S"},
		{`here you go: {"title":"T","summary":"S"} done`, "T", "S"},
		{`not json at all`, "", "not json at all"},
	}
	for _, c := range cases {
		title, summary := parseSummary(c.in)
		if title != c.title || summary != c.summary {
			t.Errorf("parseSummary(%q) = %q/%q, want %q/%q", c.in, title, summary, c.title, c.summary)
		}
	}
}
