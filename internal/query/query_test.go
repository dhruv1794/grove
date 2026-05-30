package query

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/store"
)

// fakeLLM answers navigate and synthesis calls deterministically, telling
// them apart by the system prompt.
type fakeLLM struct {
	mu                                                          sync.Mutex // judge calls run in parallel
	navCalls, ansCalls, pruneCalls, decomposeCalls, rerankCalls int
}

func (f *fakeLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sys := req.Messages[0].Content
	switch {
	case strings.Contains(sys, "navigating a tree-of-contents"):
		f.navCalls++
		return &llm.Response{Content: `{"choices": [1, 2], "reason": "matched the topic"}`}, nil
	case strings.Contains(sys, "answering a question"):
		f.ansCalls++
		return &llm.Response{Content: "Auth uses JWT tokens [1]. Deploys go through CI [2]."}, nil
	case strings.Contains(sys, "relevance filter"):
		// Keep the auth doc (a.md), reject the deploy doc.
		f.pruneCalls++
		if strings.Contains(req.Messages[1].Content, "a.md") {
			return &llm.Response{Content: "YES"}, nil
		}
		return &llm.Response{Content: "NO"}, nil
	case strings.Contains(sys, "information needs"):
		// Split into two aspect sub-queries.
		f.decomposeCalls++
		return &llm.Response{Content: "JWT tokens\nCI deploys"}, nil
	case strings.Contains(sys, "score how well"):
		// Score the deploy doc (b.md) above the auth doc to force a reorder.
		f.rerankCalls++
		if strings.Contains(req.Messages[1].Content, "b.md") {
			return &llm.Response{Content: "9"}, nil
		}
		return &llm.Response{Content: "2"}, nil
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

func TestAskPrune(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	// Descent reaches both leaves (d1, d2); the binary filter keeps only the
	// auth doc (a.md) and rejects the deploy doc (b.md).
	f := &fakeLLM{}
	res, err := Ask(ctx, Deps{Store: s, LLM: f}, "how does auth work?", Options{Prune: true, RetrieveOnly: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if f.pruneCalls < 2 {
		t.Errorf("prune calls = %d, want >= 2 (one per candidate)", f.pruneCalls)
	}
	for _, c := range res.Citations {
		if c.DocID == "d2" {
			t.Errorf("pruner kept d2 (deploy), should have rejected it: %+v", res.Citations)
		}
	}
	if len(res.Citations) != 1 || res.Citations[0].DocID != "d1" {
		t.Fatalf("pruned citations = %+v, want only d1", res.Citations)
	}
}

func TestPruneAllRejectedFallback(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustOK(t, s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}))
	mustOK(t, s.UpsertDocuments(ctx, []core.Document{
		{ID: "d2", Source: "notes", SourceRef: "b.md", Title: "Deploy", Content: "Deploys go through CI.", Hash: "h2"},
	}))

	// fakeLLM rejects b.md — every candidate is a NO; prune must not strand the
	// query, it returns the unpruned candidates instead.
	q := &querier{deps: Deps{Store: s, LLM: &fakeLLM{}}, opts: Options{Prune: true}, query: "auth?"}
	kept, err := q.prune(ctx, []string{"d2"})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(kept) != 1 || kept[0] != "d2" {
		t.Fatalf("all-rejected fallback = %v, want [d2]", kept)
	}
}

func TestAskDecompose(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	// The blended query "JWT tokens" alone FTS-matches only d1. Decomposing into
	// {"JWT tokens", "CI deploys"} should also surface d2 (Deploys go through CI),
	// so both docs come back. Fast retrieve-only keeps it to the split call only.
	f := &fakeLLM{}
	res, err := Ask(ctx, Deps{Store: s, LLM: f}, "JWT tokens", Options{Fast: true, RetrieveOnly: true, Decompose: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if f.decomposeCalls != 1 {
		t.Errorf("decompose calls = %d, want 1", f.decomposeCalls)
	}
	got := map[string]bool{}
	for _, c := range res.Citations {
		got[c.DocID] = true
	}
	if !got["d1"] || !got["d2"] {
		t.Errorf("decompose citations = %+v, want both d1 and d2", res.Citations)
	}
}

func TestAskRerank(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	// Descent reaches d1 then d2 (fused order d1, d2). The reranker scores the
	// deploy doc (b.md=d2) above the auth doc, so the result must reorder to
	// d2, d1.
	f := &fakeLLM{}
	res, err := Ask(ctx, Deps{Store: s, LLM: f}, "how does auth work?", Options{Rerank: true, RetrieveOnly: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if f.rerankCalls < 2 {
		t.Errorf("rerank calls = %d, want >= 2 (one per candidate)", f.rerankCalls)
	}
	if len(res.Citations) < 2 || res.Citations[0].DocID != "d2" {
		t.Fatalf("rerank citations = %+v, want d2 first", res.Citations)
	}
}

func TestUseRetriever(t *testing.T) {
	all := &querier{opts: Options{}} // empty = all enabled
	for _, name := range []string{"fts", "embed", "tree"} {
		if !all.useRetriever(name) {
			t.Errorf("empty Retrievers should enable %q", name)
		}
	}
	embedOnly := &querier{opts: Options{Retrievers: []string{"embed"}}}
	if !embedOnly.useRetriever("embed") {
		t.Error("embed should be enabled")
	}
	if embedOnly.useRetriever("fts") || embedOnly.useRetriever("tree") {
		t.Error("only embed should be enabled when Retrievers=[embed]")
	}
}

func TestAskEmbedOnlyNoFTS(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout)

	// "JWT tokens" FTS-matches d1; with no embedder and embed-only retrievers,
	// FTS must be skipped, so nothing is retrieved (the vanilla-RAG isolation).
	res, err := Ask(ctx, Deps{Store: s, LLM: nil}, "JWT tokens",
		Options{Fast: true, RetrieveOnly: true, Retrievers: []string{"embed"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(res.Citations) != 0 {
		t.Errorf("embed-only with no embedder should retrieve nothing, got %+v", res.Citations)
	}
}

func TestParseScore(t *testing.T) {
	cases := map[string]int{"9": 9, "10": 10, "0": 0, " 7 ": 7, "12": 10, "8/10": 8, "yes": 0, "": 0}
	for in, want := range cases {
		if got := parseScore(in); got != want {
			t.Errorf("parseScore(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCandidateWindow(t *testing.T) {
	cases := []struct {
		name   string
		window int
		maxDoc int
		want   int
	}{
		{"defaults", 0, 0, defaultCandidateWindow},
		{"explicit window", 50, 0, 50},
		{"top-k raises floor above default", 0, 50, 50},
		{"top-k raises floor above explicit window", 30, 50, 50},
		{"explicit window above top-k wins", 40, 12, 40},
	}
	for _, tc := range cases {
		q := &querier{opts: Options{CandidateWindow: tc.window, MaxDocs: tc.maxDoc}}
		if got := q.candidateWindow(); got != tc.want {
			t.Errorf("%s: candidateWindow() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestParseSubQueries(t *testing.T) {
	got := parseSubQueries("1. restrict network traffic\n- limit secret access\n\noriginal q\nrestrict network traffic", "original q")
	want := []string{"restrict network traffic", "limit secret access", "restrict network traffic"}
	if len(got) != len(want) {
		t.Fatalf("parseSubQueries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sub[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// A line equal to the original is dropped.
	if only := parseSubQueries("original q", "original q"); len(only) != 0 {
		t.Errorf("echo-only decompose = %v, want empty", only)
	}
}

func TestParseJudgment(t *testing.T) {
	cases := map[string]bool{"YES": true, "yes": true, " Yes.": true, "y": true, "NO": false, "no": false, "": false, "maybe": false}
	for in, want := range cases {
		if got := parseJudgment(in); got != want {
			t.Errorf("parseJudgment(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAskCrossLinkExpansion(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	seedTree(t, s, layout) // tree "notes": leaves d1, d2

	// A second tree "other" with a leaf doc d3, plus a strong cross-tree
	// SeeAlso edge from notes:doc:d1 into it. A weak edge to a same-tree node
	// is also added to confirm it is not expanded.
	mustOK(t, s.UpsertSource(ctx, core.Source{Name: "other", Type: core.SourceLocal}))
	mustOK(t, s.UpsertDocuments(ctx, []core.Document{
		{ID: "d3", Source: "other", SourceRef: "c.md", Title: "Graph", Content: "Linked concept.", Hash: "h3"},
	}))
	mustOK(t, s.UpsertTree(ctx, core.Tree{ID: "other", Source: "other", Name: "other", RootNodeID: "other:"}))
	mustOK(t, s.UpsertNode(ctx, core.Node{ID: "other:", TreeID: "other", Title: "Other"}))
	mustOK(t, s.UpsertNode(ctx, core.Node{ID: "other:doc:d3", TreeID: "other", ParentID: "other:", Title: "Graph", DocIDs: []string{"d3"}}))
	mustOK(t, s.AddNodeSeeAlso(ctx, map[string][]core.NodeRef{
		"notes:doc:d1": {
			{NodeID: "other:doc:d3", Reason: "Graph", Strength: 1.0}, // strong, cross-tree → expand
			{NodeID: "notes:doc:d2", Reason: "weak", Strength: 0.3},  // below threshold → skip
		},
	}))

	// Restrict to "notes" so the descent never visits "other"; the only way d3
	// surfaces is via cross-link expansion.
	res, err := Ask(ctx, Deps{Store: s, LLM: &fakeLLM{}}, "auth?", Options{Source: "notes"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	var d3 *Citation
	for i := range res.Citations {
		if res.Citations[i].DocID == "d3" {
			d3 = &res.Citations[i]
		}
	}
	if d3 == nil {
		t.Fatalf("d3 not surfaced via cross-link; citations = %+v", res.Citations)
	}
	if !d3.CrossLink {
		t.Errorf("d3 citation CrossLink = false, want true")
	}
	// d1 and d2 came from descent, not cross-link.
	for _, c := range res.Citations {
		if (c.DocID == "d1" || c.DocID == "d2") && c.CrossLink {
			t.Errorf("%s marked CrossLink, want false", c.DocID)
		}
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

func TestShouldAbstain(t *testing.T) {
	cases := []struct {
		name    string
		correct bool
		scores  []int
		want    bool
	}{
		{"disabled", false, []int{1, 1, 1}, false},
		{"no grading ran", true, nil, false},
		{"strong single match answers", true, []int{8, 2, 1}, false},
		{"all weak abstains", true, []int{2, 1, 1}, true},
		{"multi-doc quorum of moderate answers", true, []int{4, 4, 4}, false}, // the h03 fix
		{"one moderate doc is not a quorum", true, []int{4, 2, 1}, true},
		{"all calls errored", true, []int{-1, -1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &querier{opts: Options{Correct: tc.correct}, graderScores: tc.scores}
			if got := q.shouldAbstain(); got != tc.want {
				t.Errorf("shouldAbstain(scores=%v) = %v, want %v", tc.scores, got, tc.want)
			}
		})
	}
}
