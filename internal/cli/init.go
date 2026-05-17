package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

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

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [path]",
		Short: "Initialize a grove workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var layout core.Layout
			if len(args) == 1 {
				layout = core.NewLayout(args[0])
			} else {
				l, err := resolveWorkspace()
				if err != nil {
					return err
				}
				layout = l
			}
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
			if err := s.Migrate(ctx); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized grove workspace at %s\n", layout.Root)
			return nil
		},
	}
}
