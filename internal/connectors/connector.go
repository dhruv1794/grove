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
	Name       string            // user-chosen instance name, e.g. "obsidian-personal"
	Collection string            // optional logical grouping inside the source
	Include    []string          // glob filters
	Exclude    []string
	MaxSizeMB  int64
	Custom     map[string]string // connector-specific config (e.g. local path)
}

type StreamOpts struct {
	Collection string
}

// Collect drains a Document/error channel pair (as returned by
// Connector.Documents) into a slice, returning the first error seen.
func Collect(docs <-chan core.Document, errs <-chan error) ([]core.Document, error) {
	var out []core.Document
	var firstErr error
	for docs != nil || errs != nil {
		select {
		case d, ok := <-docs:
			if !ok {
				docs = nil
				continue
			}
			out = append(out, d)
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
	Capabilities() Capabilities
}
