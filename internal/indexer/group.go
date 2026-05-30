package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"grove/internal/core"
	"grove/internal/llm"
	"grove/prompts"
)

const (
	// groupThreshold is the minimum number of sibling leaf nodes (under a node
	// with no subdirectories) that triggers LLM topic grouping. Below it, a flat
	// folder is left as-is.
	groupThreshold = 8
	groupSnippet   = 200 // per-document snippet chars shown to the grouping model
	groupMaxTokens = 512
	// groupBatchSize caps how many leaves go into one grouping call. A larger
	// flat group is split into batches so the bounded output (groupMaxTokens) can
	// enumerate every member assignment — without this a hundreds-of-files flat
	// corpus truncates the response and yields no split at all (a flat tree).
	// Topics are batch-local (a coherent cluster split across batches becomes two
	// topic nodes); acceptable for v1, a second-level merge can refine later.
	groupBatchSize = 40
)

// docGroup is one topical cluster: a title and the documents assigned to it.
type docGroup struct {
	Title  string   `json:"title"`
	DocIDs []string `json:"doc_ids"`
}

// groupPayload is the cached grouping decision for a flat node, keyed on disk
// by the content hash of its members so an unchanged rebuild reuses the same
// clustering — deterministic and free.
type groupPayload struct {
	SchemaVersion int        `json:"schema_version"`
	PromptVer     string     `json:"prompt_ver"`
	BuildModel    string     `json:"build_model"`
	Groups        []docGroup `json:"groups"`
}

const groupSchemaVersion = 1

// regroup walks the assembled tree and, for any node whose children are all
// leaves and number at least groupThreshold, replaces those leaves with LLM-
// generated topical sub-nodes. It recurses into existing directory nodes first,
// so a flat sub-folder is grouped in place. Single-level only: the topic nodes
// it creates are not themselves regrouped.
func (b *builder) regroup(ctx context.Context, n *bnode) error {
	for _, c := range n.children {
		if c.doc == nil {
			if err := b.regroup(ctx, c); err != nil {
				return err
			}
		}
	}
	if len(n.children) < groupThreshold {
		return nil
	}
	for _, c := range n.children {
		if c.doc == nil {
			return nil // not a flat group (has a sub-collection); leave it
		}
	}

	groups, err := b.clusterLeaves(ctx, n)
	if err != nil {
		return err
	}
	if len(groups) <= 1 {
		return nil // no useful split; keep the flat list
	}
	formed := applyGroups(n, groups)
	b.tally(func(r *Result) { r.Groups += formed })
	return nil
}

// clusterLeaves returns a topical grouping of n's leaf children, from the cache
// when the same members were grouped before, otherwise from a model call whose
// result is cached.
func (b *builder) clusterLeaves(ctx context.Context, n *bnode) ([]docGroup, error) {
	leaves := append([]*bnode(nil), n.children...)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].doc.ID < leaves[j].doc.ID })

	path := core.PayloadPath(b.deps.Layout.Trees, groupKey(leaves),
		"group__"+slug(b.model)+".json")
	if !b.opts.Rebuild {
		if gp, err := readGroupPayload(path); err == nil {
			return gp.Groups, nil
		}
	}

	var groups []docGroup
	if len(leaves) <= groupBatchSize {
		g, err := b.groupCall(ctx, n.id, leaves)
		if err != nil {
			return nil, err
		}
		groups = g
	} else {
		// Split a large flat group into bounded batches so the response can
		// enumerate every assignment; union the per-batch topics. A batch the
		// model declines to split (parseGroups returns its single all-docs
		// sentinel) contributes no topic — those leaves stay directly under n.
		for start := 0; start < len(leaves); start += groupBatchSize {
			end := min(start+groupBatchSize, len(leaves))
			bg, err := b.groupCall(ctx, n.id, leaves[start:end])
			if err != nil {
				return nil, err
			}
			if len(bg) > 1 {
				groups = append(groups, bg...)
			}
		}
		if len(groups) == 0 {
			all := make([]string, len(leaves))
			for i, c := range leaves {
				all[i] = c.doc.ID
			}
			groups = []docGroup{{DocIDs: all}} // sentinel: no useful split
		}
	}
	// Cache the decision even when the model declined to split (a single
	// all-docs group), so a rebuild reuses it instead of paying for the call
	// again — the unchanged-rebuild-is-free invariant holds for grouping too.
	if err := core.WriteJSONAtomic(path, &groupPayload{
		SchemaVersion: groupSchemaVersion,
		PromptVer:     prompts.Group.Ver(),
		BuildModel:    b.model,
		Groups:        groups,
	}); err != nil {
		return nil, err
	}
	return groups, nil
}

