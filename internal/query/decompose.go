package query

import (
	"context"
	"strings"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/internal/store"
	"grove/prompts"
)

const (
	maxSubQueries   = 5
	decomposeTokens = 200
	decomposeTemp   = 0.2
)

// retrieverDepth is how many candidates each base retriever (FTS, embeddings)
// returns per query — at least the synthesis cap, but widened when the caller
// asks for more docs (e.g. recall@50 evaluation) so the fused pool can fill.
func (q *querier) retrieverDepth() int {
	// The precision stages can only score what the fused pool holds, so the base
	// retrievers return at least the synthesis cap, the requested top-k, and the
	// candidate window — whichever is largest.
	return max(maxSynthesisDocs, q.opts.MaxDocs, q.candidateWindow())
}

// keywordSemanticLists runs the enabled base retrievers (keyword FTS and/or
// semantic embeddings) for one query string and returns them as weighted ranked
// lists. Which run is governed by Options.Retrievers — used to isolate a single
// retriever for evaluation (e.g. embeddings-only = a vanilla vector-RAG baseline).
func (q *querier) keywordSemanticLists(ctx context.Context, query string) ([]rankedList, error) {
	var lists []rankedList
	if q.useRetriever("fts") {
		hits, err := q.deps.Store.SearchDocumentsScored(ctx, query, q.opts.Source, q.retrieverDepth())
		if err != nil {
			return nil, err
		}
		q.recordDebug("fts", hits)
		lists = append(lists, rankedList{hitIDs(hits), ftsWeight})
	}
	if q.useRetriever("embed") && q.deps.Embedder != nil {
		vecs, err := q.deps.Embedder.Embed(ctx, []string{query})
		if err != nil {
			return nil, err
		}
		if len(vecs) > 0 {
			if q.opts.ChunkEmbed {
				// passage-level: match a chunk inside a doc, return parent docs.
				sem, err := q.deps.Store.SearchByChunkVector(ctx, vecs[0], q.opts.Source, q.retrieverDepth())
				if err != nil {
					return nil, err
				}
				lists = append(lists, rankedList{sem, embedWeight})
			} else {
				hits, err := q.deps.Store.SearchByVectorScored(ctx, vecs[0], q.opts.Source, q.retrieverDepth())
				if err != nil {
					return nil, err
				}
				q.recordDebug("embed", hits)
				lists = append(lists, rankedList{hitIDs(hits), embedWeight})
			}
		}
	}
	return lists, nil
}

func hitIDs(hits []store.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out
}

// recordDebug stashes a retriever's ranked hits + scores when --debug-scores is
// on. With --decompose the same retriever runs per sub-query; only the first
// (original-query) capture is kept so features stay query-level.
func (q *querier) recordDebug(name string, hits []store.Hit) {
	if !q.opts.DebugScores {
		return
	}
	if q.retrieverDebug == nil {
		q.retrieverDebug = map[string][]RetrieverHit{}
	}
	if _, seen := q.retrieverDebug[name]; seen {
		return
	}
	out := make([]RetrieverHit, len(hits))
	for i, h := range hits {
		out[i] = RetrieverHit{DocID: h.ID, Rank: i + 1, Score: h.Score}
	}
	q.retrieverDebug[name] = out
}

// treeEmbedEnabled reports whether the collapsed-tree retriever is explicitly
// requested. Unlike the other retrievers it is NOT part of the default ensemble:
// measured net-negative on a flat, self-contained-document corpus (K8s docs, n=60
// — adds ~0 recall, dilutes top-rank), since the doc-level FTS+embed retrievers
// already reach those docs. It runs only when named in --retrievers, where it may
// help on narrative / long-document corpora (the regime RAPTOR/T-Retriever showed
// gains in). See benchmark-findings.md.
func (q *querier) treeEmbedEnabled() bool {
	for _, r := range q.opts.Retrievers {
		if r == "tree-embed" {
			return true
		}
	}
	return false
}

// withTreeEmbed appends the collapsed-tree retriever's list to lists when it is
// explicitly enabled and produced candidates. Shared by run() and runFast().
func (q *querier) withTreeEmbed(ctx context.Context, lists []rankedList) ([]rankedList, error) {
	if !q.treeEmbedEnabled() {
		return lists, nil
	}
	tl, err := q.treeEmbedList(ctx)
	if err != nil {
		return nil, err
	}
	if len(tl.ids) > 0 {
		lists = append(lists, tl)
	}
	return lists, nil
}

