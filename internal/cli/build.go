package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"

	"grove/internal/grove"
	"grove/internal/llm"
)

func newBuildCmd() *cobra.Command {
	var model, source, compress string
	var rebuild, dryRun, noGroup bool
	var concurrency int

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the knowledge forest over connected sources",
		Long: "Build indexes each connected source into a tree-of-contents, " +
			"generating a title and summary for every node with the build model. " +
			"Per-node output is cached, so an unchanged rebuild makes no model calls.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			onProgress, finishProgress := newBuildProgress(dryRun)
			res, err := g.Build(ctx, grove.BuildOpts{
				Model:       model,
				Source:      source,
				Rebuild:     rebuild,
				DryRun:      dryRun,
				NoGroup:     noGroup,
				Concurrency: concurrency,
				Compress:    compress,
				OnProgress:  onProgress,
			})
			finishProgress()
			if err != nil {
				return err
			}

			if gflags.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			renderBuildResult(cmd, res, dryRun)
			return nil
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "build model as provider/name (default: config [build].model)")
	cmd.Flags().StringVar(&source, "source", "", "build only this source (default: all)")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "ignore the node cache; re-call the model for every node")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report the work without calling the model or writing")
	cmd.Flags().BoolVar(&noGroup, "no-group", false, "mirror folder structure only; skip LLM topic grouping of flat folders")
	cmd.Flags().IntVar(&concurrency, "concurrency", 4, "max in-flight model calls (1 = sequential)")
	cmd.Flags().StringVar(&compress, "compress", "", "compress doc text before summarization: none|safe|aggressive (default: config [build].compress)")
	return cmd
}

// newBuildProgress returns a per-source progress callback for grove build and
// a finish func to flush the final bar. It returns a nil callback when progress
// should be suppressed: --json (machine output), --dry-run (no real work), or a
// non-terminal stderr (piped/redirected). A new bar is started per source.
func newBuildProgress(dryRun bool) (func(grove.BuildProgress), func()) {
	if gflags.JSON || dryRun || !isTerminal(os.Stderr) {
		return nil, func() {}
	}
	var bar *progressbar.ProgressBar
	var source string
	report := func(p grove.BuildProgress) {
		if bar == nil || p.Source != source {
			if bar != nil {
				bar.Finish()
			}
			source = p.Source
			bar = progressbar.NewOptions(p.Total,
				progressbar.OptionSetWriter(os.Stderr),
				progressbar.OptionSetDescription("building "+p.Source),
				progressbar.OptionSetPredictTime(false),
				progressbar.OptionShowCount(),
				progressbar.OptionClearOnFinish(),
				// Coalesce redraws; without a throttle the bar repaints on every
				// node. The final state still renders at Done == Total.
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

func renderBuildResult(cmd *cobra.Command, r *grove.BuildResult, dryRun bool) {
	out := cmd.OutOrStdout()
	if dryRun {
		fmt.Fprintln(out, "dry run — no model calls made, no changes written")
		fmt.Fprintf(out, "would build %s, %s (%d cached, %d to generate)\n",
			plural(r.Trees, "tree"), plural(r.Nodes, "node"), r.CacheHits, r.CacheMiss)
		return
	}
	fmt.Fprintf(out, "built %s, %s (%d from cache, %d generated)\n",
		plural(r.Trees, "tree"), plural(r.Nodes, "node"), r.CacheHits, r.CacheMiss)
	renderBuildStats(out, r)
}

// renderBuildStats prints the grouping/cross-link/model/cost/elapsed lines
// shared by `grove build` and the rebuild tail of `grove sync`.
func renderBuildStats(out io.Writer, r *grove.BuildResult) {
	if r.Groups > 0 {
		fmt.Fprintf(out, "groups:  %s from flat folders\n", plural(r.Groups, "topic"))
	}
	if r.CrossLinks > 0 {
		fmt.Fprintf(out, "links:   %s\n", plural(r.CrossLinks, "cross-link"))
	}
	if r.Compressed > 0 {
		fmt.Fprintf(out, "compress: %s, ~%d input tokens saved\n", plural(r.Compressed, "doc"), r.TokensSaved)
	}
	fmt.Fprintf(out, "model:   %s\n", r.Model)
	fmt.Fprintf(out, "llm:     %d calls · %d in / %d out tokens · %s\n",
		r.Tally.Calls, r.Tally.Usage.PromptTokens, r.Tally.Usage.CompletionTokens, costStr(r.Tally))
	fmt.Fprintf(out, "elapsed: %s\n", r.Elapsed)
}

func costStr(t llm.Tally) string { return t.USDString() }

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
