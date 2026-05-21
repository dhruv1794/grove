package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"grove/internal/grove"
	"grove/internal/llm"
)

func newBuildCmd() *cobra.Command {
	var model, source string
	var rebuild, dryRun bool

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

			res, err := g.Build(ctx, grove.BuildOpts{
				Model:   model,
				Source:  source,
				Rebuild: rebuild,
				DryRun:  dryRun,
			})
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
	return cmd
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
	if r.CrossLinks > 0 {
		fmt.Fprintf(out, "links:   %s\n", plural(r.CrossLinks, "cross-link"))
	}
	fmt.Fprintf(out, "model:   %s\n", r.Model)
	fmt.Fprintf(out, "llm:     %d calls · %d in / %d out tokens · %s\n",
		r.Tally.Calls, r.Tally.Usage.PromptTokens, r.Tally.Usage.CompletionTokens, costStr(r.Tally))
	fmt.Fprintf(out, "elapsed: %s\n", r.Elapsed)
}

func costStr(t llm.Tally) string {
	if t.USD == 0 && !t.Estimated {
		return "$0.00 (local model)"
	}
	s := fmt.Sprintf("$%.4f", t.USD)
	if t.Estimated {
		s += " (estimated)"
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
