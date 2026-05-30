// Package sync detects what changed in a connected source since the last
// ingest and applies the difference to the store. It is core-library code: it
// operates on a Store and a connectors.Connector handed to it, never knowing
// which concrete connector (local, obsidian, …) produced the documents. The
// caller (the grove API) reconstructs the connector from the stored source and
// triggers the incremental forest rebuild afterward.
package sync

import (
	"context"
	"time"

	"grove/internal/connectors"
	"grove/internal/core"
	"grove/internal/store"
)

// Diff is the set of changes between a source's current walk and what the store
// holds: new and content-changed documents to upsert, and the ids of documents
// no longer present at the source.
type Diff struct {
	Created  []core.Document
	Modified []core.Document
	Deleted  []string
}

// Empty reports whether the diff has no changes.
func (d Diff) Empty() bool {
	return len(d.Created)+len(d.Modified)+len(d.Deleted) == 0
}

// Compute diffs a source's current state against the stored documents. To stay
// cheap at scale it only reads+hashes files whose mtime is newer than since
// (via Changes) for create/modify detection, then verifies by content hash;
// deletions come from a content-free Enumerate of current ids. Passing a zero
// since forces a full re-hash (every file looks newer than the epoch).
func Compute(ctx context.Context, s store.Store, conn connectors.Connector, source string, since time.Time) (*Diff, error) {
	stored, err := s.ListDocumentDigests(ctx, source)
	if err != nil {
		return nil, err
	}

	d := &Diff{}
	// Create/modify: only mtime-touched files are read+hashed. An mtime bump
	// with an unchanged content hash is a no-op (the "verify with hash" step).
	changes, err := connectors.DrainChan(conn.Changes(ctx, since))
	if err != nil {
		return nil, err
	}
	for _, ch := range changes {
		if ch.Document == nil {
			continue
		}
		doc := *ch.Document
		prevHash, ok := stored[doc.ID]
		switch {
		case !ok:
			d.Created = append(d.Created, doc)
		case prevHash != doc.Hash:
			d.Modified = append(d.Modified, doc)
		}
	}

	// Delete: a stored id absent from the current (content-free) enumeration
	// was removed at the source.
	ids, err := connectors.DrainChan(conn.Enumerate(ctx))
	if err != nil {
		return nil, err
	}
	current := make(map[string]bool, len(ids))
	for _, id := range ids {
		current[id] = true
	}
	for id := range stored {
		if !current[id] {
			d.Deleted = append(d.Deleted, id)
		}
	}
	return d, nil
}

// Apply persists a diff: created and modified documents are upserted (which
// refreshes their doc_links and FTS rows), and deleted documents are removed.
// The forest rebuild is the caller's responsibility — the build's content-hash
// node cache makes it incremental.
func Apply(ctx context.Context, s store.Store, d *Diff) error {
	if changed := append(append([]core.Document{}, d.Created...), d.Modified...); len(changed) > 0 {
		if err := s.UpsertDocuments(ctx, changed); err != nil {
			return err
		}
	}
	if len(d.Deleted) > 0 {
		if _, err := s.DeleteDocuments(ctx, d.Deleted); err != nil {
			return err
		}
	}
	return nil
}
