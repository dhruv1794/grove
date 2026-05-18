package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <source>",
		Short: "Remove a source and all its indexed documents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			res, err := g.Disconnect(ctx, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "disconnected source %q (%d documents removed)\n", res.Name, res.DocsRemoved)
			return nil
		},
	}
}
