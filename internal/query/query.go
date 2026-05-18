// Package query answers questions against the knowledge forest. It selects
// the trees likely to hold the answer, descends each tree-of-contents
// PageIndex-style (at every level the model picks which children to explore),
// assembles the reached leaf documents, and synthesizes a cited answer.
//
// It is core-library code — it depends on the Store and LLM interfaces, never
// on a specific connector or adapter.
package query

import (
	"context"
	"fmt"
	"strings"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/store"
	"grove/prompts"
)

const (
	defaultMaxDepth   = 5
	defaultMaxTokens  = 1024
	navigateMaxTokens = 256
	navigateTemp      = 0.1
	synthesisTemp     = 0.2
	maxExcerptChars   = 4000 // per-document text budget in the synthesis prompt
	maxSynthesisDocs  = 12   // cap on leaf documents fed to synthesis
)

// Deps are the collaborators Ask needs.
type Deps struct {
	Store store.Store
	LLM   llm.LLM
}

// Options tunes a query.
type Options struct {
	Source       string // restrict to one source; "" queries every tree
	MaxDepth     int    // tree-descent depth limit; 0 → default 5
	MaxTokens    int    // synthesis output budget; 0 → default
	RetrieveOnly bool   // return citations without synthesizing an answer
	Fast         bool   // keyword/FTS retrieval, no LLM tree descent
}

