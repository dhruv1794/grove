package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/prompts"
)

// option is one navigable choice presented to the model: a tree, or a node's
// child.
type option struct{ title, summary string }

// loadedTree is a tree's nodes indexed for descent, with a lazy payload cache.
type loadedTree struct {
	tree     core.Tree
	byID     map[string]core.Node
	children map[string][]string
	payloads map[string]*core.NodePayload
}

func (q *querier) loadTree(ctx context.Context, t core.Tree) (*loadedTree, error) {
	nodes, err := q.deps.Store.ListNodesByTree(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	lt := &loadedTree{
		tree:     t,
		byID:     make(map[string]core.Node, len(nodes)),
		children: map[string][]string{},
		payloads: map[string]*core.NodePayload{},
	}
	for _, n := range nodes {
		lt.byID[n.ID] = n
		if n.ParentID != "" {
			lt.children[n.ParentID] = append(lt.children[n.ParentID], n.ID)
		}
	}
	return lt, nil
}

// summary returns a node's title and summary, reading its payload lazily. A
// missing payload falls back to the SQLite title and an empty summary.
func (q *querier) summary(lt *loadedTree, nodeID string) (title, summary string) {
	n := lt.byID[nodeID]
	title = n.Title
	p, ok := lt.payloads[nodeID]
	if !ok {
		p, _ = core.ReadNodePayload(n.PayloadPath)
		lt.payloads[nodeID] = p // cached even when nil — a miss stays a miss
	}
	if p != nil {
		if p.Title != "" {
			title = p.Title
		}
		summary = p.Summary
	}
	return title, summary
}

// descend walks one tree from nodeID, asking the model which children to
// explore at each internal node, and appends every reached leaf to leaves.
func (q *querier) descend(ctx context.Context, lt *loadedTree, nodeID string, depth int, leaves *[]string) error {
	kids := lt.children[nodeID]
	if len(kids) == 0 {
		*leaves = append(*leaves, nodeID)
		return nil
	}
	if depth >= q.opts.MaxDepth {
		collectLeaves(lt, nodeID, leaves)
		return nil
	}
	opts := make([]option, len(kids))
	for i, kid := range kids {
		opts[i].title, opts[i].summary = q.summary(lt, kid)
	}
	chosen, reason, err := q.navigate(ctx, opts)
	if err != nil {
		return err
	}
	for _, idx := range chosen {
		kid := kids[idx]
		title, _ := q.summary(lt, kid)
		q.trace = append(q.trace, TraceStep{Tree: lt.tree.Name, Node: title, Reason: reason})
		if err := q.descend(ctx, lt, kid, depth+1, leaves); err != nil {
			return err
		}
	}
	return nil
}

// collectLeaves gathers every leaf under nodeID, used when the depth limit is
// reached at an internal node.
func collectLeaves(lt *loadedTree, nodeID string, leaves *[]string) {
	kids := lt.children[nodeID]
	if len(kids) == 0 {
		*leaves = append(*leaves, nodeID)
		return
	}
	for _, kid := range kids {
		collectLeaves(lt, kid, leaves)
	}
}

// navigate asks the model which of opts to explore, returning 0-based indices.
func (q *querier) navigate(ctx context.Context, opts []option) (chosen []int, reason string, err error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "QUESTION: %s\n\nOPTIONS:\n", q.query)
	for i, o := range opts {
		s := o.summary
		if s == "" {
			s = "(no summary)"
		}
		fmt.Fprintf(&sb, "%d. %s — %s\n", i+1, o.title, s)
	}
	resp, err := q.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Navigate.Template},
			{Role: llm.RoleUser, Content: sb.String()},
		},
		Temperature: navigateTemp,
		MaxTokens:   navigateMaxTokens,
	})
	if err != nil {
		return nil, "", err
	}
	q.tally.Add(resp)
	chosen, reason = parseChoices(resp.Content, len(opts))
	return chosen, reason, nil
}

// parseChoices extracts the chosen option indices (converted to 0-based) and
// the reason from a navigate response. Out-of-range and duplicate numbers are
// dropped; an unparseable response yields no choices.
func parseChoices(content string, n int) (idx []int, reason string) {
	i := strings.IndexByte(content, '{')
	j := strings.LastIndexByte(content, '}')
	if i < 0 || j <= i {
		return nil, ""
	}
	var out struct {
		Choices []int  `json:"choices"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(content[i:j+1]), &out); err != nil {
		return nil, ""
	}
	seen := map[int]bool{}
	for _, c := range out.Choices {
		k := c - 1 // options are presented 1-based
		if k >= 0 && k < n && !seen[k] {
			seen[k] = true
			idx = append(idx, k)
		}
	}
	return idx, strings.TrimSpace(out.Reason)
}