// groupCall runs one topic-grouping model call over the given leaves and parses
// the result into docGroups. Used once for a small flat group, or per batch for
// a large one.
func (b *builder) groupCall(ctx context.Context, nodeID string, leaves []*bnode) ([]docGroup, error) {
	var sb strings.Builder
	sb.WriteString("DOCUMENTS:\n")
	for i, c := range leaves {
		title := c.doc.Title
		if title == "" {
			title = filepath.Base(c.doc.SourceRef)
		}
		body, err := b.deps.Store.GetDocumentContent(ctx, c.doc.Hash)
		if err != nil {
			return nil, fmt.Errorf("load content for %s: %w", c.doc.ID, err)
		}
		fmt.Fprintf(&sb, "%d. %s — %s\n", i+1, title, snippet(body))
	}
	resp, err := b.deps.LLM.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompts.Group.Template},
			{Role: llm.RoleUser, Content: sb.String()},
		},
		Temperature: requestTemp,
		MaxTokens:   groupMaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("group node %s: %w", nodeID, err)
	}
	b.tally(func(r *Result) { r.Tally.Add(resp) })
	return parseGroups(resp.Content, leaves), nil
}

// applyGroups rewrites n's children: each group becomes a topic node holding
// its member leaves; leaves no group claimed stay directly under n. Returns the
// number of topic nodes created. Leaf node IDs are path-independent, so
// reparenting changes only their ParentID/Depth, not their identity.
func applyGroups(n *bnode, groups []docGroup) int {
	leafByDoc := make(map[string]*bnode, len(n.children))
	for _, c := range n.children {
		if c.doc != nil {
			leafByDoc[c.doc.ID] = c
		}
	}

	grouped := map[string]bool{}
	var newChildren []*bnode
	formed := 0
	for _, g := range groups {
		var members []*bnode
		for _, did := range g.DocIDs {
			if leaf, ok := leafByDoc[did]; ok && !grouped[did] {
				members = append(members, leaf)
				grouped[did] = true
			}
		}
		if len(members) == 0 {
			continue
		}
		topic := &bnode{
			id:       topicNodeID(n.id, topicHash(members)),
			treeID:   n.treeID,
			parentID: n.id,
			name:     g.Title,
			depth:    n.depth + 1,
		}
		for _, leaf := range members {
			leaf.parentID = topic.id
			leaf.depth = topic.depth + 1
			topic.children = append(topic.children, leaf)
		}
		newChildren = append(newChildren, topic)
		formed++
	}
	for _, c := range n.children {
		if c.doc != nil && !grouped[c.doc.ID] {
			newChildren = append(newChildren, c)
		}
	}
	n.children = newChildren
	return formed
}

// topicHash derives a stable suffix for a topic node id from its members' doc
// ids, so the same cluster keeps the same node id across rebuilds regardless of
// the topic title the model picked.
func topicHash(members []*bnode) string {
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.doc.ID
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	return hex.EncodeToString(sum[:])[:8]
}

// groupKey is the cache key for a node's grouping decision: the prompt version,
// build model, and each member's (doc id, content hash). Any membership or
// content change re-clusters; an unchanged set reuses the cached grouping.
func groupKey(leaves []*bnode) string {
	h := sha256.New()
	h.Write([]byte(prompts.Group.Ver() + "\x00"))
	for _, c := range leaves {
		fmt.Fprintf(h, "%s\x00%s\x00", c.doc.ID, c.doc.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parseGroups extracts groups from the model response and maps the 1-based
// document numbers back to doc ids. Out-of-range and duplicate numbers are
// dropped (first group wins); empty groups are skipped. A response that yields
// fewer than two non-empty groups returns a single all-docs group, signalling
// "no useful split" to the caller.
func parseGroups(content string, leaves []*bnode) []docGroup {
	var out []docGroup
	if span, ok := core.JSONObjectSpan(content); ok {
		var parsed struct {
			Groups []struct {
				Title   string `json:"title"`
				Members []int  `json:"members"`
			} `json:"groups"`
		}
		if err := json.Unmarshal([]byte(span), &parsed); err == nil {
			claimed := make([]bool, len(leaves))
			for _, g := range parsed.Groups {
				var ids []string
				for _, m := range g.Members {
					k := m - 1
					if k >= 0 && k < len(leaves) && !claimed[k] {
						claimed[k] = true
						ids = append(ids, leaves[k].doc.ID)
					}
				}
				if len(ids) > 0 {
					out = append(out, docGroup{Title: strings.TrimSpace(g.Title), DocIDs: ids})
				}
			}
		}
	}
	if len(out) <= 1 {
		all := make([]string, len(leaves))
		for i, c := range leaves {
			all[i] = c.doc.ID
		}
		return []docGroup{{Title: "", DocIDs: all}}
	}
	return out
}

func snippet(content string) string {
	s := strings.Join(strings.Fields(content), " ")
	return core.TruncateRunes(s, groupSnippet)
}

func readGroupPayload(path string) (*groupPayload, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var gp groupPayload
	if err := json.Unmarshal(b, &gp); err != nil {
		return nil, fmt.Errorf("group payload %s: %w", path, err)
	}
	return &gp, nil
}
