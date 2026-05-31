// Package indexer builds the knowledge forest: for each connected source it
// assembles a tree-of-contents over the source's documents, generates a title
// and summary for every node with the build model, and persists the result.
//
// It is core-library code — it depends on the Store and LLM interfaces, never
// on a specific connector or adapter. Per-node LLM output is cached on disk
// keyed by (content_hash, prompt_ver, build_model), so an unchanged rebuild
// makes no model calls.
package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"grove/internal/compress"
	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/store"
	"grove/prompts"
)

const (
	maxLeafChars     = 6000 // document content is truncated to this for the prompt
	requestMaxTokens = 512
	requestTemp      = 0.2
)

// Deps are the collaborators Build needs.
type Deps struct {
	Store  store.Store
	LLM    llm.LLM
	Layout core.Layout
}

// Options tunes a build run.
type Options struct {
	Source      string // build only this source; "" builds every source
	Rebuild     bool   // ignore the node cache; re-call the model for every node
	DryRun      bool   // plan and report the work; no model calls, no writes
	NoGroup     bool   // skip LLM topic grouping of flat folders (mirror structure only)
	Concurrency int    // max in-flight model calls; <= 1 builds sequentially

	// Compress strips boilerplate from leaf document text before summarization
	// (opt-in; None is the default no-op). It is part of the node cache key, so
	// toggling it invalidates cached summaries.
	Compress compress.Level

	// OnProgress, if set, is called as a source's nodes are processed. It is
	// invoked serially (never concurrently), so the callback needs no locking.
	OnProgress func(Progress)
}

// Progress reports node-build progress for one source's tree. The indexer
// emits it through Options.OnProgress so an adapter (the CLI) can render a
// progress bar without the indexer depending on a presentation library.
type Progress struct {
	Source string
	Total  int // nodes in this source's tree
	Done   int // nodes completed so far in this source
}

// Result reports a completed build run.
type Result struct {
	Model      string    `json:"model"`
	Trees      int       `json:"trees"`
	Nodes      int       `json:"nodes"`
	Groups     int       `json:"groups"` // topic nodes created by LLM grouping of flat folders
	CacheHits  int       `json:"cache_hits"`
	CacheMiss  int       `json:"cache_miss"`
	CrossLinks int       `json:"cross_links"`
	Compressed int       `json:"compressed"`  // leaf docs whose text was compressed before summarization
	TokensSaved int      `json:"tokens_saved"` // approx input tokens saved by compression (mdcompress estimate)
	Tally      llm.Tally `json:"tally"`
	Elapsed    string    `json:"elapsed"`
}

// Build indexes connected sources into the forest. Each source becomes one
// tree (v1: one tree per source). It returns KindNoSources when there is
// nothing to build.
func Build(ctx context.Context, deps Deps, opts Options) (*Result, error) {
	start := time.Now()
	sources, err := deps.Store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	if opts.Source != "" {
		sources = filterSources(sources, opts.Source)
		if len(sources) == 0 {
			return nil, core.NewError(core.KindNoSources,
				fmt.Sprintf("no source named %q", opts.Source),
				"run `grove sources` to list connected sources")
		}
	}
	if len(sources) == 0 {
		return nil, core.NewError(core.KindNoSources,
			"no sources connected",
			"connect one with `grove connect local <path>`")
	}

	model := deps.LLM.Model().String()
	b := &builder{deps: deps, opts: opts, model: model, res: &Result{Model: model}}
	if opts.Concurrency > 1 {
		b.sem = make(chan struct{}, opts.Concurrency)
	}
	for _, src := range sources {
		if err := b.buildSource(ctx, src); err != nil {
			return nil, fmt.Errorf("source %s: %w", src.Name, err)
		}
	}
	// Cross-link pass runs once all trees in this run are persisted, so both
	// edge endpoints resolve to live nodes (per documents/03 §7).
	if !opts.DryRun {
		if err := b.crossLink(ctx); err != nil {
			return nil, fmt.Errorf("cross-link: %w", err)
		}
	}
	finished := time.Now()
	b.res.Elapsed = finished.Sub(start).Round(time.Millisecond).String()

	// A dry run writes nothing, so it leaves no history row.
	if !opts.DryRun {
		if err := deps.Store.RecordBuild(ctx, core.BuildRecord{
			StartedAt:     start,
			FinishedAt:    finished,
			Model:         model,
			DocsProcessed: len(b.allDocs),
			NodesCreated:  b.res.Nodes,
			CostUSD:       b.res.Tally.USD,
			Status:        core.BuildStatusOK,
		}); err != nil {
			return nil, fmt.Errorf("record build: %w", err)
		}
	}
	return b.res, nil
}

