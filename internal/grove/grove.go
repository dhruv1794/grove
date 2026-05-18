// Package grove is the core library: the orchestration API that adapters
// (CLI, MCP, HTTP) call. Adapters hold no business logic and never touch the
// Store or connectors directly — they obtain a *Grove via Open and call its
// methods. The store is hidden behind the store.Store interface here.
package grove

import (
	"context"

	"grove/internal/core"
	"grove/internal/store"
)

// Grove is an open handle to a grove workspace. One handle owns one open
// store connection for its lifetime; close it with Close.
type Grove struct {
	store      store.Store
	layout     core.Layout
	configPath string
}

// Options configures Open.
type Options struct {
	Layout     core.Layout
	ConfigPath string // optional; defaults to Layout.ConfigTOML
}

// Open opens and migrates the workspace store. It returns a *core.Error of
// kind no_workspace if `grove init` hasn't been run, or corrupted_workspace
// if the database can't be opened or migrated.
func Open(ctx context.Context, opts Options) (*Grove, error) {
	layout := opts.Layout
	if !layout.Exists() {
		return nil, core.NewError(core.KindNoWorkspace,
			"no grove workspace at "+layout.Root,
			"run `grove init` to create one")
	}
	s := store.New(layout)
	if err := s.Open(ctx); err != nil {
		return nil, core.WrapError(core.KindCorruptedWorkspace, err,
			"cannot open workspace database at "+layout.DB,
			"the workspace may be corrupted; check permissions or re-run `grove init` in a fresh directory")
	}
	if err := s.Migrate(ctx); err != nil {
		s.Close()
		return nil, core.WrapError(core.KindCorruptedWorkspace, err,
			"cannot migrate workspace database at "+layout.DB,
			"the workspace schema may be corrupted; back it up and re-run `grove init`")
	}
	cp := opts.ConfigPath
	if cp == "" {
		cp = layout.ConfigTOML
	}
	return &Grove{store: s, layout: layout, configPath: cp}, nil
}

// Layout returns the resolved workspace layout for this handle.
func (g *Grove) Layout() core.Layout { return g.layout }

// Close releases the workspace store.
func (g *Grove) Close() error { return g.store.Close() }
