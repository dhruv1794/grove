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
	"sort"
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

	// defaultCandidateWindow is how many fused candidates the precision stages
	// (rerank, prune) consider before trimming to the synthesis budget. The
	// scaling gate found ~17% of answers sit at fused rank 13–50, unreachable by
	// a smaller window — raise via --candidates to let rerank/prune score them.
	defaultCandidateWindow = 20

	// Recall-oriented descent (navigate.v2) explores broadly, so bound it:
	// stop once enough candidate leaves are gathered, and cap navigate calls.
	maxReachedLeaves = 40 // stop descending once this many leaves are reached
	navCallBudget    = 60 // safety cap on navigate calls per query

	// CRAG abstention thresholds (grader scores are 0–10). grove abstains only
	// when neither condition holds:
	//   strong:     one doc scores ≥ abstainScoreBelow (a single clear match), or
	//   collective: ≥ minModerateDocs docs score ≥ moderateScore.
	// The collective clause protects multi-doc questions, where the answer is
	// spread across several individually-mediocre docs (e.g. h03) — a best-score-
	// only gate over-abstains on those. Thresholds need measurement on the n=60
	// hard set before --correct is enabled by default.
	abstainScoreBelow = 5 // a single doc this relevant clears abstention alone
	moderateScore     = 4 // docs at least this relevant count as collective evidence
	minModerateDocs   = 2 // this many moderate docs also clears abstention

	// crossLinkMinStrength is the SeeAlso strength at or above which a reached
	// leaf's edge into another tree pulls that node's docs into the answer.
	// Explicit links (wikilinks) are 1.0; weak title/summary-overlap edges
	// (~0.3, deferred) stay below this and are not expanded.
	crossLinkMinStrength = 0.8
	maxCrossLinkDocs     = 3 // cap on docs added by cross-link expansion
)

// Embedder produces a query embedding for semantic retrieval. Optional — when
// nil (or no embeddings indexed), the semantic retriever is skipped.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

// Deps are the collaborators Ask needs.
type Deps struct {
	Store    store.Store
	LLM      llm.LLM
	Embedder Embedder // optional; enables the semantic (embeddings) retriever
}

// rrfK is the Reciprocal Rank Fusion constant (standard 60): a retriever votes
// for a doc with weight·1/(rrfK + rank), summed across retrievers.
const rrfK = 60.0

// Per-retriever fusion weights. The mechanism lets a retriever vote at a
// discount; the benchmark (benchmark-findings.md) shows the embeddings
// retriever trades top-rank precision for recall (weight 0 reproduces the BM25
// baseline 35%/0.494; weight 1 is 25%/0.448 but +5pp recall@12). The 0.5
// midpoint is non-monotonic — n=20 cannot resolve the tuning. Defaults stay
// neutral (the value all the headline ensemble/prune numbers were measured at)
// until the 50–100 Q set lands; do not tune on the small set.
const (
	ftsWeight       = 1.0
	descentWeight   = 1.0
	embedWeight     = 1.0
	treeEmbedWeight = 1.0
)

// treeNodeTopK is how many tree-node summaries the collapsed-tree retriever
// matches; each expands to its subtree's leaf documents.
const treeNodeTopK = 10

// rankedList is one retriever's ranked output plus its fusion weight.
type rankedList struct {
	ids    []string
	weight float64
}

// Options tunes a query.
type Options struct {
	Source       string // restrict to one source; "" queries every tree
	MaxDepth     int    // tree-descent depth limit; 0 → default 5
	MaxTokens    int    // synthesis output budget; 0 → default
	RetrieveOnly bool   // return citations without synthesizing an answer
	Fast         bool   // keyword/FTS retrieval, no LLM tree descent
	Prune        bool   // binary LLM relevance filter after fusion (precision stage)
	Decompose    bool   // split a multi-aspect question into per-aspect sub-queries
	Rerank       bool   // graded LLM relevance reorder after fusion (precision-at-1)
	Retrievers   []string // restrict base retrievers (e.g. ["embed"] = vector-only); empty = all
	MaxDocs      int      // cap on candidate docs returned/synthesized; 0 → default 12
	Correct      bool     // CRAG: abstain when the best grader score is below threshold (needs Rerank)
	CandidateWindow int   // fused candidates the rerank/prune stages consider; 0 → default 20
	ChunkEmbed      bool  // semantic retriever searches passage (chunk) vectors → parent docs, vs whole-doc vectors
	DebugScores     bool  // capture per-retriever ranked hits + raw scores into Result.RetrieverDebug
}

// candidateWindow is how many fused candidates the precision stages (rerank,
// prune) score before the result is trimmed to the synthesis budget.
func (q *querier) candidateWindow() int {
	w := defaultCandidateWindow
	if q.opts.CandidateWindow > 0 {
		w = q.opts.CandidateWindow
	}
	// Refine stages must score at least as many candidates as we intend to
	// return; otherwise --top-k beyond the window would silently narrow the
	// result to the window size. MaxDocs (--top-k) raises the floor so every
	// returned doc was actually graded/filtered.
	if q.opts.MaxDocs > w {
		w = q.opts.MaxDocs
	}
	return w
}

