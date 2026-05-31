package grove

import (
	"context"
	"path/filepath"
	"testing"

	"grove/internal/core"
)

func TestGraphView(t *testing.T) {
	ctx := context.Background()
	layout := core.NewLayout(filepath.Join(t.TempDir(), "ws"))
	if err := Init(ctx, layout); err != nil {
		t.Fatalf("Init: %v", err)
	}
	g, err := Open(ctx, Options{Layout: layout})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer g.Close()

	ck := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ck(g.store.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}))
	ck(g.store.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "auth.md", Title: "Auth", Hash: "h1"},
		{ID: "d2", Source: "notes", SourceRef: "deploy.md", Title: "Deploy", Hash: "h2"},
	}))
	ck(g.store.UpsertTree(ctx, core.Tree{ID: "notes", Source: "notes", Name: "notes", RootNodeID: "notes:"}))
	// root → dir (auth) → leaf d1 ; root → topic → leaf d2
	ck(g.store.UpsertNodes(ctx, []core.Node{
		{ID: "notes:", TreeID: "notes", Title: "All Notes", Depth: 0},
		{ID: "notes:auth", TreeID: "notes", ParentID: "notes:", Title: "Authentication", Depth: 1},
		{ID: "notes:/topic-abc", TreeID: "notes", ParentID: "notes:", Title: "Deploy Topic", Depth: 1},
		{ID: "notes:doc:d1", TreeID: "notes", ParentID: "notes:auth", Title: "auth.md", Depth: 2, DocIDs: []string{"d1"}},
		{ID: "notes:doc:d2", TreeID: "notes", ParentID: "notes:/topic-abc", Title: "deploy.md", Depth: 2, DocIDs: []string{"d2"}},
	}))
	// One real cross-link and one dangling (target absent) — the latter is dropped.
	ck(g.store.AddNodeSeeAlso(ctx, map[string][]core.NodeRef{
		"notes:doc:d1": {
			{NodeID: "notes:doc:d2", Reason: "links to", Strength: 1.0},
			{NodeID: "notes:doc:ghost", Reason: "dangling", Strength: 1.0},
		},
	}))

	gv, err := g.GraphView(ctx)
	if err != nil {
		t.Fatalf("GraphView: %v", err)
	}
	if len(gv.Nodes) != 5 {
		t.Fatalf("nodes = %d, want 5", len(gv.Nodes))
	}

	kind := map[string]string{}
	size := map[string]int{}
	for _, n := range gv.Nodes {
		kind[n.ID] = n.Kind
		size[n.ID] = n.Size
	}
	for id, want := range map[string]string{
		"notes:":           "root",
		"notes:auth":       "dir",
		"notes:/topic-abc": "topic",
		"notes:doc:d1":     "leaf",
		"notes:doc:d2":     "leaf",
	} {
		if kind[id] != want {
			t.Errorf("kind[%s] = %q, want %q", id, kind[id], want)
		}
	}
	// Subtree leaf counts: root covers both leaves, each hub one, leaves are 1.
	if size["notes:"] != 2 {
		t.Errorf("root size = %d, want 2", size["notes:"])
	}
	if size["notes:auth"] != 1 || size["notes:doc:d1"] != 1 {
		t.Errorf("auth/leaf sizes = %d/%d, want 1/1", size["notes:auth"], size["notes:doc:d1"])
	}

	var child, seealso int
	for _, l := range gv.Links {
		switch l.Kind {
		case "child":
			child++
		case "seealso":
			seealso++
			if l.Source != "notes:doc:d1" || l.Target != "notes:doc:d2" || l.Strength != 1.0 {
				t.Errorf("unexpected seealso link %+v", l)
			}
		}
	}
	if child != 4 { // every non-root node has one parent edge
		t.Errorf("child links = %d, want 4", child)
	}
	if seealso != 1 { // the dangling edge is filtered out
		t.Errorf("seealso links = %d, want 1 (dangling dropped)", seealso)
	}
}
