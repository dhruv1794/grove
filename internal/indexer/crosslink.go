package indexer

import (
	"context"
	"path/filepath"
	"strings"

	"grove/internal/core"
)

// crossLink resolves explicit document links into SeeAlso edges between the
// leaf nodes that contain the linked documents, then persists them. It runs
// after every tree in the run is built, so both endpoints are live nodes.
//
// v1 covers explicit links only (strength 1.0). The weak-edge heuristic over
// title/summary overlap is deferred — see documents/TODO.md.
func (b *builder) crossLink(ctx context.Context) error {
	if len(b.allDocs) == 0 {
		return nil
	}
	idx := newLinkIndex(b.allDocs)
	edges := map[string][]core.NodeRef{}
	seen := map[[2]string]bool{}
	for i := range b.allDocs {
		d := &b.allDocs[i]
		from := leafNodeID(d)
		for _, l := range d.Links {
			for _, tgtID := range idx.resolve(l) {
				tgt := idx.byID[tgtID]
				if tgt == nil || tgt.ID == d.ID {
					continue // unresolved or self-link
				}
				key := [2]string{from, leafNodeID(tgt)}
				if seen[key] {
					continue // dedupe: a doc may link the same target twice
				}
				seen[key] = true
				edges[from] = append(edges[from], core.NodeRef{
					NodeID:   key[1],
					Reason:   l.Target,
					Strength: 1.0,
				})
				b.res.CrossLinks++
			}
		}
	}
	return b.deps.Store.AddNodeSeeAlso(ctx, edges)
}

// leafNodeID is the stable id of the leaf node that holds a document (tree id =
// source name; canonical path "doc:ID").
func leafNodeID(d *core.Document) string {
	return core.LeafNodeID(d.Source, d.ID)
}

// linkIndex resolves a raw link target to candidate document IDs. Targets are
// kept raw by connectors (e.g. wikilink "Session Mgmt", doc-ref "../rfc.md"),
// so resolution tries, in order: exact source_ref, document title, then the
// filename basename without extension. All keys are lowercased.
type linkIndex struct {
	byID    map[string]*core.Document
	byRef   map[string][]string
	byTitle map[string][]string
	byBase  map[string][]string
}

func newLinkIndex(docs []core.Document) *linkIndex {
	idx := &linkIndex{
		byID:    make(map[string]*core.Document, len(docs)),
		byRef:   map[string][]string{},
		byTitle: map[string][]string{},
		byBase:  map[string][]string{},
	}
	for i := range docs {
		d := &docs[i]
		idx.byID[d.ID] = d
		idx.byRef[strings.ToLower(d.SourceRef)] = append(idx.byRef[strings.ToLower(d.SourceRef)], d.ID)
		if d.Title != "" {
			t := strings.ToLower(d.Title)
			idx.byTitle[t] = append(idx.byTitle[t], d.ID)
		}
		base := baseNoExt(d.SourceRef)
		idx.byBase[base] = append(idx.byBase[base], d.ID)
	}
	return idx
}

func (idx *linkIndex) resolve(l core.DocLink) []string {
	if l.Type == core.LinkURL {
		return nil // external link, not a document reference
	}
	key := strings.ToLower(strings.TrimSpace(l.Target))
	if key == "" {
		return nil
	}
	if ids := idx.byRef[key]; len(ids) > 0 {
		return ids
	}
	if ids := idx.byTitle[key]; len(ids) > 0 {
		return ids
	}
	return idx.byBase[baseNoExt(key)]
}

func baseNoExt(path string) string {
	base := filepath.Base(filepath.ToSlash(path))
	return strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
}
