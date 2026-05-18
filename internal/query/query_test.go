package query

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/store"
)

// fakeLLM answers navigate and synthesis calls deterministically, telling
// them apart by the system prompt.
type fakeLLM struct{ navCalls, ansCalls int }

func (f *fakeLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	sys := req.Messages[0].Content
	switch {
	case strings.Contains(sys, "navigating a tree-of-contents"):
		f.navCalls++
		return &llm.Response{Content: `{"choices": [1, 2], "reason": "matched the topic"}`}, nil
	case strings.Contains(sys, "answering a question"):
		f.ansCalls++
		return &llm.Response{Content: "Auth uses JWT tokens [1]. Deploys go through CI [2]."}, nil
	}
	return &llm.Response{Content: "{}"}, nil
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

// seedTree installs a "notes" tree: a root with two leaf documents, with node
// payloads written to disk where query expects them.
func seedTree(t *testing.T, s *store.SQLite, layout core.Layout) {
	t.Helper()
	ctx := context.Background()
	mustOK(t, s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}))
	mustOK(t, s.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "a.md", Title: "Auth", Content: "Auth uses JWT tokens.", Hash: "h1"},
		{ID: "d2", Source: "notes", SourceRef: "b.md", Title: "Deploy", Content: "Deploys go through CI.", Hash: "h2"},
	}))
	mustOK(t, s.UpsertTree(ctx, core.Tree{ID: "notes", Source: "notes", Name: "notes", RootNodeID: "notes:"}))

	type nd struct{ hash, id, parent, title, summary, doc string }
	for _, n := range []nd{
		{"ch-root", "notes:", "", "All Notes", "Auth and deployment notes.", ""},
		{"ch-d1", "notes:doc:d1", "notes:", "Auth", "How authentication works.", "d1"},
		{"ch-d2", "notes:doc:d2", "notes:", "Deploy", "How deployment works.", "d2"},
	} {
		path := core.PayloadPath(layout.Trees, n.hash, "node.json")
		mustOK(t, core.WriteNodePayload(path, &core.NodePayload{
			SchemaVersion: core.NodeSchemaVersion, NodeID: n.id, Title: n.title, Summary: n.summary,
		}))
		node := core.Node{ID: n.id, TreeID: "notes", ParentID: n.parent, Title: n.title, PayloadPath: path}
		if n.doc != "" {
			node.DocIDs = []string{n.doc}
		}
		mustOK(t, s.UpsertNode(ctx, node))
	}
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestAsk(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	f := &fakeLLM{}
	res, err := Ask(ctx, Deps{Store: s, LLM: f}, "how does auth work?", Options{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(res.Answer, "[1]") {
		t.Errorf("answer missing citation marker: %q", res.Answer)
	}
	if len(res.Citations) != 2 {
		t.Fatalf("citations = %d, want 2", len(res.Citations))
	}
	if len(res.Trace.SearchPath) != 2 {
		t.Errorf("trace steps = %d, want 2", len(res.Trace.SearchPath))
	}
	// One navigate call (single tree skips selection) + one synthesis call.
	if f.navCalls != 1 || f.ansCalls != 1 {
		t.Errorf("calls = %d nav / %d ans, want 1/1", f.navCalls, f.ansCalls)
	}
	if res.Cost.Calls != 2 {
		t.Errorf("tally calls = %d, want 2", res.Cost.Calls)
	}
}

func TestAskRetrieveOnly(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	f := &fakeLLM{}
	res, err := Ask(ctx, Deps{Store: s, LLM: f}, "auth?", Options{RetrieveOnly: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if res.Answer != "" {
		t.Errorf("retrieve-only answer = %q, want empty", res.Answer)
	}
	if len(res.Citations) != 2 {
		t.Errorf("citations = %d, want 2", len(res.Citations))
	}
	if f.ansCalls != 0 {
		t.Errorf("retrieve-only made %d synthesis calls, want 0", f.ansCalls)
	}
}

func TestAskFast(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	f := &fakeLLM{}
	res, err := Ask(ctx, Deps{Store: s, LLM: f}, "JWT tokens", Options{Fast: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// FTS matches only d1 ("Auth uses JWT tokens."), no tree descent.
	if len(res.Citations) != 1 || res.Citations[0].DocID != "d1" {
		t.Fatalf("fast citations = %+v, want one to d1", res.Citations)
	}
	if len(res.Trace.SearchPath) != 0 {
		t.Errorf("fast retrieval trace = %v, want empty (no descent)", res.Trace.SearchPath)
	}
	if f.navCalls != 0 {
		t.Errorf("fast made %d navigate calls, want 0", f.navCalls)
	}
	if f.ansCalls != 1 || res.Answer == "" {
		t.Errorf("fast should still synthesize: ansCalls=%d answer=%q", f.ansCalls, res.Answer)
	}
}

func TestAskFastRetrieveOnly(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	// nil LLM: --fast --retrieve-only must make no model call at all.
	res, err := Ask(ctx, Deps{Store: s, LLM: nil}, "JWT tokens", Options{Fast: true, RetrieveOnly: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(res.Citations) != 1 || res.Citations[0].DocID != "d1" {
		t.Fatalf("fast citations = %+v, want one to d1", res.Citations)
	}
	if res.Answer != "" {
		t.Errorf("retrieve-only answer = %q, want empty", res.Answer)
	}
	if res.Cost.Calls != 0 {
		t.Errorf("fast retrieve-only tally calls = %d, want 0", res.Cost.Calls)
	}
}

func TestAskNoTrees(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	_, err := Ask(ctx, Deps{Store: s, LLM: &fakeLLM{}}, "anything?", Options{})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindNoSources {
		t.Fatalf("want no_sources error, got %v", err)
	}
}

func TestParseChoices(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want []int
	}{
		{`{"choices": [1, 3], "reason": "x"}`, 3, []int{0, 2}},
		{"```json\n{\"choices\":[2],\"reason\":\"\"}\n```", 3, []int{1}},
		{`{"choices": [9, 1, 1], "reason": ""}`, 3, []int{0}}, // out-of-range + dup dropped
		{`garbage`, 3, nil},
	}
	for _, c := range cases {
		got, _ := parseChoices(c.in, c.n)
		if !equalInts(got, c.want) {
			t.Errorf("parseChoices(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
