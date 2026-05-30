package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"grove/internal/grove"
)

// watchDebounce coalesces a burst of filesystem events (an editor save often
// fires several) into one sync.
const watchDebounce = 400 * time.Millisecond

// ignoredWatchDirs are not descended when registering watches — noise and
// churn that never affects the index.
var ignoredWatchDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true,
	".venv": true, ".grove": true, ".obsidian": true, ".trash": true,
}

func newSyncCmd() *cobra.Command {
	var model, source string
	var force, watch bool
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Re-index sources, rebuilding only what changed",
		Long: "Sync re-walks connected sources, applies document creations, " +
			"content changes, and deletions, then rebuilds the forest. The build's " +
			"per-node cache regenerates only the branches whose documents changed, " +
			"so an unchanged sync makes no model calls.\n\n" +
			"With --watch it re-syncs on filesystem changes; with --interval it " +
			"re-syncs on a timer. Both run until interrupted (Ctrl-C).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if watch && interval > 0 {
				return fmt.Errorf("use either --watch or --interval, not both")
			}
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			opts := grove.SyncOpts{Model: model, Source: source, Force: force}

			switch {
			case watch:
				return runSyncWatch(ctx, cmd, g, opts)
			case interval > 0:
				return runSyncInterval(ctx, cmd, g, opts, interval)
			default:
				// One-shot: a progress bar is fine; long-running modes skip it to
				// keep their streaming output clean.
				onProgress, finish := newBuildProgress(false)
				opts.OnProgress = onProgress
				res, err := g.Sync(ctx, opts)
				finish()
				if err != nil {
					return err
				}
				return renderSync(cmd, res)
			}
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "build model as provider/name (default: config [build].model)")
	cmd.Flags().StringVar(&source, "source", "", "sync only this source (default: all)")
	cmd.Flags().BoolVar(&force, "force", false, "rebuild the forest even if nothing changed, ignoring the node cache")
	cmd.Flags().BoolVar(&watch, "watch", false, "re-sync on filesystem changes until interrupted")
	cmd.Flags().DurationVar(&interval, "interval", 0, "re-sync on this interval until interrupted (e.g. 30s)")
	return cmd
}

// runSyncOnce runs one sync and renders it, returning any error. Used directly
// for the one-shot path and per-iteration in watch/interval loops.
func runSyncOnce(ctx context.Context, cmd *cobra.Command, g *grove.Grove, opts grove.SyncOpts) error {
	res, err := g.Sync(ctx, opts)
	if err != nil {
		return err
	}
	return renderSync(cmd, res)
}

// runSyncInterval re-syncs on a timer until the context is cancelled (Ctrl-C).
// A failed iteration is reported but does not stop the loop.
func runSyncInterval(ctx context.Context, cmd *cobra.Command, g *grove.Grove, opts grove.SyncOpts, d time.Duration) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	if err := runSyncOnce(ctx, cmd, g, opts); err != nil {
		fmt.Fprintf(os.Stderr, "grove: sync failed: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "syncing every %s (Ctrl-C to stop)\n", d)

	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := runSyncOnce(ctx, cmd, g, opts); err != nil {
				fmt.Fprintf(os.Stderr, "grove: sync failed: %v\n", err)
			}
		}
	}
}

// runSyncWatch re-syncs whenever a watched source directory changes, until the
// context is cancelled (Ctrl-C). Events are debounced so a burst of writes
// triggers a single sync. A failed iteration is reported but does not stop.
func runSyncWatch(ctx context.Context, cmd *cobra.Command, g *grove.Grove, opts grove.SyncOpts) error {
	roots, err := g.SourceRoots(ctx, opts.Source)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return fmt.Errorf("no filesystem-backed sources to watch")
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	for _, root := range roots {
		if err := addWatchRecursive(w, root); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	if err := runSyncOnce(ctx, cmd, g, opts); err != nil {
		fmt.Fprintf(os.Stderr, "grove: sync failed: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "watching %d source(s) for changes (Ctrl-C to stop)\n", len(roots))

	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-w.Events:
			// A new subdirectory must be watched too, or notes added under it
			// are missed.
			if event.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
					_ = addWatchRecursive(w, event.Name)
				}
			}
			debounce = time.After(watchDebounce)
		case err := <-w.Errors:
			fmt.Fprintf(os.Stderr, "grove: watch error: %v\n", err)
		case <-debounce:
			debounce = nil
			if err := runSyncOnce(ctx, cmd, g, opts); err != nil {
				fmt.Fprintf(os.Stderr, "grove: sync failed: %v\n", err)
			}
		}
	}
}

// addWatchRecursive registers root and every subdirectory under it (fsnotify
// watches directories, not trees), skipping ignored dirs. Unreadable entries
// are skipped rather than aborting the walk.
func addWatchRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if ignoredWatchDirs[d.Name()] {
			return filepath.SkipDir
		}
		_ = w.Add(path)
		return nil
	})
}

func renderSync(cmd *cobra.Command, r *grove.SyncResult) error {
	if gflags.JSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
	}
	out := cmd.OutOrStdout()
	for _, s := range r.Sources {
		fmt.Fprintf(out, "%s: %d new, %d changed, %d deleted\n", s.Source, s.Created, s.Modified, s.Deleted)
	}
	if r.Build == nil {
		fmt.Fprintln(out, "up to date — no rebuild needed")
		return nil
	}
	fmt.Fprintf(out, "rebuilt %s, %s (%d from cache, %d generated)\n",
		plural(r.Build.Trees, "tree"), plural(r.Build.Nodes, "node"), r.Build.CacheHits, r.Build.CacheMiss)
	renderBuildStats(out, r.Build)
	return nil
}
