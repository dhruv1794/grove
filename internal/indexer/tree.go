package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"grove/internal/core"
	"grove/prompts"
)

// bnode is a node in the in-memory tree built before persistence. A bnode
// with a non-nil doc is a leaf; otherwise it is an internal grouping node.
type bnode struct {
	id       string
	treeID   string
	parentID string
	name     string // last path segment; set for internal nodes only
	depth    int
	doc      *core.Document // non-nil → leaf node
	children []*bnode

	// filled during processing:
	contentHash string
	payloadPath string
	title       string
	summary     string
}

// nodeID/topicSep/topicNodeID delegate to core, the single owner of the
// node-id grammar (see core.NodeID). Kept as local names so the build call
// sites read unchanged.
func nodeID(treeID, path string) string { return core.NodeID(treeID, path) }

const topicSep = core.TopicSep

func topicNodeID(parentID, hash string) string { return core.TopicNodeID(parentID, hash) }

// assembleTree builds the in-memory tree for one source: an internal node per
// directory segment of each document's native Hierarchy, with a leaf node per
// document. The returned bnode is the tree root.
func assembleTree(treeID string, docs []core.Document) *bnode {
	root := &bnode{id: nodeID(treeID, ""), treeID: treeID, depth: 0}
	dirs := map[string]*bnode{"": root}
	for i := range docs {
		d := &docs[i]
		parent := root
		segs := make([]string, 0, len(d.Hierarchy))
		for _, seg := range d.Hierarchy {
			segs = append(segs, seg)
			key := strings.Join(segs, "/")
			n, ok := dirs[key]
			if !ok {
				n = &bnode{
					id:       nodeID(treeID, key),
					treeID:   treeID,
					parentID: parent.id,
					name:     seg,
					depth:    len(segs),
				}
				dirs[key] = n
				parent.children = append(parent.children, n)
			}
			parent = n
		}
		leaf := &bnode{
			id:       nodeID(treeID, "doc:"+d.ID),
			treeID:   treeID,
			parentID: parent.id,
			depth:    parent.depth + 1,
			doc:      d,
		}
		parent.children = append(parent.children, leaf)
	}
	return root
}

// hashNode computes a node's content hash: for a leaf, over the document's
// raw-artifact hash; for an internal node, over its children's content
// hashes. It is deliberately path-independent — an unchanged document moved
// elsewhere in the tree still hits the node cache. Children must be hashed
// first (post-order).
func hashNode(n *bnode) string {
	h := sha256.New()
	if n.doc != nil {
		h.Write([]byte("doc\x00"))
		h.Write([]byte(n.doc.Hash))
	} else {
		h.Write([]byte("group\x00"))
		hs := make([]string, len(n.children))
		for i, c := range n.children {
			hs[i] = c.contentHash
		}
		sort.Strings(hs)
		for _, s := range hs {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// collectNodes flattens the bnode tree into persistable core.Node rows.
func (b *builder) collectNodes(n *bnode, builtAt time.Time, out *[]core.Node) {
	cn := core.Node{
		ID:          n.id,
		TreeID:      n.treeID,
		ParentID:    n.parentID,
		Title:       n.title,
		Summary:     n.summary,
		Depth:       n.depth,
		ContentHash: n.contentHash,
		PayloadPath: n.payloadPath,
		PromptVer:   prompts.Node.Ver(),
		BuiltBy:     b.model,
		BuiltAt:     builtAt,
	}
	for _, c := range n.children {
		cn.Children = append(cn.Children, c.id)
	}
	if n.doc != nil {
		cn.DocIDs = []string{n.doc.ID}
	}
	*out = append(*out, cn)
	for _, c := range n.children {
		b.collectNodes(c, builtAt, out)
	}
}
