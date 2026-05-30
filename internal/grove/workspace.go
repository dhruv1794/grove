package grove

import (
	"context"
	"fmt"
	"os"

	"grove/internal/core"
	"grove/internal/store"
)

var defaultConfigTOML = core.RenderConfigTemplate(core.Config{
	Workspace: core.WorkspaceConfig{SchemaVersion: 1},
})

// Init creates a new grove workspace at the given layout: directories,
// config.toml, and a migrated SQLite database. It errors if a workspace
// already exists there.
func Init(ctx context.Context, layout core.Layout) error {
	if layout.Exists() {
		return fmt.Errorf("workspace already initialized at %s", layout.Root)
	}
	for _, d := range []string{layout.Root, layout.Trees, layout.Docs, layout.Auth, layout.Logs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(layout.ConfigTOML, []byte(defaultConfigTOML), 0o644); err != nil {
		return err
	}
	s := store.New(layout)
	if err := s.Open(ctx); err != nil {
		return err
	}
	defer s.Close()
	return s.Migrate(ctx)
}
