package store

import (
	"context"
	"time"

	"grove/internal/core"
)

// Store is the persistence boundary. Default implementation: SQLite + filesystem JSON.
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
	GetDocument(ctx context.Context, id string) (*core.Document, error)
	GetDocuments(ctx context.Context, ids []string) ([]core.Document, error)
	FindDocuments(ctx context.Context, ref string) ([]core.Document, error)
	ListDocumentsBySource(ctx context.Context, source string) ([]core.Document, error)
	SearchDocuments(ctx context.Context, query, source string, limit int) ([]string, error)
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
}
