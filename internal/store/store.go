package store

import (
	"context"
	"time"

	"grove/internal/core"
)

// Store is the persistence boundary. Default implementation: SQLite + filesystem JSON.
// Hit is a ranked search result carrying its raw retriever score (bm25 for FTS,
// cosine for vectors). Used by the scored search variants for diagnostics.
type Hit struct {
	ID    string
	Score float64
}

type Store interface {
	// Lifecycle
	Open(ctx context.Context) error
	Close() error
	Migrate(ctx context.Context) error

	// Sources
	UpsertSource(ctx context.Context, s core.Source) error
	GetSource(ctx context.Context, name string) (*core.Source, error)
	ListSources(ctx context.Context) ([]core.Source, error)
	DeleteSource(ctx context.Context, name string) (int, error)
	TouchSourceSync(ctx context.Context, name string, t time.Time) error

	// Collections
	UpsertCollection(ctx context.Context, c core.Collection) error
	ListCollections(ctx context.Context, source string) ([]core.Collection, error)

	// Documents
	UpsertDocument(ctx context.Context, d core.Document) error
	UpsertDocuments(ctx context.Context, docs []core.Document) error
	DeleteDocuments(ctx context.Context, ids []string) (int, error)
	GetDocument(ctx context.Context, id string) (*core.Document, error)
	GetDocuments(ctx context.Context, ids []string) ([]core.Document, error)
	FindDocuments(ctx context.Context, ref string) ([]core.Document, error)
	ListDocumentMetaBySource(ctx context.Context, source string) ([]core.Document, error)
	GetDocumentContent(ctx context.Context, hash string) (string, error)
	ListDocumentDigests(ctx context.Context, source string) (map[string]string, error)
	SearchDocuments(ctx context.Context, query, source string, limit int) ([]string, error)
	// Scored variant: same ranking, but carries each hit's raw retriever score
	// (bm25 for FTS — lower is better) for diagnostics / router features.
	SearchDocumentsScored(ctx context.Context, query, source string, limit int) ([]Hit, error)

	// Embeddings (semantic retrieval)
	UpsertEmbedding(ctx context.Context, docID, source, model string, vec []float32) error
	CountEmbeddings(ctx context.Context, source string) (int, error)
	EmbeddingModels(ctx context.Context, source string) ([]string, error)
	SearchByVector(ctx context.Context, query []float32, source string, k int) ([]string, error)
	// Scored variant: same ranking, with each hit's cosine similarity (higher is
	// better) for diagnostics / router features.
	SearchByVectorScored(ctx context.Context, query []float32, source string, k int) ([]Hit, error)
	UpsertNodeEmbedding(ctx context.Context, nodeID, treeID, source, model string, vec []float32) error
	CountNodeEmbeddings(ctx context.Context, source string) (int, error)
	SearchNodesByVector(ctx context.Context, query []float32, source string, k int) ([]NodeVecHit, error)
	UpsertChunkEmbedding(ctx context.Context, docID string, chunkIdx int, source, model string, vec []float32) error
	DeleteChunkEmbeddingsByDoc(ctx context.Context, docID string) error
	CountChunkEmbeddings(ctx context.Context, source string) (int, error)
	SearchByChunkVector(ctx context.Context, query []float32, source string, k int) ([]string, error)
	CountDocuments(ctx context.Context, source string) (int, error)
	CountDocumentsBySource(ctx context.Context) (map[string]int, error)

	// Trees + nodes
	UpsertTree(ctx context.Context, t core.Tree) error
	ListTrees(ctx context.Context) ([]core.Tree, error)
	UpsertNode(ctx context.Context, n core.Node) error
	UpsertNodes(ctx context.Context, nodes []core.Node) error
	GetNode(ctx context.Context, id string) (*core.Node, error)
	ListNodesByTree(ctx context.Context, treeID string) ([]core.Node, error)
	GetNodesByContentHash(ctx context.Context, contentHash string) ([]core.Node, error)
	DeleteNodesByTree(ctx context.Context, treeID string) (int, error)
	AddNodeSeeAlso(ctx context.Context, edges map[string][]core.NodeRef) error

	// Doc links
	GetDocLinks(ctx context.Context, docID string) ([]core.DocLink, error)
	ListDocLinksBySource(ctx context.Context, source string) (map[string][]core.DocLink, error)

	// Build history
	RecordBuild(ctx context.Context, r core.BuildRecord) error
	ListBuildHistory(ctx context.Context, limit int) ([]core.BuildRecord, error)
}
