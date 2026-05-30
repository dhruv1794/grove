package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"

	"grove/internal/grove"
)

// renderConnectResult prints the post-connect summary: doc counts, an
// already-connected hint when applicable, and (if --and-sync) the sync tail.
// Same shape across every connect subcommand and the REPL.
func renderConnectResult(out io.Writer, srcType string, res *grove.ConnectResult) {
	noun := "docs"
	if srcType == "confluence" {
		noun = "pages"
	}
	switch {
	case res.Existed && res.Refetched > 0:
		fmt.Fprintf(out, "updated %s source %q: %d %s changed/new, %d unchanged (skipped)\n",
			srcType, res.Name, res.Refetched, noun, res.Skipped)
	case res.Existed:
		fmt.Fprintf(out, "updated %s source %q: nothing new — every %s already in store\n",
			srcType, res.Name, strings.TrimSuffix(noun, "s"))
	default:
		fmt.Fprintf(out, "connected %s source %q (%d %s indexed)\n", srcType, res.Name, res.DocCount, noun)
	}
	if res.Existed && res.Sync == nil {
		fmt.Fprintln(out, "  hint: connect doesn't detect deletions or rebuild — run `grove sync` (or re-run with --and-sync) to reconcile + rebuild.")
	}
	if res.Sync != nil {
		for _, s := range res.Sync.Sources {
			fmt.Fprintf(out, "  sync %s: %d new, %d changed, %d deleted\n", s.Source, s.Created, s.Modified, s.Deleted)
		}
		if b := res.Sync.Build; b != nil {
			fmt.Fprintf(out, "  rebuilt %d trees, %d nodes (%d from cache, %d generated)\n",
				b.Trees, b.Nodes, b.CacheHits, b.CacheMiss)
		}
	}
}

// newConnectProgress returns a progress callback for connect and a finish func
// to flush the bar. nil callback (no bar) under --json or a non-terminal stderr.
// Mirrors newBuildProgress's shape.
func newConnectProgress() (func(grove.IngestProgress), func()) {
	if gflags.JSON || !isTerminal(os.Stderr) {
		return nil, func() {}
	}
	var bar *progressbar.ProgressBar
	var source string
	report := func(p grove.IngestProgress) {
		if bar == nil || p.Source != source {
			if bar != nil {
				bar.Finish()
			}
			source = p.Source
			bar = progressbar.NewOptions(p.Total,
				progressbar.OptionSetWriter(os.Stderr),
				progressbar.OptionSetDescription("fetching "+p.Source),
				progressbar.OptionSetPredictTime(false),
				progressbar.OptionShowCount(),
				progressbar.OptionClearOnFinish(),
				progressbar.OptionThrottle(65*time.Millisecond),
			)
		}
		_ = bar.Set(p.Done)
	}
	return report, func() {
		if bar != nil {
			bar.Finish()
		}
	}
}

func newConnectCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "connect <source-type> [args]",
		Short: "Connect a source to the workspace",
	}
	c.AddCommand(newConnectLocalCmd())
	c.AddCommand(newConnectObsidianCmd())
	c.AddCommand(newConnectGDriveCmd())
	c.AddCommand(newConnectConfluenceCmd())
	return c
}

