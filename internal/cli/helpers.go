package cli

import (
	"context"

	"grove/internal/core"
	"grove/internal/store"
)

// resolveWorkspace returns the layout for the current invocation, honoring
// --workspace, GROVE_WORKSPACE, then the default.
func resolveWorkspace() (core.Layout, error) {
	root := gflags.Workspace
	if root == "" {
		def, err := core.DefaultWorkspace()
		if err != nil {
			return core.Layout{}, err
		}
		root = def
	}
	return core.NewLayout(root), nil
}

// configPath returns the config.toml path for this invocation: --config if
// set, otherwise the workspace default.
func configPath(layout core.Layout) string {
	if gflags.Config != "" {
		return gflags.Config
	}
	return layout.ConfigTOML
}

// openStore opens and migrates the SQLite store for the active workspace.
// Returns a *core.Error of kind no_workspace if `grove init` hasn't been
// run, or corrupted_workspace if the database can't be opened or migrated.
func openStore(ctx context.Context) (*store.SQLite, core.Layout, error) {
	layout, err := resolveWorkspace()
	if err != nil {
		return nil, layout, err
	}
	if !layout.Exists() {
		return nil, layout, core.NewError(core.KindNoWorkspace,
			"no grove workspace at "+layout.Root,
			"run `grove init` to create one")
	}
	s := store.New(layout)
	if err := s.Open(ctx); err != nil {
		return nil, layout, core.WrapError(core.KindCorruptedWorkspace, err,
			"cannot open workspace database at "+layout.DB,
			"the workspace may be corrupted; check permissions or re-run `grove init` in a fresh directory")
	}
	if err := s.Migrate(ctx); err != nil {
		s.Close()
		return nil, layout, core.WrapError(core.KindCorruptedWorkspace, err,
			"cannot migrate workspace database at "+layout.DB,
			"the workspace schema may be corrupted; back it up and re-run `grove init`")
	}
	return s, layout, nil
}
