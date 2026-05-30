package grove

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"grove/internal/core"
	"grove/internal/llm"
)

// defaultEmbedModel is the local embedding model grove uses for semantic
// retrieval unless overridden. bge-m3 (1024-dim, 8192-token context) measured
// +16–19% recall over nomic-embed-text on BEIR FiQA @30K at the same local
// cost (see benchmark-findings.md). NOTE: the dimension differs from nomic
// (768→1024), so a workspace embedded with the old default must be re-embedded
// (`grove embed` overwrites in place); SearchByVector skips dimension mismatches.
const defaultEmbedModel = "ollama/bge-m3"

// embedModel resolves the embedding model: GROVE_EMBED_MODEL, else default.
func embedModel(override string) string {
	if override != "" {
		return override
	}
	if v := os.Getenv("GROVE_EMBED_MODEL"); v != "" {
		return v
	}
	return defaultEmbedModel
}

// newEmbedder constructs an embedder for the resolved model (no network call).
func newEmbedder(override string) (*llm.Embedder, error) {
	spec, err := llm.ParseModel(embedModel(override))
	if err != nil {
		return nil, err
	}
	return llm.NewEmbedder(spec, llm.OptionsFromEnv())
}

// EmbedOpts configures Embed.
type EmbedOpts struct {
	Model    string // embedding model "provider/name"; default GROVE_EMBED_MODEL or bge-m3
	Source   string // embed only this source; "" embeds all
	MaxChars int    // cap on doc chars sent to the embedder; 0 → default 2000 (lower for tight-context models like mxbai)
	Chunks   bool   // also compute passage-level (semantic-chunk) vectors for --chunk-embed retrieval

	// OnProgress, if set, reports per-source doc-embed progress so an adapter
	// (the CLI) can render a bar. Node and chunk embeds are not yet surfaced.
	OnProgress func(EmbedProgress)
}

// EmbedProgress reports per-source embed progress (documents only for now).
type EmbedProgress struct {
	Source string
	Total  int
	Done   int
}

// EmbedResult reports an embed run.
type EmbedResult struct {
	Model         string `json:"model"`
	Embedded       int `json:"embedded"`
	Skipped        int `json:"skipped"`         // docs too long for the embedder even truncated
	NodesEmbedded  int `json:"nodes_embedded"`  // internal tree-node summaries embedded (collapsed-tree retrieval)
	ChunksEmbedded int `json:"chunks_embedded"` // passage vectors embedded (--chunks; semantic chunking)
}

const embedBatch = 32 // docs per embedding request

// embedFallbackChars is the hard char cap a doc is truncated to when it
// overflows the embedder's context even at the configured MaxChars — a last
// resort before skipping it, for tight-context models (e.g. 512-token).
const embedFallbackChars = 800

