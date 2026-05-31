package cli

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"grove/internal/core"
	webhttp "grove/internal/adapters/http"
)

func newServeCmd() *cobra.Command {
	var web, mcp, noOpen bool
	var port int
	var auth string

	cmd := &cobra.Command{
		Use:   "serve [flags]",
		Short: "Serve the forest over a web UI (or MCP)",
		Long: "serve runs a long-lived server exposing the forest.\n\n" +
			"  --web   localhost web UI + JSON/SSE API (default)\n" +
			"  --mcp   MCP stdio transport (not yet implemented)\n\n" +
			"The web server binds 127.0.0.1 only; pass --auth to require a bearer token.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if mcp {
				return core.NewError(core.KindMisuse,
					"grove serve --mcp is not implemented yet",
					"the MCP transport is the final v0.1 milestone; use --web for now")
			}
			// --web is the only transport today, so default to it when no flag is given.
			_ = web

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			srv := webhttp.New(g, webhttp.Options{
				Port:        port,
				Auth:        auth,
				OpenBrowser: !noOpen,
			})
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().BoolVar(&web, "web", false, "serve the localhost web UI + JSON/SSE API (default transport)")
	cmd.Flags().BoolVar(&mcp, "mcp", false, "serve the MCP stdio transport (not yet implemented)")
	cmd.Flags().IntVar(&port, "port", 0, "listen port (default: pick a free one)")
	cmd.Flags().StringVar(&auth, "auth", "", "require this bearer token on /api/* requests")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open the browser automatically")
	return cmd
}
