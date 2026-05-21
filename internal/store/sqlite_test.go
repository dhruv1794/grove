package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"grove/internal/core"
)

// countDocs returns the number of documents-table rows. Test lives in package
// store, so it can reach s.db directly.
func countDocs(t *testing.T, s *SQLite) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM documents`).Scan(&n); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	return n
}

func newTestStore(t *testing.T) (*SQLite, core.Layout) {
	t.Helper()
	layout := core.NewLayout(t.TempDir())
	s := New(layout)
	ctx := context.Background()
	if err := s.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, layout
}

func TestMigrate_Idempotent(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestDeleteSource_CascadesAllRows(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertSource(ctx, core.Source{
		Name: "notes", Type: core.SourceLocal, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	if err := s.UpsertCollection(ctx, core.Collection{
		Source: "notes", Name: "work", Path: "/tmp/work",
	}); err != nil {
		t.Fatalf("upsert collection: %v", err)
	}
	docs := []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "a.md", Title: "A", Hash: "h1", Content: "a"},
		{ID: "d2", Source: "notes", SourceRef: "b.md", Title: "B", Hash: "h2", Content: "b"},
	}
	if err := s.UpsertDocuments(ctx, docs); err != nil {
		t.Fatalf("upsert documents: %v", err)
	}

	// Second source — must survive the disconnect.
	if err := s.UpsertSource(ctx, core.Source{
		Name: "other", Type: core.SourceLocal, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("upsert other source: %v", err)
	}
	if err := s.UpsertDocument(ctx, core.Document{
		ID: "d3", Source: "other", SourceRef: "c.md", Title: "C", Hash: "h3", Content: "c",
	}); err != nil {
		t.Fatalf("upsert other doc: %v", err)
	}

	deleted, err := s.DeleteSource(ctx, "notes")
	if err != nil {
		t.Fatalf("delete source: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteSource returned %d, want 2", deleted)
	}

	if got, _ := s.GetSource(ctx, "notes"); got != nil {
		t.Errorf("source notes still present after delete")
	}
	if got, _ := s.CountDocuments(ctx, "notes"); got != 0 {
		t.Errorf("docs for notes still present: got %d, want 0", got)
	}
	colls, _ := s.ListCollections(ctx, "notes")
	if len(colls) != 0 {
		t.Errorf("collections for notes still present: got %d, want 0", len(colls))
	}

	// Other source must be intact.
	if got, _ := s.GetSource(ctx, "other"); got == nil {
		t.Errorf("source other was removed; cascade leaked across sources")
	}
	if got, _ := s.CountDocuments(ctx, "other"); got != 1 {
		t.Errorf("docs for other source: got %d, want 1", got)
	}
}

func TestUpsertDocuments_EmptyBatchOK(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertDocuments(context.Background(), nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
}

// fetchDocLinks reads doc_links rows for one document, ordered for stable
// comparison. Test lives in package store, so it can reach s.db directly.
func fetchDocLinks(t *testing.T, s *SQLite, fromDoc string) []core.DocLink {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT to_ref, link_type, anchor FROM doc_links WHERE from_doc = ? ORDER BY to_ref, anchor`, fromDoc)
	if err != nil {
		t.Fatalf("query doc_links: %v", err)
	}
	defer rows.Close()
	var out []core.DocLink
	for rows.Next() {
		var l core.DocLink
		var typ string
		if err := rows.Scan(&l.Target, &typ, &l.Anchor); err != nil {
			t.Fatalf("scan doc_link: %v", err)
		}
		l.Type = core.LinkType(typ)
		out = append(out, l)
	}
	return out
}

