package grove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"grove/internal/core"
)

// writeFile creates a file with content, making parent dirs as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenNoWorkspace(t *testing.T) {
	layout := core.NewLayout(filepath.Join(t.TempDir(), "missing"))
	_, err := Open(context.Background(), Options{Layout: layout})
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindNoWorkspace {
		t.Fatalf("want no_workspace error, got %v", err)
	}
}

func TestConnectStatusDisconnect(t *testing.T) {
	ctx := context.Background()
	layout := core.NewLayout(filepath.Join(t.TempDir(), "ws"))
	if err := Init(ctx, layout); err != nil {
		t.Fatalf("Init: %v", err)
	}

	docs := t.TempDir()
	writeFile(t, filepath.Join(docs, "a.md"), "# Hello\nworld\n")
	writeFile(t, filepath.Join(docs, "sub", "b.md"), "# Sub\nmore\n")

	g, err := Open(ctx, Options{Layout: layout})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer g.Close()

	res, err := g.ConnectLocal(ctx, ConnectOpts{Path: docs, Name: "notes"})
	if err != nil {
		t.Fatalf("ConnectLocal: %v", err)
	}
	if res.Name != "notes" || res.DocCount != 2 {
		t.Fatalf("connect result = %+v, want name=notes docs=2", res)
	}

	rep, err := g.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Sources) != 1 || rep.Sources[0].Name != "notes" || rep.Sources[0].DocCount != 2 {
		t.Fatalf("status sources = %+v, want one source 'notes' with 2 docs", rep.Sources)
	}
	if rep.Sources[0].LastSyncAt.IsZero() {
		t.Error("expected LastSyncAt to be set after connect")
	}

	srcs, err := g.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("ListSources = %d, want 1", len(srcs))
	}

	dr, err := g.Disconnect(ctx, "notes")
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if dr.DocsRemoved != 2 {
		t.Fatalf("DocsRemoved = %d, want 2", dr.DocsRemoved)
	}

	if _, err := g.Disconnect(ctx, "notes"); err == nil {
		t.Error("expected error disconnecting an unknown source")
	}

	rep, err = g.Status(ctx)
	if err != nil {
		t.Fatalf("Status after disconnect: %v", err)
	}
	if len(rep.Sources) != 0 {
		t.Fatalf("status sources after disconnect = %d, want 0", len(rep.Sources))
	}
}

func TestTree(t *testing.T) {
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
	// Seed a 3-level tree directly (build needs an LLM): root → collection → leaf.
	ck(g.store.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal}))
	ck(g.store.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "auth.md", Title: "Auth", Hash: "h1"},
		{ID: "d2", Source: "notes", SourceRef: "deploy.md", Title: "Deploy", Hash: "h2"},
	}))
	ck(g.store.UpsertTree(ctx, core.Tree{ID: "notes", Source: "notes", Name: "notes", RootNodeID: "notes:"}))
	ck(g.store.UpsertNodes(ctx, []core.Node{
		{ID: "notes:", TreeID: "notes", Title: "All Notes", Depth: 0},
		{ID: "notes:auth", TreeID: "notes", ParentID: "notes:", Title: "Authentication", Depth: 1},
		{ID: "notes:deploy", TreeID: "notes", ParentID: "notes:", Title: "Deployment", Depth: 1},
		{ID: "notes:doc:d1", TreeID: "notes", ParentID: "notes:auth", Title: "auth.md", Depth: 2, DocIDs: []string{"d1"}},
		{ID: "notes:doc:d2", TreeID: "notes", ParentID: "notes:deploy", Title: "deploy.md", Depth: 2, DocIDs: []string{"d2"}},
	}))

	// Resolve by source path; full depth.
	views, err := g.Tree(ctx, "auth.md", 0)
	if err != nil {
		t.Fatalf("Tree(auth.md): %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	v := views[0]
	if v.DocID != "d1" || v.Tree != "notes" || v.Root.Title != "All Notes" {
		t.Fatalf("view = %+v", v)
	}
	auth := childByID(v.Root, "notes:auth")
	if auth == nil || len(auth.Children) != 1 {
		t.Fatalf("auth node = %+v, want one child", auth)
	}
	if leaf := auth.Children[0]; !leaf.IsTarget {
		t.Errorf("d1 leaf not marked as target: %+v", leaf)
	}
	if dep := childByID(v.Root, "notes:deploy"); dep == nil || dep.Children[0].IsTarget {
		t.Errorf("d2 leaf wrongly marked as target")
	}

	// Resolve by document ID.
	if vs, err := g.Tree(ctx, "d1", 0); err != nil || len(vs) != 1 || vs[0].DocID != "d1" {
		t.Errorf("Tree(d1) = %v, %v", vs, err)
	}

	// --depth 1 caps the render at the collection level — leaves are dropped.
	shallow, err := g.Tree(ctx, "auth.md", 1)
	if err != nil {
		t.Fatal(err)
	}
	if a := childByID(shallow[0].Root, "notes:auth"); a == nil || len(a.Children) != 0 {
		t.Errorf("depth=1 should drop leaves, got %+v", a)
	}

	// Unknown reference → misuse error.
	_, err = g.Tree(ctx, "nope.md", 0)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindMisuse {
		t.Errorf("Tree(nope.md): want misuse error, got %v", err)
	}
}

func childByID(n *TreeNode, id string) *TreeNode {
	for _, c := range n.Children {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func TestInitRejectsExisting(t *testing.T) {
	ctx := context.Background()
	layout := core.NewLayout(filepath.Join(t.TempDir(), "ws"))
	if err := Init(ctx, layout); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := Init(ctx, layout); err == nil {
		t.Error("expected second Init to error on an existing workspace")
	}
}
