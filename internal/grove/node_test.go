package grove

import (
	"context"
	"path/filepath"
	"testing"

	"grove/internal/core"
)

// seedForest stands up a tiny two-level forest with one cross-link, returning an
// open Grove. Mirrors graph_test's setup. Caller defers g.Close().
func seedForest(t *testing.T) *Grove {
	t.Helper()
	ctx := context.Background()
	layout := core.NewLayout(filepath.Join(t.TempDir(), "ws"))
	if err := Init(ctx, layout); err != nil {
		t.Fatalf("Init: %v", err)
	}
	g, err := Open(ctx, Options{Layout: layout})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ck := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ck(g.store.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}))
	ck(g.store.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "auth.md", Title: "Auth", Content: "auth body", Hash: "h1"},
		{ID: "d2", Source: "notes", SourceRef: "deploy.md", Title: "Deploy", Content: "deploy body", Hash: "h2"},
	}))
	ck(g.store.UpsertTree(ctx, core.Tree{ID: "notes", Source: "notes", Name: "notes", RootNodeID: "notes:", DocCount: 2, NodeCount: 5}))
	// The root summary lives in an on-disk payload (not a DB column), so write one
	// and point the node at it — that's what summaryOf reads in production.
	rootPayload := filepath.Join(layout.Trees, "root.json")
	ck(core.WriteNodePayload(rootPayload, &core.NodePayload{NodeID: "notes:", Title: "All Notes", Summary: "everything"}))
	ck(g.store.UpsertNodes(ctx, []core.Node{
		{ID: "notes:", TreeID: "notes", Title: "All Notes", PayloadPath: rootPayload, Depth: 0},
		{ID: "notes:auth", TreeID: "notes", ParentID: "notes:", Title: "Authentication", Summary: "auth stuff", Depth: 1},
		{ID: "notes:/topic-abc", TreeID: "notes", ParentID: "notes:", Title: "Deploy Topic", Depth: 1},
		{ID: "notes:doc:d1", TreeID: "notes", ParentID: "notes:auth", Title: "auth.md", Depth: 2, DocIDs: []string{"d1"}},
		{ID: "notes:doc:d2", TreeID: "notes", ParentID: "notes:/topic-abc", Title: "deploy.md", Depth: 2, DocIDs: []string{"d2"}},
	}))
	ck(g.store.AddNodeSeeAlso(ctx, map[string][]core.NodeRef{
		"notes:doc:d1": {
			{NodeID: "notes:doc:d2", TreeID: "notes", Reason: "links to", Strength: 1.0},
			{NodeID: "notes:weaklink", TreeID: "notes", Reason: "weak", Strength: 0.3},
		},
	}))
	return g
}

func TestNavigate(t *testing.T) {
	ctx := context.Background()
	g := seedForest(t)
	defer g.Close()

	// Empty node id starts at the root and returns its two children.
	root, err := g.Navigate(ctx, "notes", "")
	if err != nil {
		t.Fatalf("Navigate root: %v", err)
	}
	if root.Current.ID != "notes:" {
		t.Fatalf("root id = %q, want notes:", root.Current.ID)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root children = %d, want 2", len(root.Children))
	}
	for _, c := range root.Children {
		if c.ID == "notes:auth" {
			if !c.HasChildren {
				t.Errorf("auth node should report has_children")
			}
			if c.DocCount != 1 {
				t.Errorf("auth doc_count = %d, want 1", c.DocCount)
			}
		}
	}

	// A leaf has no children.
	leaf, err := g.Navigate(ctx, "notes", "notes:doc:d1")
	if err != nil {
		t.Fatalf("Navigate leaf: %v", err)
	}
	if len(leaf.Children) != 0 {
		t.Fatalf("leaf children = %d, want 0", len(leaf.Children))
	}

	if _, err := g.Navigate(ctx, "nope", ""); err == nil {
		t.Error("Navigate of unknown tree should error")
	}
	if _, err := g.Navigate(ctx, "notes", "notes:ghost"); err == nil {
		t.Error("Navigate of unknown node should error")
	}
}

func TestNodeByID(t *testing.T) {
	ctx := context.Background()
	g := seedForest(t)
	defer g.Close()

	// NodeByID resolves the tree from the node id alone.
	nav, err := g.NodeByID(ctx, "notes:auth")
	if err != nil {
		t.Fatalf("NodeByID: %v", err)
	}
	if nav.Current.Title != "Authentication" || len(nav.Children) != 1 {
		t.Fatalf("got %q with %d children, want Authentication/1", nav.Current.Title, len(nav.Children))
	}
}

func TestNodeDocuments(t *testing.T) {
	ctx := context.Background()
	g := seedForest(t)
	defer g.Close()

	// A leaf returns its own document, truncated.
	leaf, err := g.NodeDocuments(ctx, "notes:doc:d1", 4)
	if err != nil {
		t.Fatalf("NodeDocuments leaf: %v", err)
	}
	if len(leaf.Documents) != 1 || leaf.Documents[0].DocID != "d1" {
		t.Fatalf("leaf docs = %+v, want [d1]", leaf.Documents)
	}
	if leaf.Documents[0].Content != "auth" { // "auth body" truncated to 4 runes
		t.Errorf("content = %q, want truncated %q", leaf.Documents[0].Content, "auth")
	}

	// A hub gathers the leaf documents beneath it.
	hub, err := g.NodeDocuments(ctx, "notes:", 0)
	if err != nil {
		t.Fatalf("NodeDocuments hub: %v", err)
	}
	if len(hub.Documents) != 2 {
		t.Fatalf("hub docs = %d, want 2 (d1+d2)", len(hub.Documents))
	}
}

func TestNodeLinks(t *testing.T) {
	ctx := context.Background()
	g := seedForest(t)
	defer g.Close()

	// Default-ish threshold keeps the strong link, drops the weak 0.3 one.
	links, err := g.NodeLinks(ctx, "notes:doc:d1", 0.5)
	if err != nil {
		t.Fatalf("NodeLinks: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("links = %d, want 1 (weak 0.3 filtered)", len(links))
	}
	if links[0].NodeID != "notes:doc:d2" || links[0].Title != "deploy.md" {
		t.Errorf("link = %+v, want d2/deploy.md", links[0])
	}

	// A lower threshold lets the weak edge through.
	all, err := g.NodeLinks(ctx, "notes:doc:d1", 0.0)
	if err != nil {
		t.Fatalf("NodeLinks all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("links = %d, want 2 at threshold 0", len(all))
	}
}

func TestForestViewSummary(t *testing.T) {
	ctx := context.Background()
	g := seedForest(t)
	defer g.Close()

	trees, err := g.ForestView(ctx, 1)
	if err != nil {
		t.Fatalf("ForestView: %v", err)
	}
	if len(trees) != 1 || trees[0].Summary != "everything" {
		t.Fatalf("summary = %q, want %q", treesSummary(trees), "everything")
	}
}

func treesSummary(trees []ForestTree) string {
	if len(trees) == 0 {
		return ""
	}
	return trees[0].Summary
}