func TestUpsertDocuments_PersistsDocLinks(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertSource(ctx, core.Source{
		Name: "notes", Type: core.SourceLocal, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	doc := core.Document{
		ID: "d1", Source: "notes", SourceRef: "a.md", Title: "A", Hash: "h1",
		Links: []core.DocLink{
			{Type: core.LinkWiki, Target: "Session Management"},
			{Type: core.LinkDocRef, Target: "rfc.md", Anchor: "goals"},
		},
	}
	if err := s.UpsertDocuments(ctx, []core.Document{doc}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got := fetchDocLinks(t, s, "d1")
	// ORDER BY to_ref: "Session Management" sorts before "rfc.md" (BINARY).
	want := []core.DocLink{
		{Type: core.LinkWiki, Target: "Session Management"},
		{Type: core.LinkDocRef, Target: "rfc.md", Anchor: "goals"},
	}
	if len(got) != len(want) {
		t.Fatalf("doc_links: got %d rows, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("doc_link[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Re-upserting a doc must replace its links, not accumulate them.
func TestUpsertDocuments_ReplacesDocLinks(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertSource(ctx, core.Source{
		Name: "notes", Type: core.SourceLocal, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	doc := core.Document{
		ID: "d1", Source: "notes", SourceRef: "a.md", Hash: "h1",
		Links: []core.DocLink{{Type: core.LinkWiki, Target: "Old"}},
	}
	if err := s.UpsertDocuments(ctx, []core.Document{doc}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	doc.Hash = "h2"
	doc.Links = []core.DocLink{{Type: core.LinkWiki, Target: "New"}}
	if err := s.UpsertDocuments(ctx, []core.Document{doc}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got := fetchDocLinks(t, s, "d1")
	if len(got) != 1 || got[0].Target != "New" {
		t.Errorf("doc_links after re-upsert: got %v, want one link to New", got)
	}
}

// A doc that loses all its links must end up with zero doc_links rows.
func TestUpsertDocument_ClearsDocLinksWhenRemoved(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertSource(ctx, core.Source{
		Name: "notes", Type: core.SourceLocal, ConnectedAt: time.Now(),
	}); err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	doc := core.Document{
		ID: "d1", Source: "notes", SourceRef: "a.md", Hash: "h1",
		Links: []core.DocLink{{Type: core.LinkWiki, Target: "Gone"}},
	}
	if err := s.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	doc.Hash = "h2"
	doc.Links = nil
	if err := s.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if got := fetchDocLinks(t, s, "d1"); len(got) != 0 {
		t.Errorf("doc_links after link removal: got %v, want none", got)
	}
}

// Re-upserting the same ID updates the row in place (ON CONFLICT(id)) rather
// than inserting a duplicate, and the mutable fields and FTS index follow the
// latest values.
func TestUpsertDocument_IdempotentOnConflict(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal, ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	doc := core.Document{
		ID: "d1", Source: "notes", SourceRef: "a.md", Collection: "old",
		Title: "Original", Hash: "h1", Content: "first revision",
	}
	if err := s.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Re-ingest with the same ID but changed mutable fields.
	doc.Collection = "new"
	doc.Title = "Renamed"
	doc.Hash = "h2"
	doc.Content = "second revision"
	if err := s.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if n := countDocs(t, s); n != 1 {
		t.Fatalf("documents row count = %d, want 1 (conflict updated in place)", n)
	}
	got, err := s.GetDocument(ctx, "d1")
	if err != nil || got == nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.Title != "Renamed" || got.Hash != "h2" || got.Collection != "new" {
		t.Errorf("after conflict update = %+v, want Title=Renamed Hash=h2 Collection=new", got)
	}
	// FTS reflects the new title; the old one no longer matches.
	if hits, _ := s.SearchDocuments(ctx, "Renamed", "", 10); len(hits) != 1 || hits[0] != "d1" {
		t.Errorf("FTS search 'Renamed' = %v, want [d1]", hits)
	}
	if hits, _ := s.SearchDocuments(ctx, "Original", "", 10); len(hits) != 0 {
		t.Errorf("FTS search 'Original' = %v, want none after rename", hits)
	}
}

// writeDocPayload is content-addressed and uses O_EXCL: the first writer for a
// hash wins, and a later write for the same hash is a silent no-op. (A shared
// hash means identical content by construction, so this is safe; it also makes
// concurrent writers for the same hash race-free.)
func TestWriteDocPayload_OExclPreservesFirst(t *testing.T) {
	s, _ := newTestStore(t)
	first := core.Document{ID: "d1", Source: "notes", SourceRef: "a.md", Hash: "shared", Content: "ORIGINAL"}
	if err := s.writeDocPayload(first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// A second doc reusing the hash must not clobber the on-disk payload, and
	// must not error despite O_EXCL.
	second := core.Document{ID: "d2", Source: "notes", SourceRef: "b.md", Hash: "shared", Content: "DIFFERENT"}
	if err := s.writeDocPayload(second); err != nil {
		t.Fatalf("second write (should be a silent no-op): %v", err)
	}
	body, err := os.ReadFile(core.PayloadPath(s.layout.Docs, "shared", "doc.json"))
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	var p core.Document
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Content != "ORIGINAL" {
		t.Errorf("payload content = %q, want ORIGINAL preserved by O_EXCL", p.Content)
	}
}

// A failure partway through a batch must roll back the whole transaction: no
// document from the batch is left committed. The FK on documents.source lets
// us force the failure — the second doc names a source that doesn't exist.
func TestUpsertDocuments_RollsBackOnMidBatchError(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal, ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	batch := []core.Document{
		{ID: "ok", Source: "notes", SourceRef: "a.md", Hash: "h1"},
		{ID: "bad", Source: "ghost", SourceRef: "b.md", Hash: "h2"}, // FK violation
	}
	if err := s.UpsertDocuments(ctx, batch); err == nil {
		t.Fatal("expected FK error on mid-batch doc, got nil")
	}
	// The first doc, inserted earlier in the same tx, must be rolled back.
	if n := countDocs(t, s); n != 0 {
		t.Errorf("documents row count = %d after failed batch, want 0 (rolled back)", n)
	}
	if got, _ := s.GetDocument(ctx, "ok"); got != nil {
		t.Errorf("doc 'ok' survived a rolled-back batch: %+v", got)
	}
}

func seedTree(t *testing.T, s *SQLite, treeID string) {
	t.Helper()
	if err := s.UpsertTree(context.Background(), core.Tree{
		ID: treeID, Source: "notes", Name: treeID, PromptVer: "p1", BuildModel: "ollama/x",
	}); err != nil {
		t.Fatalf("upsert tree %s: %v", treeID, err)
	}
}

func findNode(nodes []core.Node, id string) *core.Node {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}

func TestUpsertNodes_RoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	seedTree(t, s, "t1")

	nodes := []core.Node{
		{ID: "n-root", TreeID: "t1", Title: "Root", Depth: 0, ContentHash: "ch-root", PromptVer: "p1"},
		{ID: "n-a", TreeID: "t1", ParentID: "n-root", Title: "A", Depth: 1, ContentHash: "ch-a", PromptVer: "p1",
			DocIDs: []string{"d1", "d2"}},
		{ID: "n-b", TreeID: "t1", ParentID: "n-root", Title: "B", Depth: 1, ContentHash: "ch-b", PromptVer: "p1",
			DocIDs:  []string{"d3"},
			SeeAlso: []core.NodeRef{{NodeID: "n-a", Reason: "overlap", Strength: 0.5}}},
	}
	if err := s.UpsertNodes(ctx, nodes); err != nil {
		t.Fatalf("upsert nodes: %v", err)
	}

	got, err := s.ListNodesByTree(ctx, "t1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListNodesByTree: got %d nodes, want 3", len(got))
	}
	if got[0].ID != "n-root" { // ORDER BY depth, id — depth-0 root first
		t.Errorf("first node = %s, want n-root", got[0].ID)
	}
	na := findNode(got, "n-a")
	if na == nil || len(na.DocIDs) != 2 || na.DocIDs[0] != "d1" || na.DocIDs[1] != "d2" {
		t.Errorf("n-a DocIDs = %v, want [d1 d2]", na)
	}
	nb := findNode(got, "n-b")
	if nb == nil || len(nb.SeeAlso) != 1 {
		t.Fatalf("n-b SeeAlso = %+v, want one ref", nb)
	}
	if nb.SeeAlso[0].NodeID != "n-a" || nb.SeeAlso[0].Reason != "overlap" || nb.SeeAlso[0].Strength != 0.5 {
		t.Errorf("n-b SeeAlso[0] = %+v", nb.SeeAlso[0])
	}

	one, err := s.GetNode(ctx, "n-b")
	if err != nil || one == nil {
		t.Fatalf("GetNode n-b: %v", err)
	}
	if one.Title != "B" || len(one.DocIDs) != 1 || one.DocIDs[0] != "d3" {
		t.Errorf("GetNode n-b = %+v", one)
	}
	if len(one.SeeAlso) != 1 || one.SeeAlso[0].NodeID != "n-a" {
		t.Errorf("GetNode n-b SeeAlso = %v", one.SeeAlso)
	}
	if missing, _ := s.GetNode(ctx, "nope"); missing != nil {
		t.Errorf("GetNode of missing id returned %+v", missing)
	}
}

// Re-upserting a node must replace its edges, not accumulate them.
func TestUpsertNodes_ReplacesEdges(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	seedTree(t, s, "t1")

	n := core.Node{ID: "n1", TreeID: "t1", Title: "N", ContentHash: "h1", PromptVer: "p1",
		DocIDs:  []string{"d1", "d2"},
		SeeAlso: []core.NodeRef{{NodeID: "x", Strength: 0.1}}}
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	n.DocIDs = []string{"d9"}
	n.SeeAlso = nil
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := s.GetNode(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DocIDs) != 1 || got.DocIDs[0] != "d9" {
		t.Errorf("DocIDs after re-upsert = %v, want [d9]", got.DocIDs)
	}
	if len(got.SeeAlso) != 0 {
		t.Errorf("SeeAlso after re-upsert = %v, want none", got.SeeAlso)
	}
}

func TestUpsertNodes_EmptyBatchOK(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.UpsertNodes(context.Background(), nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
}

func TestGetNodesByContentHash(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	seedTree(t, s, "t1")
	seedTree(t, s, "t2")

	// Same content hash across two trees — the cache lookup finds both.
	if err := s.UpsertNodes(ctx, []core.Node{
		{ID: "a", TreeID: "t1", Title: "A", ContentHash: "shared", PromptVer: "p1"},
		{ID: "b", TreeID: "t2", Title: "B", ContentHash: "shared", PromptVer: "p1"},
		{ID: "c", TreeID: "t1", Title: "C", ContentHash: "other", PromptVer: "p1"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNodesByContentHash(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("GetNodesByContentHash(shared) = %v, want nodes a,b", got)
	}
	if none, _ := s.GetNodesByContentHash(ctx, "missing"); len(none) != 0 {
		t.Errorf("unknown hash returned %d nodes", len(none))
	}
}

// AddNodeSeeAlso adds edges without clearing a node's existing node_docs, and
// re-adding an edge updates it in place rather than duplicating.
func TestAddNodeSeeAlso(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	seedTree(t, s, "t1")
	if err := s.UpsertNodes(ctx, []core.Node{
		{ID: "n1", TreeID: "t1", Title: "N1", ContentHash: "h1", PromptVer: "p1", DocIDs: []string{"d1"}},
		{ID: "n2", TreeID: "t1", Title: "N2", ContentHash: "h2", PromptVer: "p1"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.AddNodeSeeAlso(ctx, map[string][]core.NodeRef{
		"n1": {{NodeID: "n2", Reason: "links", Strength: 1.0}},
	}); err != nil {
		t.Fatal(err)
	}
	n1, _ := s.GetNode(ctx, "n1")
	if n1 == nil || len(n1.DocIDs) != 1 || n1.DocIDs[0] != "d1" {
		t.Errorf("n1 DocIDs = %+v, want [d1] preserved (edges are additive)", n1)
	}
	if len(n1.SeeAlso) != 1 || n1.SeeAlso[0].NodeID != "n2" || n1.SeeAlso[0].Strength != 1.0 {
		t.Errorf("n1 SeeAlso = %+v, want one edge to n2 strength 1.0", n1.SeeAlso)
	}

	// Re-adding the same (from,to) updates reason/strength, not a duplicate row.
	if err := s.AddNodeSeeAlso(ctx, map[string][]core.NodeRef{
		"n1": {{NodeID: "n2", Reason: "weaker", Strength: 0.5}},
	}); err != nil {
		t.Fatal(err)
	}
	n1, _ = s.GetNode(ctx, "n1")
	if len(n1.SeeAlso) != 1 || n1.SeeAlso[0].Reason != "weaker" || n1.SeeAlso[0].Strength != 0.5 {
		t.Errorf("after re-add, n1 SeeAlso = %+v, want a single updated edge", n1.SeeAlso)
	}

	if err := s.AddNodeSeeAlso(ctx, nil); err != nil {
		t.Errorf("empty edges should be a no-op: %v", err)
	}
}

func TestDeleteNodesByTree(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	seedTree(t, s, "t1")
	seedTree(t, s, "t2")

	if err := s.UpsertNodes(ctx, []core.Node{
		{ID: "t1-root", TreeID: "t1", Title: "R", ContentHash: "h", PromptVer: "p1", DocIDs: []string{"d1"}},
		{ID: "t1-leaf", TreeID: "t1", ParentID: "t1-root", Title: "L", ContentHash: "h2", PromptVer: "p1"},
	}); err != nil {
		t.Fatal(err)
	}
	// A t2 node with a cross-tree SeeAlso edge pointing into t1.
	if err := s.UpsertNode(ctx, core.Node{
		ID: "t2-n", TreeID: "t2", Title: "X", ContentHash: "h3", PromptVer: "p1",
		SeeAlso: []core.NodeRef{{NodeID: "t1-leaf", Reason: "ref", Strength: 0.9}},
	}); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.DeleteNodesByTree(ctx, "t1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteNodesByTree returned %d, want 2", deleted)
	}
	if rest, _ := s.ListNodesByTree(ctx, "t1"); len(rest) != 0 {
		t.Errorf("t1 still has %d nodes after delete", len(rest))
	}
	// t2's node survives; its dangling cross-tree edge into t1 is gone.
	t2n, err := s.GetNode(ctx, "t2-n")
	if err != nil || t2n == nil {
		t.Fatalf("t2-n should survive: %v", err)
	}
	if len(t2n.SeeAlso) != 0 {
		t.Errorf("cross-tree SeeAlso into deleted tree not cleaned: %v", t2n.SeeAlso)
	}
}

func TestDocLinksRead(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertSource(ctx, core.Source{Name: "notes", Type: core.SourceLocal, ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "a.md", Hash: "h1", Links: []core.DocLink{
			{Type: core.LinkWiki, Target: "Topic"},
			{Type: core.LinkDocRef, Target: "b.md", Anchor: "intro"},
		}},
		{ID: "d2", Source: "notes", SourceRef: "b.md", Hash: "h2", Links: []core.DocLink{
			{Type: core.LinkURL, Target: "https://example.com"},
		}},
		{ID: "d3", Source: "notes", SourceRef: "c.md", Hash: "h3"},
	}); err != nil {
		t.Fatal(err)
	}

	d1Links, err := s.GetDocLinks(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	// ORDER BY to_ref: "Topic" (0x54) sorts before "b.md" (0x62) under BINARY.
	if len(d1Links) != 2 || d1Links[0] != (core.DocLink{Type: core.LinkWiki, Target: "Topic"}) {
		t.Errorf("GetDocLinks(d1) = %+v", d1Links)
	}
	if got, _ := s.GetDocLinks(ctx, "d3"); len(got) != 0 {
		t.Errorf("d3 has no links, got %v", got)
	}

	bySource, err := s.ListDocLinksBySource(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(bySource) != 2 {
		t.Errorf("ListDocLinksBySource: %d docs with links, want 2 (d1,d2)", len(bySource))
	}
	if len(bySource["d1"]) != 2 || len(bySource["d2"]) != 1 {
		t.Errorf("link counts: d1=%d d2=%d, want 2,1", len(bySource["d1"]), len(bySource["d2"]))
	}
	if _, ok := bySource["d3"]; ok {
		t.Error("d3 has no links and should not be a map key")
	}
}

func TestSearchDocuments(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"notes", "other"} {
		if err := s.UpsertSource(ctx, core.Source{Name: name, Type: core.SourceLocal, ConnectedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertDocuments(ctx, []core.Document{
		{ID: "d1", Source: "notes", SourceRef: "a.md", Title: "Login", Hash: "h1",
			Content: "Users sign in with a password."},
		{ID: "d2", Source: "notes", SourceRef: "b.md", Title: "Deploy", Hash: "h2",
			Content: "The login service restarts after a deploy."},
		{ID: "d3", Source: "other", SourceRef: "c.md", Title: "Misc", Hash: "h3",
			Content: "An unrelated login note."},
	}); err != nil {
		t.Fatal(err)
	}

	// Title matches outrank content matches: d1 ("Login" in title) before d2.
	got, err := s.SearchDocuments(ctx, "login", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("search login: got %v, want 3 hits", got)
	}
	if got[0] != "d1" {
		t.Errorf("search login best hit = %s, want d1 (title weight)", got[0])
	}

	// source filter restricts scope.
	got, err = s.SearchDocuments(ctx, "login", "notes", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("search login source=notes: got %v, want d1,d2", got)
	}

	// limit caps the result count.
	if got, _ = s.SearchDocuments(ctx, "login", "", 1); len(got) != 1 {
		t.Errorf("search login limit=1: got %v, want 1", got)
	}

	// no usable terms → no results, no error.
	if got, err = s.SearchDocuments(ctx, "  ??  ", "", 10); err != nil || got != nil {
		t.Errorf("search punctuation-only: got %v, %v", got, err)
	}

	// DeleteSource clears the source's FTS rows.
	if _, err := s.DeleteSource(ctx, "notes"); err != nil {
		t.Fatal(err)
	}
	got, err = s.SearchDocuments(ctx, "login", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "d3" {
		t.Errorf("search login after delete: got %v, want [d3]", got)
	}
}

func TestFindDocuments(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		if err := s.UpsertSource(ctx, core.Source{Name: name, Type: core.SourceLocal, ConnectedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	// Two docs in different sources share the source_ref "notes.md".
	if err := s.UpsertDocuments(ctx, []core.Document{
		{ID: "id-a", Source: "a", SourceRef: "notes.md", Title: "A", Hash: "h1"},
		{ID: "id-b", Source: "b", SourceRef: "notes.md", Title: "B", Hash: "h2"},
		{ID: "id-c", Source: "a", SourceRef: "other.md", Title: "C", Hash: "h3"},
	}); err != nil {
		t.Fatal(err)
	}

	// Resolve by document ID — exactly one match.
	if got, err := s.FindDocuments(ctx, "id-c"); err != nil || len(got) != 1 || got[0].ID != "id-c" {
		t.Errorf("FindDocuments(id-c) = %v, %v", got, err)
	}
	// Resolve by a colliding source_ref — both, ordered by source.
	got, err := s.FindDocuments(ctx, "notes.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Source != "a" || got[1].Source != "b" {
		t.Errorf("FindDocuments(notes.md) = %v, want id-a then id-b", got)
	}
	// Unknown reference — no match, no error.
	if got, err := s.FindDocuments(ctx, "missing"); err != nil || len(got) != 0 {
		t.Errorf("FindDocuments(missing) = %v, %v", got, err)
	}
}

func TestCountDocumentsBySource(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		if err := s.UpsertSource(ctx, core.Source{
			Name: name, Type: core.SourceLocal, ConnectedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	docs := []core.Document{
		{ID: "1", Source: "a", SourceRef: "1", Hash: "ha1"},
		{ID: "2", Source: "a", SourceRef: "2", Hash: "ha2"},
		{ID: "3", Source: "b", SourceRef: "1", Hash: "hb1"},
	}
	if err := s.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
	counts, err := s.CountDocumentsBySource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["a"] != 2 || counts["b"] != 1 {
		t.Errorf("counts: got %v, want {a:2 b:1}", counts)
	}
}
