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

// ConfigPath returns the config.toml path this handle resolves models from.
func (g *Grove) ConfigPath() string { return g.configPath }

// SetQueryModel persists the default query model, surviving across sessions. It
// writes the local workspace config when that file already sets a query model
// (an opted-in per-forest override), otherwise the global ~/.grove/config.toml
// so the default applies to every forest.
func (g *Grove) SetQueryModel(model string) error {
	local, _ := core.LoadRawConfig(g.configPath)
	return core.UpdateConfigFile(g.modelWritePath(local.Query.Model != ""),
		func(c *core.Config) { c.Query.Model = model })
}

// SetBuildModel persists the default build/index model with the same
// local-if-overridden-else-global rule as SetQueryModel.
func (g *Grove) SetBuildModel(model string) error {
	local, _ := core.LoadRawConfig(g.configPath)
	return core.UpdateConfigFile(g.modelWritePath(local.Build.Model != ""),
		func(c *core.Config) { c.Build.Model = model })
}

// modelWritePath returns the local config path when the forest already
// overrides the key, else the global config path (falling back to local if the
// home dir can't be resolved).
func (g *Grove) modelWritePath(localOverrides bool) string {
	if localOverrides {
		return g.configPath
	}
	if gp, err := core.GlobalConfigPath(); err == nil {
		return gp
	}
	return g.configPath
}

// Close releases the workspace store.
func (g *Grove) Close() error { return g.store.Close() }
