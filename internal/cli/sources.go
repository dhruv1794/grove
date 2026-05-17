package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newSourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sources",
		Short: "List connected sources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			s, _, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()
			sources, err := s.ListSources(ctx)
			if err != nil {
				return err
			}
			if gflags.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(sources)
			}
			if len(sources) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no sources connected)")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tCONNECTED")
			for _, src := range sources {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", src.Name, src.Type, src.ConnectedAt.Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}
}