// Embed computes and stores a vector for every document (title + body) so the
// semantic retriever can run. Idempotent: re-embedding overwrites in place.
func (g *Grove) Embed(ctx context.Context, opts EmbedOpts) (*EmbedResult, error) {
	emb, err := newEmbedder(opts.Model)
	if err != nil {
		return nil, err
	}
	model := emb.Model().String()

	sources, err := g.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	maxChars := embedMaxChars
	if opts.MaxChars > 0 {
		maxChars = opts.MaxChars
	}
	res := &EmbedResult{Model: model}
	for _, src := range sources {
		if opts.Source != "" && src.Name != opts.Source {
			continue
		}
		metas, err := g.store.ListDocumentMetaBySource(ctx, src.Name)
		if err != nil {
			return nil, err
		}
		if opts.OnProgress != nil {
			opts.OnProgress(EmbedProgress{Source: src.Name, Total: len(metas), Done: 0})
		}
		for start := 0; start < len(metas); start += embedBatch {
			end := min(start+embedBatch, len(metas))
			batch := metas[start:end]
			texts := make([]string, len(batch))
			for i, d := range batch {
				body, err := g.store.GetDocumentContent(ctx, d.Hash)
				if err != nil {
					return nil, err
				}
				texts[i] = d.Title + "\n\n" + core.TruncateRunes(body, maxChars)
			}
			vecs, err := emb.Embed(ctx, texts)
			if errors.Is(err, llm.ErrContextLength) {
				// One overlong doc fails the whole batch — retry each doc alone so
				// the rest still embed, truncating/skipping only the offender.
				embedded, skipped, ferr := g.embedIndividually(ctx, emb, src.Name, model, batch, texts)
				if ferr != nil {
					return nil, ferr
				}
				res.Embedded += embedded
				res.Skipped += skipped
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("embed source %q: %w", src.Name, err)
			}
			for i, v := range vecs {
				if err := g.store.UpsertEmbedding(ctx, batch[i].ID, src.Name, model, v); err != nil {
					return nil, err
				}
			}
			res.Embedded += len(batch)
			if opts.OnProgress != nil {
				opts.OnProgress(EmbedProgress{Source: src.Name, Total: len(metas), Done: end})
			}
		}
		if err := g.embedNodes(ctx, emb, src.Name, model, maxChars, res); err != nil {
			return nil, err
		}
		if opts.Chunks {
			if err := g.embedChunks(ctx, emb, src.Name, model, maxChars, res); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

// embedChunks computes passage-level vectors: each document is split into
// semantic chunks, each chunk embedded and stored against its parent doc, for
// the --chunk-embed retriever. Replaces a doc's prior chunk vectors so a re-embed
// reflects the current chunking. Idempotent. A passage that overflows the
// embedder's context is skipped (counted via res.Skipped).
func (g *Grove) embedChunks(ctx context.Context, emb *llm.Embedder, source, model string, maxChars int, res *EmbedResult) error {
	metas, err := g.store.ListDocumentMetaBySource(ctx, source)
	if err != nil {
		return err
	}
	for _, d := range metas {
		body, err := g.store.GetDocumentContent(ctx, d.Hash)
		if err != nil {
			return err
		}
		chunks, err := semanticChunks(ctx, emb, d.ID, d.Title+"\n\n"+body)
		if err != nil {
			return fmt.Errorf("chunk doc %s: %w", d.ID, err)
		}
		if err := g.store.DeleteChunkEmbeddingsByDoc(ctx, d.ID); err != nil {
			return err
		}
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = core.TruncateRunes(c, maxChars)
		}
		vecs, err := emb.Embed(ctx, texts)
		if errors.Is(err, llm.ErrContextLength) {
			fmt.Fprintf(os.Stderr, "warning: skipping %d chunks of doc %s — embedder context exceeded\n", len(texts), d.ID)
			res.Skipped += len(texts)
			continue
		}
		if err != nil {
			return fmt.Errorf("embed chunks of %s: %w", d.ID, err)
		}
		for i, v := range vecs {
			if err := g.store.UpsertChunkEmbedding(ctx, d.ID, i, source, model, v); err != nil {
				return err
			}
		}
		res.ChunksEmbedded += len(vecs)
	}
	return nil
}

// embedNodes embeds the summaries of internal tree nodes for a source, enabling
// collapsed-tree retrieval. Leaf nodes are skipped — their documents are already
// embedded; only the abstractive summary nodes add a new signal. Node summaries
// are short, so a single batched call per tree suffices; a context overflow
// (unexpected) skips the batch with a warning rather than aborting.
func (g *Grove) embedNodes(ctx context.Context, emb *llm.Embedder, source, model string, maxChars int, res *EmbedResult) error {
	trees, err := g.store.ListTrees(ctx)
	if err != nil {
		return err
	}
	for _, t := range trees {
		if t.Source != source {
			continue
		}
		nodes, err := g.store.ListNodesByTree(ctx, t.ID)
		if err != nil {
			return err
		}
		isParent := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			if n.ParentID != "" {
				isParent[n.ParentID] = true
			}
		}
		var ids, texts []string
		for _, n := range nodes {
			if !isParent[n.ID] {
				continue // leaf — its docs are embedded already
			}
			text := nodeEmbedText(n, maxChars)
			if text == "" {
				continue
			}
			ids = append(ids, n.ID)
			texts = append(texts, text)
		}
		for start := 0; start < len(ids); start += embedBatch {
			end := min(start+embedBatch, len(ids))
			vecs, err := emb.Embed(ctx, texts[start:end])
			if errors.Is(err, llm.ErrContextLength) {
				fmt.Fprintf(os.Stderr, "warning: skipping %d node summaries in tree %s — embedder context exceeded\n", end-start, t.ID)
				continue
			}
			if err != nil {
				return fmt.Errorf("embed node summaries for tree %q: %w", t.ID, err)
			}
			for i, v := range vecs {
				if err := g.store.UpsertNodeEmbedding(ctx, ids[start+i], t.ID, source, model, v); err != nil {
					return err
				}
			}
			res.NodesEmbedded += end - start
		}
	}
	return nil
}

// nodeEmbedText is the "title + summary" text embedded for an internal node,
// read from its payload (the source of truth for the summary) with a fallback
// to the SQLite title. Truncated to the same char cap as documents.
func nodeEmbedText(n core.Node, maxChars int) string {
	title, summary := n.Title, ""
	if p, err := core.ReadNodePayload(n.PayloadPath); err == nil && p != nil {
		if p.Title != "" {
			title = p.Title
		}
		summary = p.Summary
	}
	text := strings.TrimSpace(title)
	if summary != "" {
		text += "\n\n" + summary
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return core.TruncateRunes(text, maxChars)
}

// embedIndividually re-embeds a batch one document at a time after the batch hit
// the model's context limit. A doc that still overflows is retried once at a
// hard char cap, then skipped with a warning — so a single overlong doc can't
// abort embedding thousands of others.
func (g *Grove) embedIndividually(ctx context.Context, emb *llm.Embedder, source, model string, batch []core.Document, texts []string) (embedded, skipped int, err error) {
	for i, text := range texts {
		vecs, e := emb.Embed(ctx, []string{text})
		if errors.Is(e, llm.ErrContextLength) {
			vecs, e = emb.Embed(ctx, []string{core.TruncateRunes(text, embedFallbackChars)})
		}
		if errors.Is(e, llm.ErrContextLength) {
			fmt.Fprintf(os.Stderr, "warning: skipping doc %s — too long for the embedder even at %d chars\n", batch[i].ID, embedFallbackChars)
			skipped++
			continue
		}
		if e != nil {
			return embedded, skipped, fmt.Errorf("embed doc %s: %w", batch[i].ID, e)
		}
		if e := g.store.UpsertEmbedding(ctx, batch[i].ID, source, model, vecs[0]); e != nil {
			return embedded, skipped, e
		}
		embedded++
	}
	return embedded, skipped, nil
}

// embedMaxChars caps the doc text sent to the embedder. Kept well under small
// embedding models' context window (nomic-embed-text ~2048 tokens) — token-
// dense docs (YAML/code) pack more tokens per char. The doc lead carries most
// of its topical signal anyway.
const embedMaxChars = 2000
