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
			name := args[0]
			s, _, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			src, err := s.GetSource(ctx, name)
			if err != nil {
				return err
			}
			if src == nil {
				return fmt.Errorf("no source named %q", name)
			}
			count, err := s.DeleteSource(ctx, name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "disconnected source %q (%d documents removed)\n", name, count)
			return nil
		},
	}
}
