package cli

import (
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags="-X grove/internal/cli.Version=...".
var Version = "0.0.0-dev"

type globalFlags struct {
	Workspace string
	Config    string
	JSON      bool
	NoColor   bool
}

var gflags globalFlags

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "grove",
		Short:        "A local knowledge forest for AI tools",
		Long:         "grove indexes your docs, notes, and team exports into a tree-based knowledge structure that AI clients can query through MCP.",
		SilenceUsage: true,
		// Errors are printed once by main.go; let it own the formatting.
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&gflags.Workspace, "workspace", "", "workspace directory (default ~/.grove/default or $GROVE_WORKSPACE)")
	root.PersistentFlags().StringVar(&gflags.Config, "config", "", "config file path (default <workspace>/config.toml)")
	root.PersistentFlags().BoolVar(&gflags.JSON, "json", false, "machine-readable JSON output")
	root.PersistentFlags().BoolVar(&gflags.NoColor, "no-color", false, "disable colored output")

	root.AddCommand(newInitCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newConnectCmd())
	root.AddCommand(newDisconnectCmd())
	root.AddCommand(newSourcesCmd())
	return root
}
