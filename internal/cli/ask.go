package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"grove/internal/core"
	"grove/internal/grove"
)

// citationMarkerRe matches a bracketed run of comma/space-separated numbers so
// that both the prompt's instructed form ([2][5]) and the common deviation
// [1, 2] are captured. The inner numbers are split out by citedNums.
var citationMarkerRe = regexp.MustCompile(`\[([\d,\s]+)\]`)

// citedNums returns the citation numbers actually referenced in an answer via
// [n] markers, including comma-joined forms like [1, 2].
func citedNums(answer string) map[int]bool {
	out := map[int]bool{}
	for _, m := range citationMarkerRe.FindAllStringSubmatch(answer, -1) {
		for _, f := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }) {
			if n, err := strconv.Atoi(f); err == nil {
				out[n] = true
			}
		}
	}
	return out
}

// citationDisplay decides which retrieved citations to show. With a synthesized
// answer, show only those the answer cites via [n]; if it cites none (e.g. the
// answer says the topic isn't in the corpus), suppress the list entirely — the
// retrieved candidates are just noise. In retrieve-only mode there is no answer
// to cite from, so show every retrieved doc (that's the point of the mode).
func citationDisplay(answer string, retrieveOnly, abstained bool) (cited map[int]bool, showAll, suppress bool) {
	if retrieveOnly {
		return nil, true, false
	}
	// On abstention the answer carries no [n] markers but explicitly promises a
	// list of closest matches — show them all instead of suppressing.
	if abstained {
		return nil, true, false
	}
	cited = citedNums(answer)
	if len(cited) == 0 {
		return nil, false, true
	}
	return cited, false, false
}

// askStages is the set of retrieval stages a --mode selects. The CLI (the
// transport adapter) owns this mode→stages mapping; query.Options stays
// primitive booleans so the core library has no notion of "modes".
type askStages struct {
	fast      bool // no LLM tree descent (FTS + embeddings only)
	decompose bool // split a multi-aspect question into sub-queries
	rerank    bool // graded LLM reorder of fused candidates
}

// stagesForMode maps a mode name to its stages. Defaults derive from the
// retrieval benchmark (documents/benchmark-findings.md):
//   - fast: instant, model-free with --retrieve-only.
//   - balanced (default): + decompose — big coverage win, model-robust (8b ok).
//   - quality: + rerank — best ranking, but needs a STRONG model (8b is harmful).
//   - deep: tree descent + decompose + rerank — explainable navigation path.
func stagesForMode(mode string) (askStages, error) {
	switch mode {
	case "fast":
		return askStages{fast: true}, nil
	case "balanced":
		return askStages{fast: true, decompose: true}, nil
	case "quality":
		return askStages{fast: true, decompose: true, rerank: true}, nil
	case "deep":
		return askStages{decompose: true, rerank: true}, nil
	default:
		return askStages{}, core.NewError(core.KindMisuse,
			fmt.Sprintf("unknown mode %q", mode),
			"use one of: fast, balanced, quality, deep")
	}
}

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

			st, err := stagesForMode(mode)
			if err != nil {
				return err
			}
			// --fast is the spec's instant fallback: when set true it forces the
			// model-free path, overriding any richer mode.
			if cmd.Flags().Changed("fast") && fast {
				st = askStages{fast: true}
			}
			// Granular stage flags override the mode when explicitly passed.
			if cmd.Flags().Changed("decompose") {
				st.decompose = decompose
			}
			if cmd.Flags().Changed("rerank") {
				st.rerank = rerank
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
				Fast:         st.fast,
				Prune:        prune,
				Decompose:    st.decompose,
				Rerank:       st.rerank,
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
	cited, showAll, suppress := citationDisplay(r.Answer, retrieveOnly, r.Abstained)
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
