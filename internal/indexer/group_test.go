package indexer

import (
	"context"
	"strings"
	"sync"
	"testing"

	"grove/internal/core"
	"grove/internal/llm"
)

// groupingLLM returns a fixed clustering for the group prompt and a fixed
// title/summary for the node prompt, telling them apart by system prompt. It
// splits the documents into two halves by their presented order.
type groupingLLM struct {
	mu         sync.Mutex
	groupCalls int
	nodeCalls  int
	groupJSON  string
}

func (f *groupingLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sys := req.Messages[0].Content
	if strings.Contains(sys, "organizing a flat list of documents") {
		f.groupCalls++
		return &llm.Response{Content: f.groupJSON, Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 10}}, nil
	}
	f.nodeCalls++
	return &llm.Response{Content: `{"title": "T", "summary": "S"}`, Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5}}, nil
}

func (f *groupingLLM) Stream(ctx context.Context, req llm.Request, _ func(string) error) (*llm.Response, error) {
	return f.Complete(ctx, req)
}
func (f *groupingLLM) CountTokens(context.Context, llm.Request) (int, error) { return 0, nil }
func (f *groupingLLM) Model() llm.ModelSpec                                  { return llm.ModelSpec{Provider: "fake", Name: "model"} }

func leaf(id string) *bnode {
	return &bnode{id: nodeID("t", "doc:"+id), treeID: "t", doc: &core.Document{ID: id, Hash: "h-" + id}}
}

func TestParseGroups(t *testing.T) {
	leaves := []*bnode{leaf("a"), leaf("b"), leaf("c"), leaf("d")}

	got := parseGroups(`{"groups":[{"title":"X","members":[1,2]},{"title":"Y","members":[3,4]}]}`, leaves)
	if len(got) != 2 || got[0].Title != "X" || len(got[0].DocIDs) != 2 || got[0].DocIDs[0] != "a" {
		t.Fatalf("got %+v", got)
	}

	// Duplicate and out-of-range members are dropped (first group claims them).
	got = parseGroups(`{"groups":[{"title":"X","members":[1,9]},{"title":"Y","members":[1,2]}]}`, leaves)
	if len(got) != 2 || len(got[0].DocIDs) != 1 || got[0].DocIDs[0] != "a" {
		t.Errorf("dedupe/range: group X = %+v", got[0])
	}
	if len(got[1].DocIDs) != 1 || got[1].DocIDs[0] != "b" {
		t.Errorf("dedupe: group Y = %+v", got[1])
	}

	// Garbage or a single group → one all-docs group (signals "no split").
	got = parseGroups("not json", leaves)
	if len(got) != 1 || len(got[0].DocIDs) != 4 {
		t.Errorf("fallback = %+v, want one group of 4", got)
	}
}

func TestApplyGroups(t *testing.T) {
	parent := &bnode{id: "t:", treeID: "t", depth: 0}
	for _, id := range []string{"a", "b", "c"} {
		l := leaf(id)
		l.parentID = parent.id
		parent.children = append(parent.children, l)
	}
	// Group {a,b}; c is left unassigned and should stay directly under parent.
	formed := applyGroups(parent, []docGroup{{Title: "Pair", DocIDs: []string{"a", "b"}}})
	if formed != 1 {
		t.Fatalf("formed = %d, want 1", formed)
	}
	if len(parent.children) != 2 {
		t.Fatalf("parent children = %d, want 2 (topic + leftover c)", len(parent.children))
	}
	topic := parent.children[0]
	if topic.doc != nil || len(topic.children) != 2 || topic.name != "Pair" {
		t.Errorf("topic node = %+v", topic)
	}
	if topic.children[0].parentID != topic.id || topic.children[0].depth != topic.depth+1 {
		t.Errorf("member not reparented: %+v", topic.children[0])
	}
	if parent.children[1].doc == nil || parent.children[1].doc.ID != "c" {
		t.Errorf("leftover = %+v, want leaf c", parent.children[1])
	}
}

func TestTopicHashStable(t *testing.T) {
	a, b, c := leaf("a"), leaf("b"), leaf("c")
	if topicHash([]*bnode{a, b, c}) != topicHash([]*bnode{c, a, b}) {
		t.Error("topicHash should be order-independent")
	}
	if topicHash([]*bnode{a, b}) == topicHash([]*bnode{a, c}) {
		t.Error("different membership should hash differently")
	}
}

// TestBuildGroupsFlatFolder builds a flat source of 10 root docs and checks the
// LLM grouping inserts topic nodes, then that a rebuild reuses the cached
// grouping (no second group call) and hits the node cache.
func TestBuildGroupsFlatFolder(t *testing.T) {
	ctx := context.Background()
	s, layout := newTestStore(t)
	if err := s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}); err != nil {
		t.Fatal(err)
	}
	var docs []core.Document
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		docs = append(docs, core.Document{ID: id, Source: "notes", SourceRef: id + ".md", Title: id, Content: "content " + id, Hash: "h-" + id})
	}
	if err := s.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
	// Two groups: docs 1-5 and 6-10 (1-based, in presented order — sorted by id).
	f := &groupingLLM{groupJSON: `{"groups":[{"title":"First","members":[1,2,3,4,5]},{"title":"Second","members":[6,7,8,9,10]}]}`}
	deps := Deps{Store: s, LLM: f, Layout: layout}

	res, err := Build(ctx, deps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Groups != 2 {
		t.Errorf("Groups = %d, want 2", res.Groups)
	}
	if f.groupCalls != 1 {
		t.Errorf("group calls = %d, want 1", f.groupCalls)
	}
	// 10 leaves + 2 topic nodes + 1 root = 13 nodes.
	if res.Nodes != 13 {
		t.Errorf("Nodes = %d, want 13 (10 leaves + 2 topics + root)", res.Nodes)
	}

	nodes, err := s.ListNodesByTree(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	topics := 0
	for _, n := range nodes {
		if strings.Contains(n.ID, topicSep) {
			topics++
			if n.ParentID != "notes:" {
				t.Errorf("topic %s parent = %q, want notes:", n.ID, n.ParentID)
			}
		}
	}
	if topics != 2 {
		t.Errorf("persisted topic nodes = %d, want 2", topics)
	}

	// Rebuild: grouping is cached (no new group call) and nodes hit the cache.
	f2 := &groupingLLM{groupJSON: f.groupJSON}
	res2, err := Build(ctx, Deps{Store: s, LLM: f2, Layout: layout}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f2.groupCalls != 0 {
		t.Errorf("rebuild group calls = %d, want 0 (cached)", f2.groupCalls)
	}
	if f2.nodeCalls != 0 {
		t.Errorf("rebuild node calls = %d, want 0 (cached)", f2.nodeCalls)
	}
	if res2.CacheMiss != 0 {
		t.Errorf("rebuild cache misses = %d, want 0", res2.CacheMiss)
	}
}
