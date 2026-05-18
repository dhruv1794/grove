package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"grove/internal/core"
	"grove/internal/grove"
)

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
			if err := grove.Init(ctx, layout); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized grove workspace at %s\n", layout.Root)
			return nil
		},
	}
}
