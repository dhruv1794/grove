package grove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/query"
)

// AskOpts configures Ask.
type AskOpts struct {
	Query        string
	Model        string // "provider/name"; if empty, falls back to config [query].model
	Source       string
	MaxDepth     int
	MaxTokens    int
	RetrieveOnly bool
	Fast         bool // keyword/FTS retrieval, no LLM tree descent
	Prune        bool // binary LLM relevance filter after fusion (precision stage)
	Decompose    bool // split a multi-aspect question into per-aspect sub-queries
	Rerank       bool // graded LLM relevance reorder after fusion (precision-at-1)
	Retrievers   []string // restrict base retrievers (e.g. ["embed"]); empty = all
	MaxDocs      int      // cap on candidate docs returned; 0 → default 12
	Correct      bool     // CRAG: abstain when no retrieved doc scores above threshold
	CandidateWindow int   // fused candidates the rerank/prune stages consider; 0 → default 20
	ChunkEmbed      bool  // semantic retriever searches passage vectors → parent docs (needs `grove embed --chunks`)
	DebugScores     bool  // emit per-retriever ranked hits + raw scores in the result
}

// AskResult is a completed query.
type AskResult = query.Result

// Ask answers a question over the knowledge forest. The query model is
// resolved with precedence flag > GROVE_QUERY_MODEL > config [query].model.
//
// An LLM is needed for tree descent and for answer synthesis; --fast skips
// descent, and --retrieve-only skips synthesis, so `--fast --retrieve-only`
// resolves no chat model. The fast tier still embeds the query (FTS+embeddings
// ensemble) when the corpus is indexed — a local embed call, not a chat call.
func (g *Grove) Ask(ctx context.Context, opts AskOpts) (*AskResult, error) {
	if opts.Query == "" {
		return nil, core.NewError(core.KindMisuse,
			"empty query",
			"pass a question, e.g. grove ask \"what's our auth flow?\"")
	}
	var client llm.LLM
	// The fast retrieve-only path is model-free — unless --prune or --decompose
	// is set, each of which needs a model even there (the binary filter, the
	// sub-query split).
	needModel := !(opts.Fast && opts.RetrieveOnly) || opts.Prune || opts.Decompose || opts.Rerank || opts.Correct
	if needModel {
		model := opts.Model
		if model == "" {
			if cfg, err := core.LoadMergedConfig(g.configPath); err == nil {
				model = cfg.Query.Model
			}
		}
		if model == "" {
			return nil, core.NewError(core.KindMisuse,
				"no query model specified",
				"pass --model provider/name (e.g. ollama/llama3.1:8b), or set [query].model in config.toml")
		}
		spec, err := llm.ParseModel(model)
		if err != nil {
			return nil, err
		}
		if client, err = llm.New(spec, llm.OptionsFromEnv()); err != nil {
			return nil, err
		}
	}
	// Wire the semantic retriever only when the corpus is embedded — otherwise
	// the query embed call would be wasted and could fail if no embed model is
	// available. Best-effort: a missing embedder just disables semantic search.
	var embedder query.Embedder
	if n, err := g.store.CountEmbeddings(ctx, opts.Source); err == nil && n > 0 {
		// If the corpus was embedded with a different model than the query
		// embedder, the stored vectors have a different dimension and are
		// silently skipped in cosine search — semantic retrieval becomes a no-op.
		// Warn (don't fail) and skip the wasted query-embed call.
		want := embedModel("")
		if models, err := g.store.EmbeddingModels(ctx, opts.Source); err == nil && len(models) > 0 && !slices.Contains(models, want) {
			fmt.Fprintf(os.Stderr,
				"warning: stored embeddings use %v but the query embedder is %q — semantic retrieval disabled; run `grove embed` to rebuild with the current model\n",
				models, want)
		} else if e, err := newEmbedder(""); err == nil {
			embedder = e
		}
	}
	// --chunk-embed routes the semantic retriever at passage vectors. Without
	// any chunk embeddings that retriever returns nothing and silently drops out
	// of the ensemble — warn rather than degrade quietly.
	if opts.ChunkEmbed {
		if n, err := g.store.CountChunkEmbeddings(ctx, opts.Source); err == nil && n == 0 {
			fmt.Fprintln(os.Stderr,
				"warning: --chunk-embed is set but no passage vectors exist — semantic retrieval will contribute nothing; run `grove embed --chunks` first")
		}
	}

	res, err := query.Ask(ctx, query.Deps{Store: g.store, LLM: client, Embedder: embedder}, opts.Query, query.Options{
		Source:       opts.Source,
		MaxDepth:     opts.MaxDepth,
		MaxTokens:    opts.MaxTokens,
		RetrieveOnly: opts.RetrieveOnly,
		Fast:         opts.Fast,
		Prune:        opts.Prune,
		Decompose:    opts.Decompose,
		Rerank:       opts.Rerank,
		Retrievers:   opts.Retrievers,
		MaxDocs:      opts.MaxDocs,
		Correct:      opts.Correct,
		CandidateWindow: opts.CandidateWindow,
		ChunkEmbed:      opts.ChunkEmbed,
		DebugScores:     opts.DebugScores,
	})
	if err != nil {
		return nil, err
	}
	g.locateCitations(ctx, res)
	return res, nil
}

// locateCitations fills each citation's Location with a clickable target: an
// absolute filesystem path for filesystem-backed sources (local, obsidian).
// Best-effort — a source without a resolvable root just leaves Location empty,
// and the renderer falls back to source/source_ref.
func (g *Grove) locateCitations(ctx context.Context, res *AskResult) {
	if res == nil || len(res.Citations) == 0 {
		return
	}
	roots, err := g.SourceRoots(ctx, "")
	if err != nil {
		return
	}
	for i := range res.Citations {
		c := &res.Citations[i]
		root, ok := roots[c.Source]
		if !ok || c.SourceRef == "" {
			continue
		}
		if abs, err := filepath.Abs(filepath.Join(root, c.SourceRef)); err == nil {
			c.Location = abs
		}
	}
}
