package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/store"
)

// fakeLLM is a deterministic stand-in: it counts calls and returns a fixed
// title/summary JSON, so tests exercise the indexer without a real model. The
// mutex makes the call counter safe under concurrent builds; tests read it
// after Build returns (all workers joined), so the read needs no lock.
type fakeLLM struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
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
	if hist, _ := s.ListBuildHistory(ctx, 0); len(hist) != 0 {
		t.Errorf("dry run wrote %d history rows, want 0", len(hist))
	}
}

// TestBuildConcurrent exercises the parallel node-processing path (run with
// -race to catch data races on the shared Result). A flat corpus of N docs
// gives N independent leaves under the root, so they process concurrently.
func TestBuildConcurrent(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	if err := s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}); err != nil {
		t.Fatal(err)
	}
	const n = 12
	docs := make([]core.Document, n)
	for i := range docs {
		docs[i] = core.Document{
			ID: fmt.Sprintf("d%d", i), Source: "notes",
			SourceRef: fmt.Sprintf("note-%d.md", i),
			Title:     fmt.Sprintf("Note %d", i),
			Content:   fmt.Sprintf("body of note %d", i),
			Hash:      fmt.Sprintf("hash-%d", i),
		}
	}
	if err := s.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}

	f := &fakeLLM{}
	deps := Deps{Store: s, LLM: f, Layout: layout}
	res, err := Build(ctx, deps, Options{Concurrency: 4})
	if err != nil {
		t.Fatalf("concurrent Build: %v", err)
	}
	// root + 12 leaves = 13 nodes, all freshly generated.
	if res.Nodes != n+1 || res.CacheMiss != n+1 || res.CacheHits != 0 {
		t.Fatalf("res = %+v, want %d nodes / %d miss / 0 hits", res, n+1, n+1)
	}
	if f.calls != n+1 {
		t.Errorf("model calls = %d, want %d", f.calls, n+1)
	}
	if got, _ := s.ListNodesByTree(ctx, "notes"); len(got) != n+1 {
		t.Errorf("persisted nodes = %d, want %d", len(got), n+1)
	}

	// Concurrent rebuild is fully cached: no model calls.
	res2, err := Build(ctx, deps, Options{Concurrency: 4})
	if err != nil {
		t.Fatalf("concurrent rebuild: %v", err)
	}
	if res2.CacheHits != n+1 || res2.CacheMiss != 0 || f.calls != n+1 {
		t.Errorf("rebuild: hits=%d miss=%d calls=%d, want %d/0/%d", res2.CacheHits, res2.CacheMiss, f.calls, n+1, n+1)
	}
}

