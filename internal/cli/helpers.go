package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"grove/internal/core"
	"grove/internal/grove"
)

// expandHomePath expands a leading ~ to the user's home directory.
func expandHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

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

// openGroveAt opens a handle for an explicit workspace path. Used by the REPL's
// /workspace command to switch forests mid-session. The config is resolved from
// the workspace itself (not the session's --config).
func openGroveAt(ctx context.Context, root string) (*grove.Grove, error) {
	root = expandHomePath(root)
	return grove.Open(ctx, grove.Options{Layout: core.NewLayout(root)})
}
