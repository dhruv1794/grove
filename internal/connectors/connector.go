package connectors

import (
	"context"
	"time"

	"grove/internal/core"
)

type AuthMethod string

const (
	AuthNone   AuthMethod = "none"
	AuthAPIKey AuthMethod = "api_key"
	AuthOAuth  AuthMethod = "oauth"
)

type Capabilities struct {
	SupportsIncremental bool
	SupportsLinks       bool
	SupportsRichMeta    bool
	SupportsAuth        AuthMethod
	NativeFormat        string
}

type ConnectorConfig struct {
	Name       string   // user-chosen instance name, e.g. "obsidian-personal"
	Collection string   // optional logical grouping inside the source
	Include    []string // glob filters
	Exclude    []string
	MaxSizeMB  int64
	Custom     map[string]string // connector-specific config (e.g. local path)
}

type StreamOpts struct {
	Collection string
	// KnownDocs lets a re-run skip already-ingested + unchanged documents. Keys
	// are doc IDs (sha256 of source + source_ref), values are the stored
	// modification time. A connector emits a doc only when the ID is unknown
	// OR the source's modification time is strictly newer than the stored one.
	// This is what makes `grove connect` resumable: a killed run leaves
	// already-fetched docs in the store; the next run skips them. Empty / nil
	// disables the filter (every doc emitted).
	KnownDocs map[string]time.Time
}

// DrainChan drains an item/error channel pair (the shape every connector
// stream returns) into a slice, returning the first error seen.
func DrainChan[T any](items <-chan T, errs <-chan error) ([]T, error) {
	var out []T
	var firstErr error
	for items != nil || errs != nil {
		select {
		case v, ok := <-items:
			if !ok {
				items = nil
				continue
			}
			out = append(out, v)
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if e != nil && firstErr == nil {
				firstErr = e
			}
		}
	}
	return out, firstErr
}

// Collect drains a Document/error channel pair (as returned by
// Connector.Documents) into a slice, returning the first error seen.
func Collect(docs <-chan core.Document, errs <-chan error) ([]core.Document, error) {
	return DrainChan(docs, errs)
}

// Connector is the contract every source implements. Connectors emit
// normalized Documents and Change streams; they never touch storage,
// indexing, or model calls directly.
type Connector interface {
	Name() string
	Connect(ctx context.Context, cfg ConnectorConfig) error
	Disconnect(ctx context.Context) error
	Validate(ctx context.Context) error
	Documents(ctx context.Context, opts StreamOpts) (<-chan core.Document, <-chan error)
	Changes(ctx context.Context, since time.Time) (<-chan core.Change, <-chan error)
	// Enumerate streams the current document IDs without reading contents — the
	// cheap pass sync uses for deletion detection.
	Enumerate(ctx context.Context) (<-chan string, <-chan error)
	Capabilities() Capabilities
}
