package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print grove version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if gflags.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"version": Version})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "grove", Version)
			return nil
		},
	}
}
