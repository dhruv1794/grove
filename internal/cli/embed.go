package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"

	"grove/internal/grove"
)

// newEmbedProgress mirrors newBuildProgress/newConnectProgress for the embed
// bar (per-source documents).
func newEmbedProgress() (func(grove.EmbedProgress), func()) {
	if gflags.JSON || !isTerminal(os.Stderr) {
		return nil, func() {}
	}
	var bar *progressbar.ProgressBar
	var source string
	report := func(p grove.EmbedProgress) {
		if bar == nil || p.Source != source {
			if bar != nil {
				bar.Finish()
			}
			source = p.Source
			bar = progressbar.NewOptions(p.Total,
				progressbar.OptionSetWriter(os.Stderr),
				progressbar.OptionSetDescription("embedding "+p.Source),
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

func newEmbedCmd() *cobra.Command {
	var model, source string
	var maxChars int
	var chunks bool

	cmd := &cobra.Command{
		Use:   "embed",
		Short: "Compute semantic embeddings for documents (enables semantic retrieval)",
		Long: "Embed computes a vector per document with a small embedding model " +
			"(default ollama/bge-m3) and stores it, so `grove ask` can " +
			"fuse semantic search with keyword and tree retrieval. Idempotent.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			g, err := openGrove(ctx)
			if err != nil {
				return err
			}
			defer g.Close()

			onProgress, finishProgress := newEmbedProgress()
			defer finishProgress()
			res, err := g.Embed(ctx, grove.EmbedOpts{
				Model: model, Source: source, MaxChars: maxChars, Chunks: chunks,
				OnProgress: onProgress,
			})
			if err != nil {
				return err
			}
			if gflags.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "embedded %d documents with %s\n", res.Embedded, res.Model)
			if res.NodesEmbedded > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "embedded %d tree-node summaries (collapsed-tree retrieval)\n", res.NodesEmbedded)
			}
			if res.ChunksEmbedded > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "embedded %d passage chunks (semantic chunking)\n", res.ChunksEmbedded)
			}
			if res.Skipped > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "skipped %d document(s) too long for the embedder\n", res.Skipped)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&chunks, "chunks", false, "also compute passage-level (semantic-chunk) vectors for `ask --chunk-embed`")
	cmd.Flags().StringVar(&model, "model", "", "embedding model provider/name (default GROVE_EMBED_MODEL or ollama/bge-m3)")
	cmd.Flags().StringVar(&source, "source", "", "embed only this source (default: all)")
	cmd.Flags().IntVar(&maxChars, "max-chars", 0, "cap doc chars sent to the embedder (default 2000; lower for tight-context models like mxbai)")
	return cmd
}
