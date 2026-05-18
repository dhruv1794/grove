package cli

import (
	"context"

	"grove/internal/core"
	"grove/internal/grove"
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

// openGrove opens the core-library handle for the active workspace, honoring
// --workspace/--config. It returns a *core.Error of kind no_workspace if
// `grove init` hasn't been run, or corrupted_workspace if the database can't
// be opened or migrated.
func openGrove(ctx context.Context) (*grove.Grove, error) {
	layout, err := resolveWorkspace()
	if err != nil {
		return nil, err
	}
	return grove.Open(ctx, grove.Options{Layout: layout, ConfigPath: gflags.Config})
}
