package grove

import (
	"context"

	"grove/internal/core"
)

// NodeSummary identifies a node for navigation: its stable id, title, and
// summary. Behind grove_navigate / the grove://node resource.
type NodeSummary struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
	TreeID  string `json:"tree_id,omitempty"`
}

// NodeChild is one child in a one-level navigation step. HasChildren lets a
// client decide whether to descend further; DocCount is the leaf-document count
// in the child's subtree.
type NodeChild struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary,omitempty"`
	HasChildren bool   `json:"has_children"`
	DocCount    int    `json:"doc_count"`
}

// NodeNav is a node plus a one-level view of its children — one descent step.
type NodeNav struct {
	Current  NodeSummary `json:"current_node"`
	Children []NodeChild `json:"children"`
}

// NodeDoc is one document backing a node.
type NodeDoc struct {
	DocID     string `json:"doc_id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	SourceRef string `json:"source_ref"`
	Content   string `json:"content"`
}

// NodeRead is the documents backing a node, with the node's identity.
type NodeRead struct {
	Node      NodeSummary `json:"node"`
	Documents []NodeDoc   `json:"documents"`
}

// NodeLink is one cross-link edge out of a node into a (usually other) tree.
type NodeLink struct {
	NodeID   string  `json:"node_id"`
	TreeID   string  `json:"tree_id"`
	Title    string  `json:"title"`
	Reason   string  `json:"reason,omitempty"`
	Strength float32 `json:"strength"`
}

// Navigate returns one node and a single level of its children. An empty nodeID
// starts at the tree's root. Behind the grove_navigate tool. The node id is the
// stable core.Node.ID, not a content hash (see 03/05).
func (g *Grove) Navigate(ctx context.Context, treeID, nodeID string) (*NodeNav, error) {
	if treeID == "" {
		return nil, core.NewError(core.KindMisuse,
			"empty tree id",
			"pass a tree_id from grove_list_trees")
	}
	tree, nodes, err := g.treeNodes(ctx, treeID)
	if err != nil {
		return nil, err
	}
	target := nodeID
	if target == "" {
		target = tree.RootNodeID
	}
	byID := make(map[string]core.Node, len(nodes))
	childIDs := map[string][]string{}
	for _, n := range nodes {
		byID[n.ID] = n
		if n.ParentID != "" {
			childIDs[n.ParentID] = append(childIDs[n.ParentID], n.ID)
		}
	}
	cur, ok := byID[target]
	if !ok {
		return nil, core.NewError(core.KindMisuse,
			"no node "+target+" in tree "+treeID,
			"navigate from the tree root (omit node_id) or use an id returned by a prior step")
	}
	nav := &NodeNav{Current: nodeSummary(cur)}
	for _, cid := range childIDs[cur.ID] {
		c := byID[cid]
		nav.Children = append(nav.Children, NodeChild{
			ID: c.ID, Title: nodeTitle(c), Summary: summaryOf(c),
			HasChildren: len(childIDs[c.ID]) > 0,
			DocCount:    subtreeDocCount(c.ID, byID, childIDs),
		})
	}
	return nav, nil
}

// NodeByID returns a node and one level of its children, resolved from the node
// id alone (the id encodes its tree). Behind the grove://node resource.
func (g *Grove) NodeByID(ctx context.Context, nodeID string) (*NodeNav, error) {
	node, err := g.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, core.NewError(core.KindMisuse,
			"no node "+nodeID, "use a node id from grove_navigate or grove_list_trees")
	}
	return g.Navigate(ctx, node.TreeID, nodeID)
}

// NodeDocuments returns the document(s) backing a node, each content truncated
// to maxChars runes (0 = untruncated). For a hub node (no docs of its own) it
// gathers the leaf documents in its subtree. Behind grove_read.
func (g *Grove) NodeDocuments(ctx context.Context, nodeID string, maxChars int) (*NodeRead, error) {
	node, err := g.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, core.NewError(core.KindMisuse,
			"no node "+nodeID, "use a node id from grove_navigate")
	}
	ids := node.DocIDs
	if len(ids) == 0 {
		_, nodes, err := g.treeNodes(ctx, node.TreeID)
		if err != nil {
			return nil, err
		}
		ids = subtreeDocIDs(nodeID, nodes)
	}
	out := &NodeRead{Node: nodeSummary(*node)}
	if len(ids) == 0 {
		return out, nil
	}
	docs, err := g.store.GetDocuments(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		content := d.Content
		if maxChars > 0 {
			content = core.TruncateRunes(content, maxChars)
		}
		out.Documents = append(out.Documents, NodeDoc{
			DocID: d.ID, Title: d.Title, Source: d.Source,
			SourceRef: d.SourceRef, Content: content,
		})
	}
	return out, nil
}

// NodeLinks returns the cross-link edges out of a node whose strength is at
// least minStrength, resolving each target's title and tree. Behind grove_links.
func (g *Grove) NodeLinks(ctx context.Context, nodeID string, minStrength float32) ([]NodeLink, error) {
	node, err := g.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, core.NewError(core.KindMisuse,
			"no node "+nodeID, "use a node id from grove_navigate")
	}
	out := []NodeLink{}
	for _, ref := range node.SeeAlso {
		if ref.Strength < minStrength {
			continue
		}
		link := NodeLink{
			NodeID: ref.NodeID, TreeID: ref.TreeID,
			Reason: ref.Reason, Strength: ref.Strength,
		}
		// Resolve the target's title; skip a dangling edge's title rather than fail.
		if tn, err := g.store.GetNode(ctx, ref.NodeID); err == nil && tn != nil {
			link.Title = nodeTitle(*tn)
			if link.TreeID == "" {
				link.TreeID = tn.TreeID
			}
		}
		out = append(out, link)
	}
	return out, nil
}

// treeNodes loads a tree and its nodes by tree id, erroring with misuse when the
// tree is unknown.
func (g *Grove) treeNodes(ctx context.Context, treeID string) (core.Tree, []core.Node, error) {
	trees, err := g.store.ListTrees(ctx)
	if err != nil {
		return core.Tree{}, nil, err
	}
	for _, t := range trees {
		if t.ID == treeID {
			nodes, err := g.store.ListNodesByTree(ctx, treeID)
			if err != nil {
				return core.Tree{}, nil, err
			}
			return t, nodes, nil
		}
	}
	return core.Tree{}, nil, core.NewError(core.KindMisuse,
		"no tree with id "+treeID,
		"run grove_list_trees to list built trees")
}

func nodeSummary(n core.Node) NodeSummary {
	return NodeSummary{ID: n.ID, Title: nodeTitle(n), Summary: summaryOf(n), TreeID: n.TreeID}
}

// summaryOf returns a node's abstractive summary. The summary lives in the
// on-disk node payload (not the DB row), so this reads it best-effort, falling
// back to any summary already on the node (set in tests, empty after the row
// scan). An un-summarized or payload-less node yields "".
func summaryOf(n core.Node) string {
	if n.PayloadPath != "" {
		if p, err := core.ReadNodePayload(n.PayloadPath); err == nil && p != nil {
			return p.Summary
		}
	}
	return n.Summary
}

// subtreeDocCount sums the leaf-document counts under id (inclusive).
func subtreeDocCount(id string, byID map[string]core.Node, childIDs map[string][]string) int {
	total := len(byID[id].DocIDs)
	for _, c := range childIDs[id] {
		total += subtreeDocCount(c, byID, childIDs)
	}
	return total
}

// subtreeDocIDs collects the document ids of every node in the subtree rooted at
// rootID, deduped, preserving first-seen order.
func subtreeDocIDs(rootID string, nodes []core.Node) []string {
	childIDs := map[string][]string{}
	byID := make(map[string]core.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
		if n.ParentID != "" {
			childIDs[n.ParentID] = append(childIDs[n.ParentID], n.ID)
		}
	}
	var ids []string
	seen := map[string]bool{}
	visiting := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		if visiting[id] { // guard a malformed cycle
			return
		}
		visiting[id] = true
		for _, docID := range byID[id].DocIDs {
			if !seen[docID] {
				seen[docID] = true
				ids = append(ids, docID)
			}
		}
		for _, c := range childIDs[id] {
			walk(c)
		}
	}
	walk(rootID)
	return ids
}