// Citation is one source document backing an answer. N is its inline marker.
type Citation struct {
	N         int    `json:"n"`
	DocID     string `json:"doc_id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	SourceRef string `json:"source_ref"`
}

// TraceStep is one descent decision: a node entered and why.
type TraceStep struct {
	Tree   string `json:"tree"`
	Node   string `json:"node"`
	Reason string `json:"reason"`
}

// RetrievalTrace is the navigation path through the forest — not the model's
// chain-of-thought.
type RetrievalTrace struct {
	SearchPath []TraceStep `json:"search_path"`
}

// Result is a completed query.
type Result struct {
	Answer    string         `json:"answer"`
	Citations []Citation     `json:"citations"`
	Trace     RetrievalTrace `json:"retrieval_trace"`
	Cost      llm.Tally      `json:"cost"`
}

// Ask answers query against the forest.
func Ask(ctx context.Context, deps Deps, query string, opts Options) (*Result, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = defaultMaxDepth
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaultMaxTokens
	}
	q := &querier{deps: deps, opts: opts, query: query}
	if opts.Fast {
		return q.runFast(ctx)
	}
	trees, err := deps.Store.ListTrees(ctx)
	if err != nil {
		return nil, err
	}
	if opts.Source != "" {
		trees = filterTrees(trees, opts.Source)
	}
	if len(trees) == 0 {
		return nil, core.NewError(core.KindNoSources,
			"no trees to query",
			"run `grove build` first (and `grove connect` if there are no sources)")
	}
	return q.run(ctx, trees)
}

type querier struct {
	deps  Deps
	opts  Options
	query string
	tally llm.Tally
	trace []TraceStep
}

func (q *querier) run(ctx context.Context, trees []core.Tree) (*Result, error) {
	selected, err := q.selectTrees(ctx, trees)
	if err != nil {
		return nil, err
	}

	var docIDs []string
	seen := map[string]bool{}
	for _, t := range selected {
		lt, err := q.loadTree(ctx, t)
		if err != nil {
			return nil, err
		}
		var leaves []string
		if err := q.descend(ctx, lt, t.RootNodeID, 0, &leaves); err != nil {
			return nil, err
		}
		for _, leafID := range leaves {
			for _, did := range lt.byID[leafID].DocIDs {
				if !seen[did] {
					seen[did] = true
					docIDs = append(docIDs, did)
				}
			}
		}
	}
	return q.assemble(ctx, docIDs)
}

// runFast answers via SQLite full-text keyword search instead of LLM tree
// descent. With RetrieveOnly set it makes no model call at all — the
// sub-second path; otherwise it still synthesizes a cited answer from the
// keyword hits.
func (q *querier) runFast(ctx context.Context) (*Result, error) {
	docIDs, err := q.deps.Store.SearchDocuments(ctx, q.query, q.opts.Source, maxSynthesisDocs)
	if err != nil {
		return nil, err
	}
	return q.assemble(ctx, docIDs)
}

// assemble caps the candidate documents, loads them, builds citations, and
// (unless RetrieveOnly) synthesizes a cited answer. Shared tail of the
// descent path and the --fast keyword path.
func (q *querier) assemble(ctx context.Context, docIDs []string) (*Result, error) {
	if len(docIDs) > maxSynthesisDocs {
		docIDs = docIDs[:maxSynthesisDocs]
	}
	docs, err := q.deps.Store.GetDocuments(ctx, docIDs)
	if err != nil {
		return nil, err
	}

	res := &Result{Trace: RetrievalTrace{SearchPath: q.trace}}
	if len(docs) == 0 {
		res.Answer = "No relevant documents were found for this query."
		res.Cost = q.tally
		return res, nil
	}
	res.Citations = make([]Citation, len(docs))
	for i, d := range docs {
		res.Citations[i] = Citation{N: i + 1, DocID: d.ID, Title: d.Title, Source: d.Source, SourceRef: d.SourceRef}
	}
	if !q.opts.RetrieveOnly {
		if res.Answer, err = q.synthesize(ctx, docs); err != nil {
			return nil, err
		}
	}
	res.Cost = q.tally
	return res, nil
}

// selectTrees asks the model which trees likely hold the answer. A single
// tree is used directly; if the model picks none, every tree is searched
// rather than giving up.
func (q *querier) selectTrees(ctx context.Context, trees []core.Tree) ([]core.Tree, error) {
	if len(trees) == 1 {
		return trees, nil
	}
	opts := make([]option, len(trees))
	for i, t := range trees {
		opts[i] = q.treeOption(ctx, t)
	}
	chosen, _, err := q.navigate(ctx, opts)
	if err != nil {
		return nil, err
	}
	if len(chosen) == 0 {
		return trees, nil
	}
	sel := make([]core.Tree, 0, len(chosen))
	for _, idx := range chosen {
		sel = append(sel, trees[idx])
	}
	return sel, nil
}

// treeOption describes a tree to the selection model using its root node.
func (q *querier) treeOption(ctx context.Context, t core.Tree) option {
	o := option{title: t.Name}
	node, err := q.deps.Store.GetNode(ctx, t.RootNodeID)
	if err != nil || node == nil {
		return o
	}
	p, err := core.ReadNodePayload(node.PayloadPath)
	if err != nil {
		return o
	}
	if p.Title != "" {
		o.title = p.Title
	}
	o.summary = p.Summary
	return o
}

func (q *querier) synthesize(ctx context.Context, docs []core.Document) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "QUESTION: %s\n\nSOURCE EXCERPTS:\n", q.query)
	for i, d := range docs {
		title := d.Title
		if title == "" {
			title = d.SourceRef
		}
		fmt.Fprintf(&sb, "\n[%d] %s (%s)\n%s\n", i+1, title, d.SourceRef, core.TruncateRunes(d.Content, maxExcerptChars))
	}
	resp, err := q.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Answer.Template},
			{Role: llm.RoleUser, Content: sb.String()},
		},
		Temperature: synthesisTemp,
		MaxTokens:   q.opts.MaxTokens,
	})
	if err != nil {
		return "", err
	}
	q.tally.Add(resp)
	return strings.TrimSpace(resp.Content), nil
}

func filterTrees(trees []core.Tree, source string) []core.Tree {
	var out []core.Tree
	for _, t := range trees {
		if t.Source == source {
			out = append(out, t)
		}
	}
	return out
}