type builder struct {
	deps    Deps
	opts    Options
	model   string
	res     *Result
	allDocs []core.Document // every doc built this run; the cross-link pass resolves links over it

	sem chan struct{} // bounds in-flight model calls; nil when building sequentially
	mu  sync.Mutex    // guards res (counts + Tally) under concurrent node processing

	pgMu     sync.Mutex // serializes progress updates + the OnProgress callback, separate from mu so UI rendering doesn't block the counter hot path
	pgSource string     // source whose tree is currently being processed
	pgTotal  int        // node count of the current source's tree
	pgDone   int        // nodes completed in the current source
}

func (b *builder) buildSource(ctx context.Context, src core.Source) error {
	docs, err := b.deps.Store.ListDocumentMetaBySource(ctx, src.Name)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil // nothing to index for this source — not an error
	}
	b.allDocs = append(b.allDocs, docs...) // unguarded: sources build sequentially in Build

	treeID := src.Name
	root := assembleTree(treeID, docs)
	// Topic grouping needs model calls, so it is skipped on a dry run (which
	// makes none) — dry-run node counts therefore omit any topic nodes.
	if !b.opts.NoGroup && !b.opts.DryRun {
		if err := b.regroup(ctx, root); err != nil {
			return err
		}
	}
	b.progressStart(src.Name, root)
	if err := b.processNode(ctx, root); err != nil {
		return err
	}
	b.res.Trees++
	if b.opts.DryRun {
		return nil
	}

	builtAt := time.Now()
	var nodes []core.Node
	b.collectNodes(root, builtAt, &nodes)

	// Order matters: clear stale nodes, then upsert the tree row so the
	// nodes' tree_id foreign key resolves, then upsert the nodes.
	if _, err := b.deps.Store.DeleteNodesByTree(ctx, treeID); err != nil {
		return err
	}
	tree := core.Tree{
		ID:         treeID,
		Source:     src.Name,
		Name:       src.Name,
		RootNodeID: root.id,
		DocCount:   len(docs),
		NodeCount:  len(nodes),
		Created:    builtAt,
		Modified:   builtAt,
		BuildModel: b.model,
		PromptVer:  prompts.Node.Ver(),
	}
	if err := b.deps.Store.UpsertTree(ctx, tree); err != nil {
		return err
	}
	return b.deps.Store.UpsertNodes(ctx, nodes)
}

// processNode walks the tree post-order: children are processed first (a
// node's content hash derives from theirs), then the node itself. Sibling
// subtrees are independent, so they run concurrently when a semaphore is set;
// the semaphore in fillNode bounds the scarce resource — in-flight model calls
// — rather than goroutines, which would deadlock a parent waiting on children.
func (b *builder) processNode(ctx context.Context, n *bnode) error {
	if err := b.processChildren(ctx, n); err != nil {
		return err
	}
	if err := b.fillNode(ctx, n); err != nil {
		return err
	}
	b.progressTick()
	return nil
}

// progressStart resets the progress counters for a source's tree and emits the
// initial (Done=0) event. It runs before any worker goroutine is spawned, so
// the field writes are visible to later progressTick calls via that happens-
// before edge; no lock needed here. countNodes is only walked when a progress
// consumer is attached.
func (b *builder) progressStart(source string, root *bnode) {
	if b.opts.OnProgress == nil {
		return
	}
	total := countNodes(root)
	b.pgSource, b.pgTotal, b.pgDone = source, total, 0
	b.opts.OnProgress(Progress{Source: source, Total: total, Done: 0})
}

// progressTick advances the current source's progress by one node, under pgMu
// so OnProgress is never invoked concurrently — without blocking the counter
// hot path (mu), which every worker takes via tally.
func (b *builder) progressTick() {
	if b.opts.OnProgress == nil {
		return
	}
	b.pgMu.Lock()
	defer b.pgMu.Unlock()
	b.pgDone++
	b.opts.OnProgress(Progress{Source: b.pgSource, Total: b.pgTotal, Done: b.pgDone})
}

// countNodes returns the number of nodes in the assembled tree rooted at n.
func countNodes(n *bnode) int {
	count := 1
	for _, c := range n.children {
		count += countNodes(c)
	}
	return count
}

