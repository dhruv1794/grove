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
	"path/filepath"
	"strings"
	"time"

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
	Source  string // build only this source; "" builds every source
	Rebuild bool   // ignore the node cache; re-call the model for every node
	DryRun  bool   // plan and report the work; no model calls, no writes
}

// Result reports a completed build run.
type Result struct {
	Model     string    `json:"model"`
	Trees     int       `json:"trees"`
	Nodes     int       `json:"nodes"`
	CacheHits int       `json:"cache_hits"`
	CacheMiss int       `json:"cache_miss"`
	Tally     llm.Tally `json:"tally"`
	Elapsed   string    `json:"elapsed"`
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
	for _, src := range sources {
		if err := b.buildSource(ctx, src); err != nil {
			return nil, fmt.Errorf("source %s: %w", src.Name, err)
		}
	}
	b.res.Elapsed = time.Since(start).Round(time.Millisecond).String()
	return b.res, nil
}

type builder struct {
	deps  Deps
	opts  Options
	model string
	res   *Result
}

func (b *builder) buildSource(ctx context.Context, src core.Source) error {
	docs, err := b.deps.Store.ListDocumentsBySource(ctx, src.Name)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil // nothing to index for this source — not an error
	}

	treeID := src.Name
	root := assembleTree(treeID, docs)
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

// processNode walks the tree post-order: it hashes each node from its
// (already-processed) children, then fills the title/summary from the node
// cache or a fresh model call.
func (b *builder) processNode(ctx context.Context, n *bnode) error {
	for _, c := range n.children {
		if err := b.processNode(ctx, c); err != nil {
			return err
		}
	}
	n.contentHash = hashNode(n)
	n.payloadPath = core.PayloadPath(b.deps.Layout.Trees, n.contentHash,
		cacheFilename(prompts.Node.Ver(), b.model))
	b.res.Nodes++

	if !b.opts.Rebuild {
		if p, err := core.ReadNodePayload(n.payloadPath); err == nil {
			n.title, n.summary = p.Title, p.Summary
			b.res.CacheHits++
			return nil
		}
	}
	b.res.CacheMiss++
	if b.opts.DryRun {
		n.title = b.fallbackTitle(n)
		return nil
	}

	resp, err := b.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Node.Template},
			{Role: llm.RoleUser, Content: b.nodeContent(n)},
		},
		Temperature: requestTemp,
		MaxTokens:   requestMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("summarize node %s: %w", n.id, err)
	}
	b.res.Tally.Add(resp)
	title, summary := parseSummary(resp.Content)
	if title == "" {
		title = b.fallbackTitle(n)
	}
	n.title, n.summary = title, summary
	return b.writeNodePayload(n)
}

// nodeContent assembles the user message for a node: a leaf supplies its
// document's (truncated) text; an internal node supplies its children's
// titles and summaries.
func (b *builder) nodeContent(n *bnode) string {
	if n.doc != nil {
		title := n.doc.Title
		if title == "" {
			title = filepath.Base(n.doc.SourceRef)
		}
		return "DOCUMENT: " + title + "\n\n" + core.TruncateRunes(n.doc.Content, maxLeafChars)
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
	return sb.String()
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
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i >= 0 && j > i {
		var out struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(s[i:j+1]), &out); err == nil {
			return strings.TrimSpace(out.Title), strings.TrimSpace(out.Summary)
		}
	}
	return "", strings.TrimSpace(s)
}

// cacheFilename is the per-node payload filename, encoding the prompt version
// and build model so a node's cache entry is keyed by all three of
// (content_hash, prompt_ver, build_model).
func cacheFilename(promptVer, model string) string {
	return slug(promptVer) + "__" + slug(model) + ".json"
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
