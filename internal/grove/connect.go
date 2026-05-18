package grove

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"grove/internal/connectors"
	"grove/internal/connectors/local"
	"grove/internal/core"
)

// ConnectLocalOpts configures ConnectLocal.
type ConnectLocalOpts struct {
	Path       string
	Name       string // optional; defaults to "local-<basename>"
	Collection string
	Include    []string
	Exclude    []string
	MaxSizeMB  int64
}

// ConnectResult reports the outcome of a connect call.
type ConnectResult struct {
	Name     string
	Path     string
	DocCount int
}

// ConnectLocal connects a local folder as a source and indexes its documents
// into the workspace store in a single batch.
func (g *Grove) ConnectLocal(ctx context.Context, opts ConnectLocalOpts) (*ConnectResult, error) {
	abs, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, err
	}
	name := opts.Name
	if name == "" {
		name = "local-" + filepath.Base(abs)
	}

	conn := local.New()
	cfg := connectors.ConnectorConfig{
		Name:       name,
		Collection: opts.Collection,
		Include:    opts.Include,
		Exclude:    opts.Exclude,
		MaxSizeMB:  opts.MaxSizeMB,
		Custom:     map[string]string{"path": abs},
	}
	if err := conn.Connect(ctx, cfg); err != nil {
		return nil, err
	}

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal connector config for %q: %w", name, err)
	}
	src := core.Source{
		Name:        name,
		Type:        core.SourceLocal,
		ConfigJSON:  string(cfgJSON),
		ConnectedAt: time.Now(),
	}
	if err := g.store.UpsertSource(ctx, src); err != nil {
		return nil, err
	}
	if opts.Collection != "" {
		if err := g.store.UpsertCollection(ctx, core.Collection{Source: name, Name: opts.Collection, Path: abs}); err != nil {
			return nil, err
		}
	}

	// Buffer the whole stream so the upsert is one transaction.
	batch, err := connectors.Collect(conn.Documents(ctx, connectors.StreamOpts{}))
	if err != nil {
		return nil, err
	}
	if err := g.store.UpsertDocuments(ctx, batch); err != nil {
		return nil, err
	}
	if err := g.store.TouchSourceSync(ctx, name, time.Now()); err != nil {
		return nil, err
	}

	return &ConnectResult{Name: name, Path: abs, DocCount: len(batch)}, nil
}
