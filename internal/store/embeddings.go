package store

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
)

// encodeVec packs a float32 vector into a little-endian byte blob.
func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec unpacks a blob written by encodeVec.
func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// UpsertEmbedding stores (or replaces) a document's embedding vector.
func (s *SQLite) UpsertEmbedding(ctx context.Context, docID, source, model string, vec []float32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO doc_embeddings (doc_id, source, model, dim, vec) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(doc_id) DO UPDATE SET source=excluded.source, model=excluded.model, dim=excluded.dim, vec=excluded.vec
	`, docID, source, model, len(vec), encodeVec(vec))
	return err
}

// CountEmbeddings returns how many documents in source have an embedding —
// lets `grove embed` skip already-embedded corpora.
func (s *SQLite) CountEmbeddings(ctx context.Context, source string) (int, error) {
	var n int
	q := `SELECT COUNT(*) FROM doc_embeddings`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// NodeVecHit is a node-summary vector match: the node and its tree (the latter
// lets the caller expand the node to its subtree's leaf documents).
type NodeVecHit struct {
	NodeID string
	TreeID string
}

// UpsertNodeEmbedding stores (or replaces) an internal tree-node's summary
// embedding, for collapsed-tree retrieval.
func (s *SQLite) UpsertNodeEmbedding(ctx context.Context, nodeID, treeID, source, model string, vec []float32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO node_embeddings (node_id, tree_id, source, model, dim, vec) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET tree_id=excluded.tree_id, source=excluded.source, model=excluded.model, dim=excluded.dim, vec=excluded.vec
	`, nodeID, treeID, source, model, len(vec), encodeVec(vec))
	return err
}

// CountNodeEmbeddings returns how many tree nodes in source have a summary
// embedding — lets the query side wire the collapsed-tree retriever only when
// node vectors exist.
func (s *SQLite) CountNodeEmbeddings(ctx context.Context, source string) (int, error) {
	var n int
	q := `SELECT COUNT(*) FROM node_embeddings`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// SearchNodesByVector returns the tree nodes whose stored summary embedding is
// most cosine-similar to query, best-first, capped at k. Brute-force, like
// SearchByVector; node counts are far smaller than doc counts.
func (s *SQLite) SearchNodesByVector(ctx context.Context, query []float32, source string, k int) ([]NodeVecHit, error) {
	if len(query) == 0 {
		return nil, nil
	}
	if k <= 0 {
		k = 10
	}
	q := `SELECT node_id, tree_id, vec FROM node_embeddings`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	qn := norm(query)
	type scored struct {
		hit   NodeVecHit
		score float64
	}
	var hits []scored
	for rows.Next() {
		var id, tid string
		var blob []byte
		if err := rows.Scan(&id, &tid, &blob); err != nil {
			return nil, err
		}
		v := decodeVec(blob)
		if len(v) != len(query) {
			continue // different embedding model/dimension; skip
		}
		hits = append(hits, scored{NodeVecHit{id, tid}, cosine(query, v, qn)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > k {
		hits = hits[:k]
	}
	out := make([]NodeVecHit, len(hits))
	for i, h := range hits {
		out[i] = h.hit
	}
	return out, nil
}

// UpsertChunkEmbedding stores (or replaces) one passage vector for a document.
func (s *SQLite) UpsertChunkEmbedding(ctx context.Context, docID string, chunkIdx int, source, model string, vec []float32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chunk_embeddings (doc_id, chunk_idx, source, model, dim, vec) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(doc_id, chunk_idx) DO UPDATE SET source=excluded.source, model=excluded.model, dim=excluded.dim, vec=excluded.vec
	`, docID, chunkIdx, source, model, len(vec), encodeVec(vec))
	return err
}

// DeleteChunkEmbeddingsByDoc clears a document's chunk vectors, so a re-embed
// (which may yield a different chunk count) doesn't leave stale passages.
func (s *SQLite) DeleteChunkEmbeddingsByDoc(ctx context.Context, docID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunk_embeddings WHERE doc_id = ?`, docID)
	return err
}

// CountChunkEmbeddings returns how many passage vectors exist in source.
func (s *SQLite) CountChunkEmbeddings(ctx context.Context, source string) (int, error) {
	var n int
	q := `SELECT COUNT(*) FROM chunk_embeddings`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// SearchByChunkVector brute-forces cosine over passage vectors and returns the
// best-matching parent documents, best-first, deduped (a doc's score is its best
// chunk), capped at k.
func (s *SQLite) SearchByChunkVector(ctx context.Context, query []float32, source string, k int) ([]string, error) {
	if len(query) == 0 {
		return nil, nil
	}
	if k <= 0 {
		k = 12
	}
	q := `SELECT doc_id, vec FROM chunk_embeddings`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	qn := norm(query)
	best := map[string]float64{}
	var order []string
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		v := decodeVec(blob)
		if len(v) != len(query) {
			continue
		}
		sc := cosine(query, v, qn)
		if cur, ok := best[id]; !ok || sc > cur {
			if !ok {
				order = append(order, id)
			}
			best[id] = sc
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(order, func(i, j int) bool { return best[order[i]] > best[order[j]] })
	if len(order) > k {
		order = order[:k]
	}
	return order, nil
}

// EmbeddingModels returns the distinct embedding models present in the store
// for source (all sources when empty). Lets callers warn when the corpus was
// embedded with a different model than the one querying (a silent dim mismatch).
func (s *SQLite) EmbeddingModels(ctx context.Context, source string) ([]string, error) {
	q := `SELECT DISTINCT model FROM doc_embeddings`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SearchByVector returns the doc IDs whose stored embedding is most cosine-
// similar to query, best-first, capped at k. source restricts scope when set.
// Brute-force over the source's vectors — adequate at local-first scale.
func (s *SQLite) SearchByVector(ctx context.Context, query []float32, source string, k int) ([]string, error) {
	hits, err := s.SearchByVectorScored(ctx, query, source, k)
	if err != nil || hits == nil {
		return nil, err
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out, nil
}

func (s *SQLite) SearchByVectorScored(ctx context.Context, query []float32, source string, k int) ([]Hit, error) {
	if len(query) == 0 {
		return nil, nil
	}
	if k <= 0 {
		k = 12
	}
	q := `SELECT doc_id, vec FROM doc_embeddings`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	qn := norm(query)
	var hits []Hit
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		v := decodeVec(blob)
		if len(v) != len(query) {
			continue // different embedding model/dimension; skip
		}
		hits = append(hits, Hit{ID: id, Score: cosine(query, v, qn)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

func norm(v []float32) float64 {
	var s float64
	for _, f := range v {
		s += float64(f) * float64(f)
	}
	return math.Sqrt(s)
}

// cosine returns the cosine similarity of a and b; aNorm is a's precomputed
// L2 norm (a is the query, reused across every candidate).
func cosine(a, b []float32, aNorm float64) float64 {
	var dot, bn float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		bn += float64(b[i]) * float64(b[i])
	}
	denom := aNorm * math.Sqrt(bn)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