// treeEmbedList is the collapsed-tree retriever: it embeds the query, finds the
// most similar internal tree-node summaries, and expands each to its subtree's
// leaf documents — surfacing the tree as a retrieval signal at every level
// (fused via RRF) rather than via greedy descent. Empty (and harmless) when no
// node embeddings exist or no embedder is wired.
func (q *querier) treeEmbedList(ctx context.Context) (rankedList, error) {
	if q.deps.Embedder == nil {
		return rankedList{}, nil
	}
	vecs, err := q.deps.Embedder.Embed(ctx, []string{q.query})
	if err != nil || len(vecs) == 0 {
		return rankedList{}, err
	}
	hits, err := q.deps.Store.SearchNodesByVector(ctx, vecs[0], q.opts.Source, treeNodeTopK)
	if err != nil || len(hits) == 0 {
		return rankedList{}, err
	}
	cache := map[string]*loadedTree{}
	var ids []string
	seen := map[string]bool{}
	depth := q.retrieverDepth()
	for _, h := range hits {
		lt := cache[h.TreeID]
		if lt == nil {
			if lt, err = q.loadTree(ctx, core.Tree{ID: h.TreeID}); err != nil {
				return rankedList{}, err
			}
			cache[h.TreeID] = lt
		}
		var leaves []string
		collectLeaves(lt, h.NodeID, &leaves)
		for _, leafID := range leaves {
			for _, did := range lt.byID[leafID].DocIDs {
				if !seen[did] {
					seen[did] = true
					ids = append(ids, did)
				}
			}
		}
		if len(ids) >= depth {
			break // enough candidates; RRF dilutes the long tail anyway
		}
	}
	if len(ids) > depth {
		ids = ids[:depth]
	}
	return rankedList{ids, treeEmbedWeight}, nil
}

// useRetriever reports whether retriever name is enabled. An empty
// Options.Retrievers means "all" (the normal ensemble); a non-empty list
// restricts to exactly those named, for baseline isolation.
func (q *querier) useRetriever(name string) bool {
	if len(q.opts.Retrievers) == 0 {
		return true
	}
	for _, r := range q.opts.Retrievers {
		if r == name {
			return true
		}
	}
	return false
}

// fusedKeywordSemantic gathers FTS+embedding lists for the original query and,
// when --decompose is set, for each aspect sub-query, so docs that only match
// one aspect of a multi-aspect question are surfaced. All lists fuse together
// in the caller via rrfFuse.
func (q *querier) fusedKeywordSemantic(ctx context.Context) ([]rankedList, error) {
	queries := []string{q.query}
	if q.opts.Decompose {
		subs, err := q.decompose(ctx)
		if err != nil {
			return nil, err
		}
		queries = append(queries, subs...)
	}
	var lists []rankedList
	for _, query := range queries {
		l, err := q.keywordSemanticLists(ctx, query)
		if err != nil {
			return nil, err
		}
		lists = append(lists, l...)
	}
	return lists, nil
}

// decompose asks the model to split the question into per-aspect sub-queries.
// Returns only the sub-queries (the original is searched separately). A failed
// or empty split degrades to no sub-queries rather than erroring the query.
func (q *querier) decompose(ctx context.Context) ([]string, error) {
	if q.deps.LLM == nil {
		return nil, nil
	}
	resp, err := q.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Decompose.Template},
			{Role: llm.RoleUser, Content: q.query},
		},
		Temperature: decomposeTemp,
		MaxTokens:   decomposeTokens,
	})
	if err != nil {
		return nil, err
	}
	q.tally.Add(resp)
	return parseSubQueries(resp.Content, q.query), nil
}

// parseSubQueries reads one sub-query per line, dropping blanks, list markers,
// and any line equal to the original query (already searched). Capped at
// maxSubQueries. A single line that just echoes the original yields nothing.
func parseSubQueries(content, original string) []string {
	var subs []string
	for _, line := range strings.Split(content, "\n") {
		s := strings.TrimSpace(line)
		s = strings.TrimLeft(s, "-*0123456789.) ")
		s = strings.TrimSpace(s)
		if s == "" || strings.EqualFold(s, strings.TrimSpace(original)) {
			continue
		}
		subs = append(subs, s)
		if len(subs) >= maxSubQueries {
			break
		}
	}
	return subs
}
