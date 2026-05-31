package core

// Node identity (documents/03 §"Node identity rule"): a node's stable logical
// id is its tree id joined to its canonical path within that tree. This is the
// single owner of that grammar — the indexer builds ids with it, the query
// layer reconstructs cited-leaf ids with it, and the web graph matches against
// it. Changing the scheme here changes it everywhere (the one place the web
// client mirrors, web/src/api.ts:leafNodeId, must track this).

// NodeID joins a tree id and a canonical path within the tree. The empty path
// is the tree root.
func NodeID(treeID, path string) string { return treeID + ":" + path }

// LeafNodeID is the id of the leaf node holding a document; its canonical path
// is "doc:<docID>".
func LeafNodeID(treeID, docID string) string { return NodeID(treeID, "doc:"+docID) }

// TopicSep separates a parent node id from a grouping-derived suffix in a topic
// node's id (e.g. "notes:dir/topic-ab12cd34"); a topic node is an LLM-clustered
// hub rather than a native directory.
const TopicSep = "/topic-"

// TopicNodeID joins a parent node id to a grouping-hash suffix.
func TopicNodeID(parentID, hash string) string { return parentID + TopicSep + hash }
