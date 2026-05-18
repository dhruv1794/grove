package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"grove/internal/grove"
)

func newAskCmd() *cobra.Command {
	var model, source string
	var maxDepth, maxTokens int
	var retrieveOnly, fast bool

	cmd := &cobra.Command{
		Use:   "ask <query>",
		Short: "Ask a question over the knowledge forest",
		Long: "Ask selects the trees likely to hold the answer, descends each " +
			"tree-of-contents to the relevant leaf documents, and synthesizes a " +
			"cited answer.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			res, err := g.Ask(ctx, grove.AskOpts{
				Query:        strings.Join(args, " "),
				Model:        model,
				Source:       source,
				MaxDepth:     maxDepth,
				MaxTokens:    maxTokens,
				RetrieveOnly: retrieveOnly,
				Fast:         fast,
			})
			if err != nil {
				return err
			}

			if gflags.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			renderAskResult(cmd, res, retrieveOnly)
			return nil
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "query model as provider/name (default: config [query].model)")
	cmd.Flags().StringVar(&source, "source", "", "restrict to one source")
	cmd.Flags().BoolVar(&retrieveOnly, "retrieve-only", false, "return matching sources without synthesizing an answer")
	cmd.Flags().BoolVar(&fast, "fast", false, "keyword/full-text retrieval, no LLM tree descent (with --retrieve-only: no LLM call at all)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 0, "tree-descent depth limit (default 5)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "synthesis output token budget (default 1024)")
	return cmd
}

func renderAskResult(cmd *cobra.Command, r *grove.AskResult, retrieveOnly bool) {
	out := cmd.OutOrStdout()
	if !retrieveOnly {
		fmt.Fprintln(out, r.Answer)
		fmt.Fprintln(out)
	}
	if len(r.Citations) > 0 {
		fmt.Fprintln(out, "Sources:")
		for _, c := range r.Citations {
			title := c.Title
			if title == "" {
				title = c.SourceRef
			}
			fmt.Fprintf(out, "  [%d] %s — %s/%s\n", c.N, title, c.Source, c.SourceRef)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "%d llm calls · %d in / %d out tokens · %s\n",
		r.Cost.Calls, r.Cost.Usage.PromptTokens, r.Cost.Usage.CompletionTokens, costStr(r.Cost))
}
