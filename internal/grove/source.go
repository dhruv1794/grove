package grove

import (
	"context"
	"fmt"
	"time"

	"grove/internal/core"
)

// DisconnectResult reports the outcome of a disconnect call.
type DisconnectResult struct {
	Name        string
	DocsRemoved int
}

// Disconnect removes a source and cascade-deletes its documents, collections,
// and doc_links rows. It errors if no source with that name exists.
func (g *Grove) Disconnect(ctx context.Context, name string) (*DisconnectResult, error) {
	src, err := g.store.GetSource(ctx, name)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, fmt.Errorf("no source named %q", name)
	}
	count, err := g.store.DeleteSource(ctx, name)
	if err != nil {
		return nil, err
	}
	return &DisconnectResult{Name: name, DocsRemoved: count}, nil
}

// ListSources returns every connected source.
func (g *Grove) ListSources(ctx context.Context) ([]core.Source, error) {
	return g.store.ListSources(ctx)
}

// Document returns one document — metadata plus full content — by its ID, read
// from the content-addressed store (never an arbitrary filesystem path). It
// returns a misuse error when no document has that ID. Behind /api/doc/:id.
func (g *Grove) Document(ctx context.Context, id string) (*core.Document, error) {
	docs, err := g.store.GetDocuments(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, core.NewError(core.KindMisuse,
			"no document with id "+id,
			"use an id from a tree node or a citation")
	}
	return &docs[0], nil
}

// SourceStatus is one source's entry in a StatusReport.
type SourceStatus struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	DocCount   int       `json:"doc_count"`
	LastSyncAt time.Time `json:"last_sync_at"`
}

// StatusReport is the workspace snapshot returned by Status.
type StatusReport struct {
	Workspace string      `json:"workspace"`
	Config    core.Config `json:"config"`
	Sources   []SourceStatus `json:"sources"`
}

// Status returns a snapshot of the workspace: its config and every connected
// source with a document count.
func (g *Grove) Status(ctx context.Context) (*StatusReport, error) {
	sources, err := g.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := g.store.CountDocumentsBySource(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := core.LoadMergedConfig(g.configPath)
	if err != nil {
		return nil, err
	}
	report := &StatusReport{Workspace: g.layout.Root, Config: cfg}
	for _, src := range sources {
		report.Sources = append(report.Sources, SourceStatus{
			Name:       src.Name,
			Type:       string(src.Type),
			DocCount:   counts[src.Name],
			LastSyncAt: src.LastSyncAt,
		})
	}
	return report, nil
}
