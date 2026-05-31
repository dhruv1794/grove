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
	var model, source, mode string
	var retrievers []string
	var maxDepth, maxTokens, topK, candidates int
	var correct, chunkEmbed, debugScores bool
	var retrieveOnly, fast, prune, decompose, rerank bool

	cmd := &cobra.Command{
		Use:   "ask <query>",
		Short: "Ask a question over the knowledge forest",
		Long: "Ask retrieves the documents relevant to a question and synthesizes a " +
			"cited answer.\n\n" +
			"Modes (--mode):\n" +
			"  fast      keyword + embeddings, no LLM retrieval calls (instant)\n" +
			"  balanced  + decompose into sub-queries (default; best coverage, any model)\n" +
			"  quality   + graded rerank (best ranking; needs a strong model)\n" +
			"  deep      tree descent + decompose + rerank (explainable navigation)\n\n" +
			"The --fast/--decompose/--rerank/--prune flags override individual stages.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			st, err := grove.StagesForMode(mode)
			if err != nil {
				return err
			}
			// --fast is the spec's instant fallback: when set true it forces the
			// model-free path, overriding any richer mode.
			if cmd.Flags().Changed("fast") && fast {
				st = grove.Stages{Fast: true}
			}
			// Granular stage flags override the mode when explicitly passed.
			if cmd.Flags().Changed("decompose") {
				st.Decompose = decompose
			}
			if cmd.Flags().Changed("rerank") {
				st.Rerank = rerank
			}

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
				Fast:         st.Fast,
				Prune:        prune,
				Decompose:    st.Decompose,
				Rerank:       st.Rerank,
				Retrievers:   retrievers,
				MaxDocs:      topK,
				Correct:      correct,
				CandidateWindow: candidates,
				ChunkEmbed:      chunkEmbed,
				DebugScores:     debugScores,
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
	cmd.Flags().StringVar(&mode, "mode", "balanced", "retrieval mode: fast, balanced, quality, deep")
	cmd.Flags().StringSliceVar(&retrievers, "retrievers", nil, "restrict base retrievers for evaluation: fts, embed, tree (default: all)")
	cmd.Flags().StringVar(&source, "source", "", "restrict to one source")
	cmd.Flags().BoolVar(&retrieveOnly, "retrieve-only", false, "return matching sources without synthesizing an answer")
	cmd.Flags().BoolVar(&fast, "fast", false, "force the model-free keyword+embeddings path (overrides --mode)")
	cmd.Flags().BoolVar(&prune, "prune", false, "advanced: binary LLM relevance filter after fusion (needs a model)")
	cmd.Flags().BoolVar(&decompose, "decompose", false, "advanced: override --mode's sub-query decomposition (needs a model)")
	cmd.Flags().BoolVar(&rerank, "rerank", false, "advanced: override --mode's graded rerank (needs a strong model)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 0, "tree-descent depth limit (default 5)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "synthesis output token budget (default 1024)")
	cmd.Flags().IntVar(&topK, "top-k", 0, "cap on candidate docs returned (default 12; widens retriever depth for eval)")
	cmd.Flags().IntVar(&candidates, "candidates", 0, "fused candidates the rerank/prune stages score before trimming to top-k (default 20)")
	cmd.Flags().BoolVar(&correct, "correct", false, "CRAG: abstain when no retrieved doc scores above threshold (needs a strong model)")
	cmd.Flags().BoolVar(&chunkEmbed, "chunk-embed", false, "semantic retriever searches passage vectors → parent docs (needs `grove embed --chunks`)")
	cmd.Flags().BoolVar(&debugScores, "debug-scores", false, "include per-retriever ranked hits + raw bm25/cosine scores in --json output")
	return cmd
}

func renderAskResult(cmd *cobra.Command, r *grove.AskResult, retrieveOnly bool) {
	out := cmd.OutOrStdout()
	if !retrieveOnly {
		fmt.Fprintln(out, r.Answer)
		fmt.Fprintln(out)
	}
	cited, showAll, suppress := grove.CitationDisplay(r.Answer, retrieveOnly, r.Abstained)
	if len(r.Citations) > 0 && !suppress {
		fmt.Fprintln(out, "Sources:")
		for _, c := range r.Citations {
			if !showAll && !cited[c.N] {
				continue
			}
			title := c.Title
			if title == "" {
				title = c.SourceRef
			}
			marker := ""
			if c.CrossLink {
				marker = " (cross-link)"
			}
			// Location (absolute path / URL) is the clickable target most
			// terminals linkify; fall back to source/source_ref when unset.
			loc := c.Location
			if loc == "" {
				loc = c.Source + "/" + c.SourceRef
			}
			fmt.Fprintf(out, "  [%d] %s%s\n      %s\n", c.N, title, marker, loc)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "%d llm calls · %d in / %d out tokens · %s\n",
		r.Cost.Calls, r.Cost.Usage.PromptTokens, r.Cost.Usage.CompletionTokens, costStr(r.Cost))
}
