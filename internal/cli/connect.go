package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"grove/internal/connectors"
	"grove/internal/connectors/local"
	"grove/internal/core"
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
			abs, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			if name == "" {
				name = "local-" + filepath.Base(abs)
			}

			s, _, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			conn := local.New()
			cfg := connectors.ConnectorConfig{
				Name:       name,
				Collection: collection,
				Include:    includes,
				Exclude:    excludes,
				MaxSizeMB:  maxSizeMB,
				Custom:     map[string]string{"path": abs},
			}
			if err := conn.Connect(ctx, cfg); err != nil {
				return err
			}

			cfgJSON, err := json.Marshal(cfg)
			if err != nil {
				return err
			}
			src := core.Source{
				Name:        name,
				Type:        core.SourceLocal,
				ConfigJSON:  string(cfgJSON),
				ConnectedAt: time.Now(),
			}
			if err := s.UpsertSource(ctx, src); err != nil {
				return err
			}
			if collection != "" {
				if err := s.UpsertCollection(ctx, core.Collection{Source: name, Name: collection, Path: abs}); err != nil {
					return err
				}
			}

			// Drain the connector to a buffer, then upsert in one transaction.
			batch, err := connectors.Collect(conn.Documents(ctx, connectors.StreamOpts{}))
			if err != nil {
				return err
			}
			if err := s.UpsertDocuments(ctx, batch); err != nil {
				return err
			}
			if err := s.TouchSourceSync(ctx, name, time.Now()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "connected local source %q at %s (%d docs indexed)\n", name, abs, len(batch))
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