func (b *builder) processChildren(ctx context.Context, n *bnode) error {
	if b.sem == nil || len(n.children) <= 1 {
		for _, c := range n.children {
			if err := b.processNode(ctx, c); err != nil {
				return err
			}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	for _, c := range n.children {
		wg.Add(1)
		go func(c *bnode) {
			defer wg.Done()
			if err := b.processNode(ctx, c); err != nil {
				once.Do(func() { firstErr = err; cancel() })
			}
		}(c)
	}
	wg.Wait()
	return firstErr
}

// fillNode hashes a node from its already-processed children, then fills its
// title/summary from the node cache or a fresh model call.
func (b *builder) fillNode(ctx context.Context, n *bnode) error {
	n.contentHash = hashNode(n)
	n.payloadPath = core.PayloadPath(b.deps.Layout.Trees, n.contentHash,
		cacheFilename(prompts.Node.Ver(), b.model, b.opts.Compress.CacheToken()))
	b.tally(func(r *Result) { r.Nodes++ })

	if !b.opts.Rebuild {
		if p, err := core.ReadNodePayload(n.payloadPath); err == nil {
			n.title, n.summary = p.Title, p.Summary
			b.tally(func(r *Result) { r.CacheHits++ })
			return nil
		}
	}
	b.tally(func(r *Result) { r.CacheMiss++ })
	if b.opts.DryRun {
		n.title = b.fallbackTitle(n)
		return nil
	}

	if b.sem != nil {
		select {
		case b.sem <- struct{}{}:
			defer func() { <-b.sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	userMsg, err := b.nodeContent(ctx, n)
	if err != nil {
		return err
	}
	resp, err := b.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Node.Template},
			{Role: llm.RoleUser, Content: userMsg},
		},
		Temperature: requestTemp,
		MaxTokens:   requestMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("summarize node %s: %w", n.id, err)
	}
	b.tally(func(r *Result) { r.Tally.Add(resp) })
	title, summary := parseSummary(resp.Content)
	if title == "" {
		title = b.fallbackTitle(n)
	}
	n.title, n.summary = title, summary
	return b.writeNodePayload(n)
}

// tally applies a result mutation under the lock so concurrent fillNode calls
// don't race on the shared counters. Increments are commutative, so totals are
// independent of node-processing order.
func (b *builder) tally(f func(*Result)) {
	b.mu.Lock()
	f(b.res)
	b.mu.Unlock()
}

// nodeContent assembles the user message for a node: a leaf supplies its
// document's (truncated) text, fetched lazily here since it is only needed on a
// cache miss; an internal node supplies its children's titles and summaries.
func (b *builder) nodeContent(ctx context.Context, n *bnode) (string, error) {
	if n.doc != nil {
		body, err := b.deps.Store.GetDocumentContent(ctx, n.doc.Hash)
		if err != nil {
			return "", fmt.Errorf("load content for %s: %w", n.doc.ID, err)
		}
		// Strip boilerplate before truncation so the summary sees more signal per
		// token. A compression error falls back to the original text (the build
		// must not abort over one malformed doc); only meaningful savings count.
		if out, stats, cerr := compress.Apply(b.opts.Compress, body); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: compress %s: %v\n", n.doc.ID, cerr)
		} else if stats.Saved() > 0 {
			body = out
			b.tally(func(r *Result) { r.Compressed++; r.TokensSaved += stats.Saved() })
		}
		title := n.doc.Title
		if title == "" {
			title = filepath.Base(n.doc.SourceRef)
		}
		return "DOCUMENT: " + title + "\n\n" + core.TruncateRunes(body, maxLeafChars), nil
	}
	var sb strings.Builder
	sb.WriteString("SECTION grouping these subsections:\n")
	for _, c := range n.children {
		summary := c.summary
		if summary == "" {
			summary = "(no summary)"
		}
		fmt.Fprintf(&sb, "- %s: %s\n", c.title, summary)
	}
	return sb.String(), nil
}

func (b *builder) fallbackTitle(n *bnode) string {
	if n.doc != nil {
		if n.doc.Title != "" {
			return n.doc.Title
		}
		return filepath.Base(n.doc.SourceRef)
	}
	if n.parentID == "" {
		return n.treeID
	}
	return n.name
}

func (b *builder) writeNodePayload(n *bnode) error {
	p := &core.NodePayload{
		SchemaVersion: core.NodeSchemaVersion,
		NodeID:        n.id,
		Title:         n.title,
		Summary:       n.summary,
		ContentHash:   n.contentHash,
		PromptVer:     prompts.Node.Ver(),
		BuildModel:    b.model,
		BuiltAt:       time.Now(),
	}
	if n.doc != nil {
		p.DocIDs = []string{n.doc.ID}
	}
	return core.WriteNodePayload(n.payloadPath, p)
}

// parseSummary extracts the title and summary from a model response. Models
// occasionally wrap the JSON in prose or fences, so the first {...} span is
// taken; on any failure the whole response is used as the summary.
func parseSummary(s string) (title, summary string) {
	if span, ok := core.JSONObjectSpan(s); ok {
		var out struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(span), &out); err == nil {
			return strings.TrimSpace(out.Title), strings.TrimSpace(out.Summary)
		}
	}
	return "", strings.TrimSpace(s)
}

// cacheFilename is the per-node payload filename, encoding the prompt version,
// build model, and (when set) the compression level so a node's cache entry is
// keyed by all of (content_hash, prompt_ver, build_model, compress). The
// compress suffix is omitted for the default (uncompressed) case so existing
// caches stay valid.
func cacheFilename(promptVer, model, compress string) string {
	name := slug(promptVer) + "__" + slug(model)
	if compress != "" {
		name += "__c-" + slug(compress)
	}
	return name + ".json"
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func filterSources(sources []core.Source, name string) []core.Source {
	for _, s := range sources {
		if s.Name == name {
			return []core.Source{s}
		}
	}
	return nil
}
