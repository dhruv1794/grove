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

// SourceStatus is one source's entry in a StatusReport.
type SourceStatus struct {
	Name       string
	Type       string
	DocCount   int
	LastSyncAt time.Time
}

// StatusReport is the workspace snapshot returned by Status.
type StatusReport struct {
	Workspace string
	Config    core.Config
	Sources   []SourceStatus
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
