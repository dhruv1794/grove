package query

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/prompts"
)

const (
	pruneExcerptChars  = 1500 // per-document text shown to the filter
	pruneConcurrency   = 8    // bound on in-flight binary judgments
	pruneMaxTokens     = 4    // YES/NO needs almost nothing
	pruneTemp          = 0.0  // a confident precision filter, not stochastic
)

// prune is the binary relevance stage (the "could this answer the question?"
// filter) that runs after RRF fusion. Each candidate document gets an
// independent YES/NO judgment — isolated binary calls stay robust on small
// models where open-ended navigation degrades. Kept docs preserve fused order.
//
// It is a no-op without a model or when disabled. If every candidate is
// rejected (e.g. a degenerate model answering NO to all), it falls back to the
// unpruned candidates rather than returning nothing.
func (q *querier) prune(ctx context.Context, docIDs []string) ([]string, error) {
	if !q.opts.Prune || q.deps.LLM == nil || len(docIDs) == 0 {
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

	keep := make([]bool, len(docIDs))
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	sem := make(chan struct{}, pruneConcurrency)
	for i, id := range docIDs {
		doc, ok := byID[id]
		if !ok {
			continue // a fused ID with no document; drop it
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, doc core.Document) {
			defer wg.Done()
			defer func() { <-sem }()
			keepIt, resp, err := q.judge(ctx, doc)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			q.tally.Add(resp)
			keep[i] = keepIt
		}(i, doc)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	kept := make([]string, 0, len(docIDs))
	for i, id := range docIDs {
		if keep[i] {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return docIDs, nil // never strand the query with zero candidates
	}
	return kept, nil
}

// judge runs one binary YES/NO relevance call for a single document.
func (q *querier) judge(ctx context.Context, doc core.Document) (bool, *llm.Response, error) {
	title := doc.Title
	if title == "" {
		title = doc.SourceRef
	}
	user := fmt.Sprintf("QUESTION: %s\n\nDOCUMENT: %s (%s)\n%s\n\nCould this document help answer the question? Answer YES or NO.",
		q.query, title, doc.SourceRef, core.TruncateRunes(doc.Content, pruneExcerptChars))
	resp, err := q.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Prune.Template},
			{Role: llm.RoleUser, Content: user},
		},
		Temperature: pruneTemp,
		MaxTokens:   pruneMaxTokens,
	})
	if err != nil {
		return false, nil, err
	}
	return parseJudgment(resp.Content), resp, nil
}

// parseJudgment reads the binary verdict. Anything that isn't an affirmative
// "yes" is treated as a reject — but the all-rejected fallback in prune guards
// against a model that never says yes.
func parseJudgment(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "yes") || s == "y"
}