// Citation is one source document backing an answer. N is its inline marker.
// CrossLink is set when the document was surfaced via a SeeAlso edge from a
// reached leaf rather than by direct tree descent.
type Citation struct {
	N         int    `json:"n"`
	DocID     string `json:"doc_id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	SourceRef string `json:"source_ref"`
	// Location is a clickable target for the document: an absolute filesystem
	// path for local/Obsidian sources (later a URL for web sources). Set by the
	// orchestration layer (grove.Ask), which knows each source's root; empty
	// when the source has no resolvable location.
	Location  string `json:"location,omitempty"`
	CrossLink bool   `json:"cross_link,omitempty"`
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

// RetrieverHit is one base-retriever result: a doc, its 1-based rank in that
// retriever, and its raw score (bm25 for fts — lower better; cosine for embed —
// higher better). Emitted only under DebugScores.
type RetrieverHit struct {
	DocID string  `json:"doc_id"`
	Rank  int     `json:"rank"`
	Score float64 `json:"score"`
}

// Result is a completed query.
type Result struct {
	Answer    string         `json:"answer"`
	Citations []Citation     `json:"citations"`
	Trace     RetrievalTrace `json:"retrieval_trace"`
	Cost      llm.Tally      `json:"cost"`
	Abstained bool           `json:"abstained,omitempty"` // CRAG: best grader score below threshold
	// RetrieverDebug maps a base retriever ("fts","embed") to its ranked hits +
	// raw scores, for diagnostics and router features. Set only under --debug-scores.
	RetrieverDebug map[string][]RetrieverHit `json:"retriever_debug,omitempty"`
}

// Ask answers query against the forest.
func Ask(ctx context.Context, deps Deps, query string, opts Options) (*Result, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = defaultMaxDepth
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaultMaxTokens
	}
	if opts.Correct {
		opts.Rerank = true // the grader scores come from the rerank stage
	}
	q := &querier{deps: deps, opts: opts, query: query, navBudget: navCallBudget}
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

	navBudget     int             // remaining navigate calls this query
	crossLinked   map[string]bool // doc IDs pulled in by cross-link expansion
	graderScores  []int           // rerank grader scores (0–10) this query; nil = no grading ran
	retrieverDebug map[string][]RetrieverHit // per-retriever hits+scores; nil unless DebugScores
}

// shouldAbstain implements the CRAG decision: with --correct, abstain when the
// graded candidates show neither a single strong match nor a quorum of moderate
// ones. Returns false when grading didn't run (nil scores) so non-rerank queries
// are unaffected.
func (q *querier) shouldAbstain() bool {
	if !q.opts.Correct || len(q.graderScores) == 0 {
		return false
	}
	best, moderate := -1, 0
	for _, s := range q.graderScores {
		best = max(best, s)
		if s >= moderateScore {
			moderate++
		}
	}
	if best < 0 {
		return false // nothing actually graded (all calls errored)
	}
	strong := best >= abstainScoreBelow
	collective := moderate >= minModerateDocs
	return !strong && !collective
}

func (q *querier) run(ctx context.Context, trees []core.Tree) (*Result, error) {
	var docIDs []string
	var reachedLeaves []core.Node
	seen := map[string]bool{}
	// Tree descent is gated like the other retrievers, so it can be excluded for
	// baseline isolation (e.g. embeddings-only). Skipping it also skips the
	// tree-selection and navigation LLM calls.
	if q.useRetriever("tree") {
		selected, err := q.selectTrees(ctx, trees)
		if err != nil {
			return nil, err
		}
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
				leaf := lt.byID[leafID]
				reachedLeaves = append(reachedLeaves, leaf)
				for _, did := range leaf.DocIDs {
					if !seen[did] {
						seen[did] = true
						docIDs = append(docIDs, did)
					}
				}
			}
		}
	}
	descentIDs := q.expandCrossLinks(ctx, reachedLeaves, docIDs, seen)

	// Ensemble retrieval: fuse the descent candidates with the keyword (FTS) and
	// semantic (embeddings) retrievers via Reciprocal Rank Fusion. Each covers
	// the others' blind spots — descent reaches structurally-linked docs, FTS
	// nails exact terms, embeddings catch meaning the keywords miss. With
	// --decompose the keyword/semantic lists also cover each aspect sub-query.
	lists, err := q.fusedKeywordSemantic(ctx)
	if err != nil {
		return nil, err
	}
	lists = append(lists, rankedList{descentIDs, descentWeight})
	if lists, err = q.withTreeEmbed(ctx, lists); err != nil {
		return nil, err
	}
	fused, err := q.refine(ctx, rrfFuse(lists...))
	if err != nil {
		return nil, err
	}
	return q.assemble(ctx, fused)
}

// refine applies the optional precision stages to a fused candidate list, in
// order: prune (binary filter, removes irrelevant) then rerank (graded
// reorder, restores precision-at-1). Each is a no-op unless its flag is set.
func (q *querier) refine(ctx context.Context, fused []string) ([]string, error) {
	pruned, err := q.prune(ctx, fused)
	if err != nil {
		return nil, err
	}
	return q.rerank(ctx, pruned)
}

// rrfFuse merges weighted ranked retriever outputs by Reciprocal Rank Fusion:
// a doc's score is Σ list.weight/(rrfK + its 1-based rank in that list). Order
// across input lists is irrelevant; ties break by first appearance.
func rrfFuse(lists ...rankedList) []string {
	score := map[string]float64{}
	var order []string
	for _, list := range lists {
		for rank, id := range list.ids {
			if _, seen := score[id]; !seen {
				order = append(order, id)
			}
			score[id] += list.weight / (rrfK + float64(rank+1))
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return score[order[i]] > score[order[j]] })
	return order
}

// expandCrossLinks widens the candidate set: for each reached leaf, a strong
// SeeAlso edge into a *different* tree pulls that node's documents in (03 §5,
// step 5). Bounded by maxCrossLinkDocs. seen guards against re-adding a doc
// already retrieved by descent; added docs are recorded in q.crossLinked so
// their citations can be marked.
func (q *querier) expandCrossLinks(ctx context.Context, leaves []core.Node, docIDs []string, seen map[string]bool) []string {
	added := 0
	for _, leaf := range leaves {
		for _, ref := range leaf.SeeAlso {
			if added >= maxCrossLinkDocs {
				return docIDs
			}
			if ref.Strength < crossLinkMinStrength {
				continue
			}
			target, err := q.deps.Store.GetNode(ctx, ref.NodeID)
			if err != nil || target == nil || target.TreeID == leaf.TreeID {
				continue
			}
			for _, did := range target.DocIDs {
				if seen[did] {
					continue
				}
				seen[did] = true
				docIDs = append(docIDs, did)
				if q.crossLinked == nil {
					q.crossLinked = map[string]bool{}
				}
				q.crossLinked[did] = true
				q.trace = append(q.trace, TraceStep{Tree: target.TreeID, Node: target.Title, Reason: "cross-link: " + ref.Reason})
				if added++; added >= maxCrossLinkDocs {
					return docIDs
				}
			}
		}
	}
	return docIDs
}

// runFast answers without LLM tree descent: it fuses keyword (FTS) and
// semantic (embeddings) retrievers via RRF — the GPU-light tier. The only
// model call is the query embedding (skipped when no embedder is wired or
// nothing is indexed), so with RetrieveOnly it makes no chat-LLM call;
// otherwise it synthesizes a cited answer from the fused hits. --decompose and
// --prune each add chat calls when set.
func (q *querier) runFast(ctx context.Context) (*Result, error) {
	lists, err := q.fusedKeywordSemantic(ctx)
	if err != nil {
		return nil, err
	}
	if lists, err = q.withTreeEmbed(ctx, lists); err != nil {
		return nil, err
	}
	fused, err := q.refine(ctx, rrfFuse(lists...))
	if err != nil {
		return nil, err
	}
	return q.assemble(ctx, fused)
}

// docCap is the number of candidate docs to keep (returned + synthesized).
func (q *querier) docCap() int {
	if q.opts.MaxDocs > 0 {
		return q.opts.MaxDocs
	}
	return maxSynthesisDocs
}

// assemble caps the candidate documents, loads them, builds citations, and
// (unless RetrieveOnly) synthesizes a cited answer. Shared tail of the
// descent path and the --fast keyword path.
func (q *querier) assemble(ctx context.Context, docIDs []string) (*Result, error) {
	if limit := q.docCap(); len(docIDs) > limit {
		docIDs = docIDs[:limit]
	}
	docs, err := q.deps.Store.GetDocuments(ctx, docIDs)
	if err != nil {
		return nil, err
	}

	res := &Result{Trace: RetrievalTrace{SearchPath: q.trace}, RetrieverDebug: q.retrieverDebug}
	if len(docs) == 0 {
		res.Answer = "No relevant documents were found for this query."
		res.Cost = q.tally
		return res, nil
	}
	res.Citations = make([]Citation, len(docs))
	for i, d := range docs {
		res.Citations[i] = Citation{N: i + 1, DocID: d.ID, Title: d.Title, Source: d.Source, SourceRef: d.SourceRef, CrossLink: q.crossLinked[d.ID]}
	}
	// CRAG abstention: when the graded candidates show neither a strong single
	// match nor a quorum of moderate ones, no retrieved context adequately
	// answers the question — abstain instead of synthesizing a confident answer.
	if q.shouldAbstain() {
		res.Abstained = true
		res.Answer = "The retrieved documents don't appear to contain enough information to answer this. Closest matches are listed below, but none is a strong match — treat any answer drawn from them with caution."
		res.Cost = q.tally
		return res, nil
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
