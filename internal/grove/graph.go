package grove

import (
	"context"
	"strings"

	"grove/internal/core"
)

// GraphNode is one node of the forest graph. Kind is "root", "dir", "topic", or
// "leaf"; Size is the node's leaf-descendant count (1 for a leaf) so the UI can
// size hubs by how much they cover. Tree is the containing tree's id.
type GraphNode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Tree  string `json:"tree"`
	Depth int    `json:"depth"`
	Size  int    `json:"size"`
	DocID string `json:"doc_id,omitempty"` // set for leaves, for doc lookup on click
}

// GraphLink is one edge: a hierarchy edge (kind "child", parent→child) or a
// cross-link (kind "seealso", with strength) from node_seealso.
type GraphLink struct {
	Source   string  `json:"source"`
	Target   string  `json:"target"`
	Kind     string  `json:"kind"`
	Strength float32 `json:"strength,omitempty"`
}

// GraphView is the whole forest as a force-directed graph: every tree node plus
// the hierarchy and cross-link edges between them. Behind GET /api/graph.
type GraphView struct {
	Nodes []GraphNode `json:"nodes"`
	Links []GraphLink `json:"links"`
}

// GraphView returns the forest as a node/link graph: nodes are tree nodes
// (root/dir/topic hubs + doc leaves), links are hierarchy edges plus
// node_seealso cross-links (including cross-tree). Hubs carry a leaf-descendant
// Size so the UI can scale them.
func (g *Grove) GraphView(ctx context.Context) (*GraphView, error) {
	trees, err := g.store.ListTrees(ctx)
	if err != nil {
		return nil, err
	}
	gv := &GraphView{}
	known := map[string]bool{} // node ids present, to drop dangling seealso targets
	type pending struct {
		from string
		ref  core.NodeRef
	}
	var seeAlso []pending

	for _, t := range trees {
		nodes, err := g.store.ListNodesByTree(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		children := map[string][]string{}
		for _, n := range nodes {
			if n.ParentID != "" {
				children[n.ParentID] = append(children[n.ParentID], n.ID)
			}
		}
		sizes := leafSizes(nodes, children)
		for _, n := range nodes {
			known[n.ID] = true
			gn := GraphNode{
				ID: n.ID, Title: nodeTitle(n), Tree: t.ID,
				Depth: n.Depth, Size: sizes[n.ID], Kind: nodeKind(n, children),
			}
			if gn.Kind == "leaf" && len(n.DocIDs) > 0 {
				gn.DocID = n.DocIDs[0]
			}
			gv.Nodes = append(gv.Nodes, gn)
			if n.ParentID != "" {
				gv.Links = append(gv.Links, GraphLink{Source: n.ParentID, Target: n.ID, Kind: "child"})
			}
			for _, ref := range n.SeeAlso {
				seeAlso = append(seeAlso, pending{from: n.ID, ref: ref})
			}
		}
	}
	for _, p := range seeAlso {
		if known[p.ref.NodeID] {
			gv.Links = append(gv.Links, GraphLink{
				Source: p.from, Target: p.ref.NodeID, Kind: "seealso", Strength: p.ref.Strength,
			})
		}
	}
	return gv, nil
}

// nodeKind classifies a node for the graph: root (no parent), leaf (no
// children), topic (grouping-derived hub), or dir (native directory hub).
func nodeKind(n core.Node, children map[string][]string) string {
	if len(children[n.ID]) == 0 {
		return "leaf"
	}
	if n.ParentID == "" {
		return "root"
	}
	if strings.Contains(n.ID, core.TopicSep) {
		return "topic"
	}
	return "dir"
}

// nodeTitle falls back to the last path segment of the node id when the node has
// no stored title (e.g. an un-summarized rebuild).
func nodeTitle(n core.Node) string {
	if n.Title != "" {
		return n.Title
	}
	id := n.ID
	if i := strings.LastIndexAny(id, "/:"); i != -1 {
		return id[i+1:]
	}
	return id
}

// leafSizes returns each node's leaf-descendant count (1 for a leaf), computed
// bottom-up. A node not reached from any root still gets its own count.
func leafSizes(nodes []core.Node, children map[string][]string) map[string]int {
	size := map[string]int{}
	var visit func(id string) int
	visiting := map[string]bool{}
	visit = func(id string) int {
		if s, ok := size[id]; ok {
			return s
		}
		if visiting[id] { // guard against a malformed cycle
			return 0
		}
		visiting[id] = true
		kids := children[id]
		if len(kids) == 0 {
			size[id] = 1
			return 1
		}
		total := 0
		for _, k := range kids {
			total += visit(k)
		}
		size[id] = total
		return total
	}
	for _, n := range nodes {
		visit(n.ID)
	}
	return size
}
