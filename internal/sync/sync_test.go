package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"grove/internal/connectors"
	"grove/internal/core"
	"grove/internal/store"
)

// fakeConnector emits a fixed document set, standing in for a re-walk so the
// diff against the store is deterministic.
type fakeConnector struct{ docs []core.Document }

func (c *fakeConnector) Name() string                                              { return "fake" }
func (c *fakeConnector) Connect(context.Context, connectors.ConnectorConfig) error { return nil }
func (c *fakeConnector) Disconnect(context.Context) error                          { return nil }
func (c *fakeConnector) Validate(context.Context) error                            { return nil }
func (c *fakeConnector) Capabilities() connectors.Capabilities                     { return connectors.Capabilities{} }

// Changes emits every doc as Modified (ignoring since — connector-level mtime
// filtering is tested in the local package); Compute reclassifies by hash.
func (c *fakeConnector) Changes(context.Context, time.Time) (<-chan core.Change, <-chan error) {
	changes := make(chan core.Change, len(c.docs))
	errs := make(chan error, 1)
	for i := range c.docs {
		d := c.docs[i]
		changes <- core.Change{DocID: d.ID, Type: core.ChangeModified, Document: &d}
	}
	close(changes)
	close(errs)
	return changes, errs
}

func (c *fakeConnector) Enumerate(context.Context) (<-chan string, <-chan error) {
	ids := make(chan string, len(c.docs))
	errs := make(chan error, 1)
	for _, d := range c.docs {
		ids <- d.ID
	}
	close(ids)
	close(errs)
	return ids, errs
}

func (c *fakeConnector) Documents(ctx context.Context, _ connectors.StreamOpts) (<-chan core.Document, <-chan error) {
	docs := make(chan core.Document, len(c.docs))
	errs := make(chan error, 1)
	for _, d := range c.docs {
		docs <- d
	}
	close(docs)
	close(errs)
	return docs, errs
}

func newStore(t *testing.T) *store.SQLite {
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
	return s
}

func doc(id, hash, content string) core.Document {
	return core.Document{ID: id, Source: "src", SourceRef: id + ".md", Title: id, Content: content, Hash: hash}
}

func TestComputeAndApply(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.UpsertSource(ctx, core.Source{Name: "src", Type: core.SourceLocal}); err != nil {
		t.Fatal(err)
	}
	// Initial state: a, b.
	if err := s.UpsertDocuments(ctx, []core.Document{doc("a", "h1", "A"), doc("b", "h2", "B")}); err != nil {
		t.Fatal(err)
	}

	// Walk reports a unchanged, b's hash changed, and c new.
	conn := &fakeConnector{docs: []core.Document{
		doc("a", "h1", "A"),     // unchanged
		doc("b", "h2x", "B v2"), // modified
		doc("c", "h3", "C"),     // created
	}}
	d, err := Compute(ctx, s, conn, "src", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Created) != 1 || d.Created[0].ID != "c" {
		t.Errorf("Created = %+v, want [c]", d.Created)
	}
	if len(d.Modified) != 1 || d.Modified[0].ID != "b" {
		t.Errorf("Modified = %+v, want [b]", d.Modified)
	}
	if len(d.Deleted) != 0 {
		t.Errorf("Deleted = %v, want none", d.Deleted)
	}
	if d.Empty() {
		t.Error("diff reported empty despite changes")
	}

	if err := Apply(ctx, s, d); err != nil {
		t.Fatal(err)
	}

	// After apply: store holds a, b(h2x), c. A second walk with c removed should
	// report exactly one deletion and no other changes.
	conn2 := &fakeConnector{docs: []core.Document{doc("a", "h1", "A"), doc("b", "h2x", "B v2")}}
	d2, err := Compute(ctx, s, conn2, "src", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.Created)+len(d2.Modified) != 0 {
		t.Errorf("second diff has unexpected create/modify: %+v", d2)
	}
	if len(d2.Deleted) != 1 || d2.Deleted[0] != "c" {
		t.Errorf("Deleted = %v, want [c]", d2.Deleted)
	}

	if err := Apply(ctx, s, d2); err != nil {
		t.Fatal(err)
	}
	digests, err := s.ListDocumentDigests(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 2 || digests["a"] != "h1" || digests["b"] != "h2x" {
		t.Errorf("final digests = %v, want {a:h1, b:h2x}", digests)
	}

	// A no-change walk yields an empty diff (the no-op path).
	d3, err := Compute(ctx, s, conn2, "src", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !d3.Empty() {
		t.Errorf("expected empty diff, got %+v", d3)
	}
}
