package grove

import (
	"context"
	"fmt"
	"os"

	"grove/internal/core"
	"grove/internal/store"
)

const defaultConfigTOML = `# grove workspace configuration
# Created by ` + "`grove init`" + `. Safe to edit.

[workspace]
schema_version = 1

[build]
# default model used by ` + "`grove build`" + ` when --model is omitted
# model = "ollama/qwen2.5:32b"

[query]
# default model used by ` + "`grove ask`" + ` when --model is omitted
# model = "anthropic/claude-sonnet-4-6"
`

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