// Two docs with identical content hash to the same node payload path. Under
// concurrency both leaves write that path; the atomic write in WriteNodePayload
// must leave a single valid JSON payload (no torn file).
func TestBuildConcurrentSharedPayload(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	if err := s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "a.md", Title: "A", Content: "same body", Hash: "dup"},
		{ID: "d2", Source: "notes", SourceRef: "b.md", Title: "B", Content: "same body", Hash: "dup"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(ctx, Deps{Store: s, LLM: &fakeLLM{}, Layout: layout}, Options{Concurrency: 4}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Every persisted node's payload must read back as valid JSON — the two
	// same-hash leaves share one path, written concurrently.
	nodes, err := s.ListNodesByTree(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if _, err := core.ReadNodePayload(n.PayloadPath); err != nil {
			t.Errorf("payload for node %s not valid after concurrent build: %v", n.ID, err)
		}
	}
}

func TestBuildProgress(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedNotes(t, s) // source "notes": root + sub + 2 leaves = 4 nodes
	if err := s.UpsertSource(ctx, core.Source{Name: "other", Type: core.SourceLocal}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDocuments(ctx, []core.Document{
		{ID: "o1", Source: "other", SourceRef: "x.md", Title: "X", Content: "c", Hash: "hx"},
	}); err != nil { // source "other": root + 1 leaf = 2 nodes
		t.Fatal(err)
	}

	var events []Progress
	_, err := Build(ctx, Deps{Store: s, LLM: &fakeLLM{}, Layout: layout}, Options{
		OnProgress: func(p Progress) { events = append(events, p) },
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	bySource := map[string][]Progress{}
	for _, e := range events {
		bySource[e.Source] = append(bySource[e.Source], e)
	}
	// Each source emits a start (Done=0) plus one tick per node, ending at Total.
	for src, total := range map[string]int{"notes": 4, "other": 2} {
		evs := bySource[src]
		if len(evs) != total+1 {
			t.Errorf("source %s: %d events, want %d", src, len(evs), total+1)
			continue
		}
		if evs[0].Done != 0 || evs[0].Total != total {
			t.Errorf("source %s: first event %+v, want Done=0 Total=%d", src, evs[0], total)
		}
		for i, e := range evs {
			if e.Total != total {
				t.Errorf("source %s event %d: Total=%d, want %d", src, i, e.Total, total)
			}
			if e.Done != i { // 0,1,2,...,total
				t.Errorf("source %s event %d: Done=%d, want %d (monotonic)", src, i, e.Done, i)
			}
		}
	}
}

func TestBuildRecordsHistory(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedNotes(t, s)

	if _, err := Build(ctx, Deps{Store: s, LLM: &fakeLLM{}, Layout: layout}, Options{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	hist, err := s.ListBuildHistory(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history rows = %d, want 1", len(hist))
	}
	h := hist[0]
	if h.Model != "fake/model" || h.DocsProcessed != 2 || h.NodesCreated != 4 || h.Status != "ok" {
		t.Errorf("history row = %+v, want model=fake/model docs=2 nodes=4 status=ok", h)
	}
	if h.FinishedAt.Before(h.StartedAt) {
		t.Errorf("finished_at %v before started_at %v", h.FinishedAt, h.StartedAt)
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

func TestCrossLink(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)

	for _, name := range []string{"notes", "other"} {
		if err := s.UpsertSource(ctx, core.Source{Name: name, Type: core.SourceLocal}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "a.md", Title: "A", Hash: "ha", Content: "x", Links: []core.DocLink{
			{Type: core.LinkWiki, Target: "B"},        // resolves to d2 by title (intra-tree)
			{Type: core.LinkWiki, Target: "B"},        // duplicate → one edge
			{Type: core.LinkURL, Target: "https://x"}, // external → skipped
		}},
		{ID: "d2", Source: "notes", SourceRef: "b.md", Title: "B", Hash: "hb", Content: "y", Links: []core.DocLink{
			{Type: core.LinkDocRef, Target: "other.md"}, // resolves to d3 by source_ref (cross-tree)
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDocuments(ctx, []core.Document{
		{ID: "d3", Source: "other", SourceRef: "other.md", Title: "Other", Hash: "hc", Content: "z"},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Build(ctx, Deps{Store: s, LLM: &fakeLLM{}, Layout: layout}, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.CrossLinks != 2 {
		t.Fatalf("CrossLinks = %d, want 2 (d1→d2 deduped, d2→d3)", res.CrossLinks)
	}

	seeAlso := func(treeID, leafID string) []core.NodeRef {
		nodes, err := s.ListNodesByTree(ctx, treeID)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			if n.ID == leafID {
				return n.SeeAlso
			}
		}
		t.Fatalf("leaf %s not found in tree %s", leafID, treeID)
		return nil
	}
	if e := seeAlso("notes", "notes:doc:d1"); len(e) != 1 || e[0].NodeID != "notes:doc:d2" {
		t.Errorf("d1 edges = %+v, want one intra-tree edge to notes:doc:d2", e)
	}
	if e := seeAlso("notes", "notes:doc:d2"); len(e) != 1 || e[0].NodeID != "other:doc:d3" {
		t.Errorf("d2 edges = %+v, want one cross-tree edge to other:doc:d3", e)
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
