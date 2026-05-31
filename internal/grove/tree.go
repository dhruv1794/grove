package grove

import (
	"context"
	"slices"

	"grove/internal/core"
)

// TreeNode is one node of a rendered tree, with its children nested.
type TreeNode struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Depth    int         `json:"depth"`
	IsTarget bool        `json:"is_target,omitempty"` // holds the queried document
	Children []*TreeNode `json:"children,omitempty"`
}

// TreeView places one document within one tree of the forest. Root is the
// tree's root node; the node(s) holding the document have IsTarget set.
type TreeView struct {
	DocID     string    `json:"doc_id"`
	DocTitle  string    `json:"doc_title"`
	Source    string    `json:"source"`
	SourceRef string    `json:"source_ref"`
	Tree      string    `json:"tree"`
	Root      *TreeNode `json:"root"`
}

// ForestTree is one whole tree of the forest rendered as a nested node
// structure, with no target document. It carries the tree's identity and
// counts for the forest/API view (vs TreeView, which places one document).
type ForestTree struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	DocCount  int       `json:"doc_count"`
	NodeCount int       `json:"node_count"`
	Root      *TreeNode `json:"root"`
}

// ForestView returns every built tree as a nested node structure. depth caps
// the rendered depth below each root (0 = full). It is the read model behind
// the web `/api/trees` endpoint and a no-argument `grove tree`.
func (g *Grove) ForestView(ctx context.Context, depth int) ([]ForestTree, error) {
	trees, err := g.store.ListTrees(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ForestTree, 0, len(trees))
	for _, t := range trees {
		ft, err := g.forestTree(ctx, t, depth)
		if err != nil {
			return nil, err
		}
		if ft != nil {
			out = append(out, *ft)
		}
	}
	return out, nil
}

// TreeByID returns one built tree by its ID as a nested node structure, or a
// misuse error if no tree has that ID. depth caps the rendered depth below the
// root (0 = full). Behind the web `/api/tree/:id` endpoint.
func (g *Grove) TreeByID(ctx context.Context, treeID string, depth int) (*ForestTree, error) {
	trees, err := g.store.ListTrees(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range trees {
		if t.ID == treeID {
			ft, err := g.forestTree(ctx, t, depth)
			if err != nil {
				return nil, err
			}
			if ft == nil {
				break
			}
			return ft, nil
		}
	}
	return nil, core.NewError(core.KindMisuse,
		"no tree with id "+treeID,
		"run `grove status` or GET /api/trees to list built trees")
}

// forestTree renders one tree's nodes into a ForestTree (no target document).
// Returns nil when the tree has no nodes (root not found).
func (g *Grove) forestTree(ctx context.Context, t core.Tree, depth int) (*ForestTree, error) {
	nodes, err := g.store.ListNodesByTree(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	root := buildTreeView(nodes, t.RootNodeID, "", depth)
	if root == nil {
		return nil, nil
	}
	return &ForestTree{
		ID: t.ID, Name: t.Name, Source: t.Source,
		DocCount: t.DocCount, NodeCount: t.NodeCount, Root: root,
	}, nil
}

// Tree resolves a document by ID or source path and returns its position in
// every built tree that contains it. depth caps the rendered tree depth
// (levels below the root); 0 renders the full tree.
func (g *Grove) Tree(ctx context.Context, ref string, depth int) ([]TreeView, error) {
	if ref == "" {
		return nil, core.NewError(core.KindMisuse,
			"empty document reference",
			"pass a document ID or source path, e.g. grove tree auth.md")
	}
	docs, err := g.store.FindDocuments(ctx, ref)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, core.NewError(core.KindMisuse,
			"no document matches "+ref,
			"pass a document ID or a source path; run `grove status` to list sources")
	}
	trees, err := g.store.ListTrees(ctx)
	if err != nil {
		return nil, err
	}
	var views []TreeView
	for _, d := range docs {
		for _, t := range trees {
			if t.Source != d.Source {
				continue
			}
			nodes, err := g.store.ListNodesByTree(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			root := buildTreeView(nodes, t.RootNodeID, d.ID, depth)
			if root == nil {
				continue
			}
			views = append(views, TreeView{
				DocID: d.ID, DocTitle: d.Title, Source: d.Source,
				SourceRef: d.SourceRef, Tree: t.Name, Root: root,
			})
		}
	}
	if len(views) == 0 {
		return nil, core.NewError(core.KindNoSources,
			"no built tree contains "+ref,
			"run `grove build` to index this document's source")
	}
	return views, nil
}

// buildTreeView assembles the nested TreeNode rooted at rootID, marking nodes
// that hold targetDocID. maxDepth caps levels below the root; 0 is unlimited.
// Returns nil if rootID is not among nodes.
func buildTreeView(nodes []core.Node, rootID, targetDocID string, maxDepth int) *TreeNode {
	byID := make(map[string]core.Node, len(nodes))
	children := map[string][]core.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
		if n.ParentID != "" {
			children[n.ParentID] = append(children[n.ParentID], n)
		}
	}
	root, ok := byID[rootID]
	if !ok {
		return nil
	}
	// seen guards the recursion against a malformed tree (a parent↔child cycle
	// or a node reachable by two paths): without it a cycle is unbounded
	// recursion → stack overflow → a 500. A revisited node is simply skipped.
	seen := map[string]bool{}
	var build func(n core.Node, level int) *TreeNode
	build = func(n core.Node, level int) *TreeNode {
		if seen[n.ID] {
			return nil
		}
		seen[n.ID] = true
		tn := &TreeNode{
			ID: n.ID, Title: n.Title, Depth: n.Depth,
			IsTarget: slices.Contains(n.DocIDs, targetDocID),
		}
		if maxDepth == 0 || level < maxDepth {
			for _, c := range children[n.ID] {
				if child := build(c, level+1); child != nil {
					tn.Children = append(tn.Children, child)
				}
			}
		}
		return tn
	}
	return build(root, 0)
}