func newConnectConfluenceCmd() *cobra.Command {
	var name, collection, spaceKey, site string
	var andSync bool

	cmd := &cobra.Command{
		Use:   "confluence",
		Short: "Connect a Confluence Cloud site",
		Long: `Connect a Confluence Cloud site over OAuth 2.0 (3LO).

On first run, grove opens your browser to authorize read-only access to your
Confluence content. The OAuth token is encrypted at rest under the workspace
auth/ directory and reused on subsequent runs.

Requires an Atlassian OAuth 2.0 (3LO) app registered at
developer.atlassian.com. Set:
  GROVE_CONFLUENCE_CLIENT_ID
  GROVE_CONFLUENCE_CLIENT_SECRET

If the authorizing account has access to multiple Confluence sites, pass
--site to disambiguate (matches site name or URL substring).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			onProgress, finishProgress := newConnectProgress()
			defer finishProgress()
			res, err := g.ConnectConfluence(ctx, grove.ConnectOpts{
				Name:       name,
				Collection: collection,
				AndSync:    andSync,
				OnProgress: onProgress,
			}, spaceKey, site)
			if err != nil {
				return err
			}
			finishProgress()
			renderConnectResult(cmd.OutOrStdout(), "confluence", res)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "source name (default \"confluence\" or \"confluence-<space>\")")
	cmd.Flags().StringVar(&collection, "collection", "", "logical grouping name")
	cmd.Flags().StringVar(&spaceKey, "space", "", "scope ingest to a single Confluence space key")
	cmd.Flags().StringVar(&site, "site", "", "site name or URL substring (when the account has multiple sites)")
	cmd.Flags().BoolVar(&andSync, "and-sync", false, "after ingest, run `grove sync` to detect deletions + rebuild")
	return cmd
}

func newConnectGDriveCmd() *cobra.Command {
	var name, collection, folderID string
	var andSync bool

	cmd := &cobra.Command{
		Use:   "gdrive",
		Short: "Connect a Google Drive account",
		Long: `Connect a Google Drive account.

On first run, grove opens your browser to authorize read-only Drive access. The
OAuth token is encrypted at rest under the workspace auth/ directory and reused
on subsequent runs.

Requires a Google Cloud "Desktop app" OAuth client; set
GROVE_GDRIVE_CLIENT_ID and GROVE_GDRIVE_CLIENT_SECRET before running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			onProgress, finishProgress := newConnectProgress()
			defer finishProgress()
			res, err := g.ConnectGDrive(ctx, grove.ConnectOpts{
				Name:       name,
				Collection: collection,
				FolderID:   folderID,
				AndSync:    andSync,
				OnProgress: onProgress,
			})
			if err != nil {
				return err
			}
			finishProgress()
			renderConnectResult(cmd.OutOrStdout(), "gdrive", res)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "source name (default \"gdrive\")")
	cmd.Flags().StringVar(&collection, "collection", "", "logical grouping name")
	cmd.Flags().StringVar(&folderID, "folder", "", "scope ingest to a Drive folder ID")
	cmd.Flags().BoolVar(&andSync, "and-sync", false, "after ingest, run `grove sync` to detect deletions + rebuild")
	return cmd
}

func newConnectLocalCmd() *cobra.Command {
	var name, collection string
	var includes, excludes []string
	var maxSizeMB int64
	var andSync bool

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

			onProgress, finishProgress := newConnectProgress()
			defer finishProgress()
			res, err := g.ConnectLocal(ctx, grove.ConnectOpts{
				Path:       args[0],
				Name:       name,
				Collection: collection,
				Include:    includes,
				Exclude:    excludes,
				MaxSizeMB:  maxSizeMB,
				AndSync:    andSync,
				OnProgress: onProgress,
			})
			if err != nil {
				return err
			}
			finishProgress()
			renderConnectResult(cmd.OutOrStdout(), "local", res)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "source name (default local-<basename>)")
	cmd.Flags().StringVar(&collection, "collection", "", "logical grouping name")
	cmd.Flags().StringSliceVar(&includes, "include", nil, "glob patterns to include")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "glob patterns to exclude")
	cmd.Flags().Int64Var(&maxSizeMB, "max-size", 0, "skip files larger than N megabytes (0 = unlimited)")
	cmd.Flags().BoolVar(&andSync, "and-sync", false, "after ingest, run `grove sync` to detect deletions + rebuild")
	return cmd
}

func newConnectObsidianCmd() *cobra.Command {
	var name, collection string
	var includes, excludes []string
	var maxSizeMB int64
	var andSync bool

	cmd := &cobra.Command{
		Use:   "obsidian <vault-path>",
		Short: "Connect an Obsidian vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			onProgress, finishProgress := newConnectProgress()
			defer finishProgress()
			res, err := g.ConnectObsidian(ctx, grove.ConnectOpts{
				Path:       args[0],
				Name:       name,
				Collection: collection,
				Include:    includes,
				Exclude:    excludes,
				MaxSizeMB:  maxSizeMB,
				AndSync:    andSync,
				OnProgress: onProgress,
			})
			if err != nil {
				return err
			}
			finishProgress()
			renderConnectResult(cmd.OutOrStdout(), "obsidian", res)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "source name (default obsidian-<basename>)")
	cmd.Flags().StringVar(&collection, "collection", "", "logical grouping name")
	cmd.Flags().StringSliceVar(&includes, "include", nil, "glob patterns to include")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "glob patterns to exclude")
	cmd.Flags().Int64Var(&maxSizeMB, "max-size", 0, "skip files larger than N megabytes (0 = unlimited)")
	cmd.Flags().BoolVar(&andSync, "and-sync", false, "after ingest, run `grove sync` to detect deletions + rebuild")
	return cmd
}
