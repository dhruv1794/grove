package query

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/prompts"
)

const (
	rerankExcerptChars  = 1500 // per-document text shown to the scorer
	rerankConcurrency   = 8
	rerankMaxTokens     = 4
	rerankTemp          = 0.0
	rerankMaxScore      = 10
)

// rerank is the graded precision stage: it scores each fused candidate 0–10 for
// how well it answers the question, then reorders by score (descending), ties
// broken by the original fused order. Unlike the binary pruner it does not drop
// docs — it reorders — so it can restore precision-at-1 on the wider candidate
// pool decomposition produces. A no-op without a model or when disabled.
func (q *querier) rerank(ctx context.Context, docIDs []string) ([]string, error) {
	if !q.opts.Rerank || q.deps.LLM == nil || len(docIDs) == 0 {
		return docIDs, nil
	}
	if w := q.candidateWindow(); len(docIDs) > w {
		docIDs = docIDs[:w]
	}
	docs, err := q.deps.Store.GetDocuments(ctx, docIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]core.Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}

	scores := make([]int, len(docIDs))
	for i := range scores {
		scores[i] = -1 // unscored (missing doc / error) sorts last but keeps order
	}
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	sem := make(chan struct{}, rerankConcurrency)
	for i, id := range docIDs {
		doc, ok := byID[id]
		if !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, doc core.Document) {
			defer wg.Done()
			defer func() { <-sem }()
			score, resp, err := q.scoreDoc(ctx, doc)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			q.tally.Add(resp)
			scores[i] = score
		}(i, doc)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	q.graderScores = scores // expose the grades for the CRAG abstention decision

	order := make([]int, len(docIDs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	ranked := make([]string, len(docIDs))
	for i, idx := range order {
		ranked[i] = docIDs[idx]
	}
	return ranked, nil
}

// scoreDoc runs one 0–10 relevance scoring call for a single document.
func (q *querier) scoreDoc(ctx context.Context, doc core.Document) (int, *llm.Response, error) {
	title := doc.Title
	if title == "" {
		title = doc.SourceRef
	}
	user := fmt.Sprintf("QUESTION: %s\n\nDOCUMENT: %s (%s)\n%s\n\nScore 0-10:",
		q.query, title, doc.SourceRef, core.TruncateRunes(doc.Content, rerankExcerptChars))
	resp, err := q.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Rerank.Template},
			{Role: llm.RoleUser, Content: user},
		},
		Temperature: rerankTemp,
		MaxTokens:   rerankMaxTokens,
	})
	if err != nil {
		return 0, nil, err
	}
	return parseScore(resp.Content), resp, nil
}

// parseScore reads the leading integer 0–10 from the model output, clamped.
// Unparseable output scores 0 (sorted below any real score, above unscored).
func parseScore(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	if n > rerankMaxScore {
		n = rerankMaxScore
	}
	return n
}
