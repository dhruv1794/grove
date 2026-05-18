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
	var build func(n core.Node, level int) *TreeNode
	build = func(n core.Node, level int) *TreeNode {
		tn := &TreeNode{
			ID: n.ID, Title: n.Title, Depth: n.Depth,
			IsTarget: slices.Contains(n.DocIDs, targetDocID),
		}
		if maxDepth == 0 || level < maxDepth {
			for _, c := range children[n.ID] {
				tn.Children = append(tn.Children, build(c, level+1))
			}
		}
		return tn
	}
	return build(root, 0)
}
