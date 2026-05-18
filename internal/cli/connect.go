package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"grove/internal/grove"
)

func newConnectCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "connect <source-type> [args]",
		Short: "Connect a source to the workspace",
	}
	c.AddCommand(newConnectLocalCmd())
	return c
}

func newConnectLocalCmd() *cobra.Command {
	var name, collection string
	var includes, excludes []string
	var maxSizeMB int64

	cmd := &cobra.Command{
		Use:   "local <path>",
		Short: "Connect a local folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			res, err := g.ConnectLocal(ctx, grove.ConnectLocalOpts{
				Path:       args[0],
				Name:       name,
				Collection: collection,
				Include:    includes,
				Exclude:    excludes,
				MaxSizeMB:  maxSizeMB,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "connected local source %q at %s (%d docs indexed)\n", res.Name, res.Path, res.DocCount)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "source name (default local-<basename>)")
	cmd.Flags().StringVar(&collection, "collection", "", "logical grouping name")
	cmd.Flags().StringSliceVar(&includes, "include", nil, "glob patterns to include")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "glob patterns to exclude")
	cmd.Flags().Int64Var(&maxSizeMB, "max-size", 0, "skip files larger than N megabytes (0 = unlimited)")
	return cmd
}
