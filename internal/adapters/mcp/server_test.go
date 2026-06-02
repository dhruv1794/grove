package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"grove/internal/core"
	"grove/internal/grove"
	"grove/internal/store"
)

// connect stands up a seeded forest, an MCP server over it, and an in-memory
// client session. It exercises the full tool/resource/prompt path without an
// LLM (every tool here is model-free; grove_search is covered live, not in CI).
func connect(t *testing.T) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	layout := core.NewLayout(filepath.Join(t.TempDir(), "ws"))
	if err := grove.Init(ctx, layout); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seed(t, layout)

	g, err := grove.Open(ctx, grove.Options{Layout: layout})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { g.Close() })

	srv := New(g, "test").srv
	ct, st := mcpsdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// seed populates a minimal two-doc forest with one cross-link, directly through
// the store (no LLM build needed).
func seed(t *testing.T, layout core.Layout) {
	t.Helper()
	ctx := context.Background()
	s := store.New(layout)
	if err := s.Open(ctx); err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ck := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ck(s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}))
	ck(s.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "auth.md", Title: "Auth", Content: "auth body", Hash: "h1"},
		{ID: "d2", Source: "notes", SourceRef: "deploy.md", Title: "Deploy", Content: "deploy body", Hash: "h2"},
	}))
	ck(s.UpsertTree(ctx, core.Tree{ID: "notes", Source: "notes", Name: "notes", RootNodeID: "notes:", DocCount: 2, NodeCount: 4}))
	ck(s.UpsertNodes(ctx, []core.Node{
		{ID: "notes:", TreeID: "notes", Title: "All Notes", Depth: 0},
		{ID: "notes:auth", TreeID: "notes", ParentID: "notes:", Title: "Authentication", Depth: 1},
		{ID: "notes:doc:d1", TreeID: "notes", ParentID: "notes:auth", Title: "auth.md", Depth: 2, DocIDs: []string{"d1"}},
		{ID: "notes:doc:d2", TreeID: "notes", ParentID: "notes:", Title: "deploy.md", Depth: 1, DocIDs: []string{"d2"}},
	}))
	ck(s.AddNodeSeeAlso(ctx, map[string][]core.NodeRef{
		"notes:doc:d1": {{NodeID: "notes:doc:d2", TreeID: "notes", Reason: "links to", Strength: 1.0}},
	}))
}

// callJSON calls a tool and decodes its structured content into v, failing on a
// tool-level error.
func callJSON(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any, v any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s returned tool error: %s", name, toolText(res))
	}
	if v == nil {
		return
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal %s structured: %v", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s into %T: %v", name, v, err)
	}
}

func toolText(res *mcpsdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestTools(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 5 {
		t.Fatalf("tools = %d, want 5", len(tools.Tools))
	}

	var lt listTreesOut
	callJSON(t, cs, "grove_list_trees", nil, &lt)
	if len(lt.Trees) != 1 || lt.Trees[0].ID != "notes" || lt.Trees[0].DocCount != 2 {
		t.Fatalf("list_trees = %+v", lt.Trees)
	}

	var nav grove.NodeNav
	callJSON(t, cs, "grove_navigate", map[string]any{"tree_id": "notes"}, &nav)
	if nav.Current.ID != "notes:" || len(nav.Children) != 2 {
		t.Fatalf("navigate root = %+v", nav)
	}

	var rd grove.NodeRead
	callJSON(t, cs, "grove_read", map[string]any{"node_id": "notes:doc:d1"}, &rd)
	if len(rd.Documents) != 1 || rd.Documents[0].Content != "auth body" {
		t.Fatalf("read = %+v", rd.Documents)
	}

	var lk linksOut
	callJSON(t, cs, "grove_links", map[string]any{"node_id": "notes:doc:d1"}, &lk)
	if len(lk.Links) != 1 || lk.Links[0].NodeID != "notes:doc:d2" {
		t.Fatalf("links = %+v", lk.Links)
	}
}

func TestToolErrorIsStructured(t *testing.T) {
	cs := connect(t)
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "grove_navigate", Arguments: map[string]any{"tree_id": "ghost"},
	})
	if err != nil {
		t.Fatalf("CallTool transport error (want tool-level error): %v", err)
	}
	if !res.IsError {
		t.Fatal("navigate of unknown tree should be a tool error")
	}
	if !strings.Contains(toolText(res), "misuse") {
		t.Errorf("error text missing kind: %q", toolText(res))
	}
}

func TestResources(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()

	for _, uri := range []string{"grove://forest", "grove://tree/notes", "grove://node/notes:auth", "grove://doc/d1"} {
		res, err := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("ReadResource %s: %v", uri, err)
		}
		if len(res.Contents) != 1 || res.Contents[0].Text == "" {
			t.Fatalf("ReadResource %s: empty contents", uri)
		}
	}

	// The doc resource carries the document content.
	res, _ := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "grove://doc/d1"})
	if !strings.Contains(res.Contents[0].Text, "auth body") {
		t.Errorf("doc resource missing content: %s", res.Contents[0].Text)
	}
}

func TestPrompts(t *testing.T) {
	cs := connect(t)
	ctx := context.Background()

	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(prompts.Prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(prompts.Prompts))
	}

	got, err := cs.GetPrompt(ctx, &mcpsdk.GetPromptParams{
		Name:      "grove_compare_sources",
		Arguments: map[string]string{"source_a": "a", "source_b": "b", "topic": "auth"},
	})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(got.Messages))
	}
	tc, ok := got.Messages[0].Content.(*mcpsdk.TextContent)
	if !ok || !strings.Contains(tc.Text, "auth") {
		t.Fatalf("prompt text missing topic: %+v", got.Messages[0].Content)
	}
}
