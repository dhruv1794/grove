package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"grove/internal/core"

	_ "modernc.org/sqlite"
)

// SQLite is the default Store. Metadata lives in SQLite at layout.DB;
// content-addressed payloads (docs, nodes) live as JSON under layout.Docs/Trees.
type SQLite struct {
	layout core.Layout
	db     *sql.DB
}

func New(layout core.Layout) *SQLite {
	return &SQLite{layout: layout}
}

func (s *SQLite) Open(ctx context.Context) error {
	if err := os.MkdirAll(s.layout.Root, 0o755); err != nil {
		return err
	}
	dsn := "file:" + s.layout.DB + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	s.db = db
	return nil
}

func (s *SQLite) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLite) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for i, stmt := range migrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) UpsertSource(ctx context.Context, src core.Source) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sources (name, type, config_json, connected_at, last_sync_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			type=excluded.type,
			config_json=excluded.config_json,
			last_sync_at=excluded.last_sync_at
	`, src.Name, src.Type, src.ConfigJSON, src.ConnectedAt.Unix(), nullableUnix(src.LastSyncAt))
	return err
}

func (s *SQLite) GetSource(ctx context.Context, name string) (*core.Source, error) {
	row := s.db.QueryRowContext(ctx, `SELECT name, type, config_json, connected_at, COALESCE(last_sync_at, 0) FROM sources WHERE name = ?`, name)
	src, err := scanSource(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return src, nil
}

func (s *SQLite) ListSources(ctx context.Context) ([]core.Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, type, config_json, connected_at, COALESCE(last_sync_at, 0) FROM sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *src)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSource(r rowScanner) (*core.Source, error) {
	var src core.Source
	var typ string
	var connected, synced int64
	if err := r.Scan(&src.Name, &typ, &src.ConfigJSON, &connected, &synced); err != nil {
		return nil, err
	}
	src.Type = core.SourceType(typ)
	src.ConnectedAt = time.Unix(connected, 0)
	src.LastSyncAt = toTime(synced)
	return &src, nil
}

// toTime converts a Unix-seconds value to a time.Time, treating 0 as zero.
func toTime(unixSec int64) time.Time {
	if unixSec == 0 {
		return time.Time{}
	}
	return time.Unix(unixSec, 0)
}

// DeleteSource removes a source and all its dependent rows (documents,
// doc_links, collections) in a single transaction and returns the number
// of documents removed. On-disk doc payloads are content-addressed and may
// be referenced by other sources, so they are NOT garbage-collected here.
// A separate `grove gc` pass is the right place for that (TODO).
func (s *SQLite) DeleteSource(ctx context.Context, name string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM doc_links WHERE from_doc IN (SELECT id FROM documents WHERE source = ?)`, name); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_documents WHERE doc_id IN (SELECT id FROM documents WHERE source = ?)`, name); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM doc_embeddings WHERE source = ?`, name); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_embeddings WHERE source = ?`, name); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_embeddings WHERE source = ?`, name); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE source = ?`, name)
	if err != nil {
		return 0, err
	}
	docsDeleted, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE source = ?`, name); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE name = ?`, name); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(docsDeleted), nil
}

func (s *SQLite) TouchSourceSync(ctx context.Context, name string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sources SET last_sync_at = ? WHERE name = ?`, t.Unix(), name)
	return err
}

func (s *SQLite) UpsertCollection(ctx context.Context, c core.Collection) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO collections (source, name, path) VALUES (?, ?, ?)
		ON CONFLICT(source, name) DO UPDATE SET path=excluded.path
	`, c.Source, c.Name, c.Path)
	return err
}

func (s *SQLite) ListCollections(ctx context.Context, source string) ([]core.Collection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, name, path FROM collections WHERE source = ? ORDER BY name`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Collection
	for rows.Next() {
		var c core.Collection
		if err := rows.Scan(&c.Source, &c.Name, &c.Path); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const upsertDocSQL = `
	INSERT INTO documents (id, source, collection, source_ref, title, hash, modified, metadata_json, indexed_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		collection=excluded.collection,
		title=excluded.title,
		hash=excluded.hash,
		modified=excluded.modified,
		metadata_json=excluded.metadata_json,
		indexed_at=excluded.indexed_at`

const (
	deleteDocLinksSQL = `DELETE FROM doc_links WHERE from_doc = ?`
	insertDocLinkSQL  = `INSERT INTO doc_links (from_doc, to_ref, link_type, anchor) VALUES (?, ?, ?, ?)`
)

// replaceDocLinks rewrites the doc_links rows for d: it deletes any prior
// rows then inserts one per d.Links. The DELETE runs unconditionally, so a
// doc that lost all its links ends up with no rows. The caller owns tx, so
// the document row and its links commit atomically.
func replaceDocLinks(ctx context.Context, tx *sql.Tx, d core.Document) error {
	if _, err := tx.ExecContext(ctx, deleteDocLinksSQL, d.ID); err != nil {
		return fmt.Errorf("document %s: clear doc_links: %w", d.ID, err)
	}
	for _, l := range d.Links {
		if _, err := tx.ExecContext(ctx, insertDocLinkSQL,
			d.ID, l.Target, string(l.Type), l.Anchor); err != nil {
			return fmt.Errorf("document %s: insert doc_link %q: %w", d.ID, l.Target, err)
		}
	}
	return nil
}

const (
	deleteDocFTSSQL = `DELETE FROM fts_documents WHERE doc_id = ?`
	insertDocFTSSQL = `INSERT INTO fts_documents (doc_id, source, title, content) VALUES (?, ?, ?, ?)`
)

// replaceDocFTS rewrites the fts_documents row for d: delete-then-insert, so a
// re-ingested doc is reindexed and never accumulates stale rows. The caller
// owns tx, so the document row and its FTS index entry commit atomically.
func replaceDocFTS(ctx context.Context, tx *sql.Tx, d core.Document) error {
	if _, err := tx.ExecContext(ctx, deleteDocFTSSQL, d.ID); err != nil {
		return fmt.Errorf("document %s: clear fts_documents: %w", d.ID, err)
	}
	if _, err := tx.ExecContext(ctx, insertDocFTSSQL, d.ID, d.Source, d.Title, d.Content); err != nil {
		return fmt.Errorf("document %s: index fts_documents: %w", d.ID, err)
	}
	return nil
}

func (s *SQLite) writeDocPayload(d core.Document) error {
	payload := core.PayloadPath(s.layout.Docs, d.Hash, "doc.json")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(d)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(payload, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	_, err = f.Write(body)
	return err
}

func (s *SQLite) UpsertDocument(ctx context.Context, d core.Document) error {
	metaJSON, err := json.Marshal(d.Metadata)
	if err != nil {
		return err
	}
	if err := s.writeDocPayload(d); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, upsertDocSQL,
		d.ID, d.Source, d.Collection, d.SourceRef, d.Title, d.Hash,
		d.Metadata.Modified.Unix(), string(metaJSON), time.Now().Unix()); err != nil {
		return err
	}
	if err := replaceDocLinks(ctx, tx, d); err != nil {
		return err
	}
	if err := replaceDocFTS(ctx, tx, d); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertDocuments batches many docs in a single SQLite transaction. Payload
// JSON is still written per-doc but tolerates duplicates via O_EXCL.
func (s *SQLite) UpsertDocuments(ctx context.Context, docs []core.Document) error {
	if len(docs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, upsertDocSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	for _, d := range docs {
		metaJSON, err := json.Marshal(d.Metadata)
		if err != nil {
			return err
		}
		if err := s.writeDocPayload(d); err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx,
			d.ID, d.Source, d.Collection, d.SourceRef, d.Title, d.Hash,
			d.Metadata.Modified.Unix(), string(metaJSON), now); err != nil {
			return err
		}
		if err := replaceDocLinks(ctx, tx, d); err != nil {
			return err
		}
		if err := replaceDocFTS(ctx, tx, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const docScanCols = `id, source, collection, source_ref, title, hash, modified, metadata_json`

// scanDocRow scans one documents-table row. It does not populate the
// content-addressed fields (Content/Hierarchy/Links) — those live in the
// payload; see readDocPayload.
func scanDocRow(r rowScanner) (core.Document, error) {
	var d core.Document
	var modified int64
	var metaJSON string
	if err := r.Scan(&d.ID, &d.Source, &d.Collection, &d.SourceRef, &d.Title, &d.Hash, &modified, &metaJSON); err != nil {
		return core.Document{}, err
	}
	if err := json.Unmarshal([]byte(metaJSON), &d.Metadata); err != nil {
		return core.Document{}, fmt.Errorf("document %s: metadata unmarshal: %w", d.ID, err)
	}
	d.Metadata.Modified = time.Unix(modified, 0)
	return d, nil
}

func (s *SQLite) GetDocument(ctx context.Context, id string) (*core.Document, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+docScanCols+` FROM documents WHERE id = ?`, id)
	d, err := scanDocRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDocumentMetaBySource returns every document for a source with its
// metadata plus Hierarchy and Links, but NOT Content — the indexer needs the
// structure (Hierarchy) and links to build and cross-link the tree, while the
// (large) body is fetched lazily per node via GetDocumentContent only on a
// cache miss. Keeping content out of this list bounds the build's working set
// to O(metadata) rather than O(corpus content). Payloads are read to source
// Hierarchy/Links but their content is not retained.
func (s *SQLite) ListDocumentMetaBySource(ctx context.Context, source string) ([]core.Document, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+docScanCols+` FROM documents WHERE source = ? ORDER BY source_ref`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Document
	for rows.Next() {
		d, err := scanDocRow(rows)
		if err != nil {
			return nil, err
		}
		p, err := s.readDocPayload(d.Hash)
		if err != nil {
			return nil, fmt.Errorf("document %s: %w", d.ID, err)
		}
		d.Hierarchy, d.Links = p.Hierarchy, p.Links
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocumentContent returns just the body of the content-addressed payload for
// a hash — the lazy companion to ListDocumentMetaBySource.
func (s *SQLite) GetDocumentContent(ctx context.Context, hash string) (string, error) {
	p, err := s.readDocPayload(hash)
	if err != nil {
		return "", err
	}
	return p.Content, nil
}

// ListDocumentDigests returns id→hash for a source without reading content
// payloads — the cheap lookup `grove sync` diffs the current walk against.
func (s *SQLite) ListDocumentDigests(ctx context.Context, source string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, hash FROM documents WHERE source = ?`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		out[id] = hash
	}
	return out, rows.Err()
}

// DeleteDocuments removes documents by id along with their doc_links and FTS
// rows, in one transaction. Mirrors DeleteSource's per-document cleanup. The
// content-addressed payload files are left on disk (shareable across docs/
// sources — reclaimed by a future `grove gc`). node_docs rows are cleared when
// the affected tree is rebuilt. Returns the number of documents deleted.
func (s *SQLite) DeleteDocuments(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	deleted := 0
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM doc_links WHERE from_doc = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM fts_documents WHERE doc_id = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM doc_embeddings WHERE doc_id = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_embeddings WHERE doc_id = ?`, id); err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			deleted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// GetDocuments returns full core.Documents (including Content/Hierarchy/Links)
// for the given ids, in request order. Unknown ids are skipped. Content
// payloads are read once per distinct hash.
func (s *SQLite) GetDocuments(ctx context.Context, ids []string) ([]core.Document, error) {
	out := make([]core.Document, 0, len(ids))
	payloads := map[string]*core.Document{}
	for _, id := range ids {
		row := s.db.QueryRowContext(ctx, `SELECT `+docScanCols+` FROM documents WHERE id = ?`, id)
		d, err := scanDocRow(row)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		p, ok := payloads[d.Hash]
		if !ok {
			if p, err = s.readDocPayload(d.Hash); err != nil {
				return nil, fmt.Errorf("document %s: %w", d.ID, err)
			}
			payloads[d.Hash] = p
		}
		d.Content, d.Hierarchy, d.Links = p.Content, p.Hierarchy, p.Links
		out = append(out, d)
	}
	return out, nil
}

// FindDocuments resolves a document reference that is either a document ID or
// a source path (source_ref). It returns every match — a bare source_ref can
// collide across sources — ordered for stable output. Content-addressed
// fields are not populated; this is a metadata lookup.
func (s *SQLite) FindDocuments(ctx context.Context, ref string) ([]core.Document, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+docScanCols+` FROM documents WHERE id = ? OR source_ref = ? ORDER BY source, source_ref`,
		ref, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Document
	for rows.Next() {
		d, err := scanDocRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *SQLite) readDocPayload(hash string) (*core.Document, error) {
	body, err := os.ReadFile(core.PayloadPath(s.layout.Docs, hash, "doc.json"))
	if err != nil {
		return nil, fmt.Errorf("read doc payload: %w", err)
	}
	var d core.Document
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("doc payload unmarshal: %w", err)
	}
	return &d, nil
}

// ftsMatchQuery turns a free-text question into an FTS5 MATCH expression:
// every alphanumeric run becomes a quoted term, OR-joined so any term can
// match and bm25 ranks by overall relevance. Quoting each term neutralizes
// FTS5 syntax characters (`?`, `"`, `*`, `-`, …) in the raw question.
// Returns "" when the query yields no usable terms.
func ftsMatchQuery(q string) string {
	terms := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " OR ")
}

// SearchDocuments runs a full-text keyword search over document titles and
// content, returning matching document IDs best-first. Title matches are
// weighted above content matches. source restricts the scope when non-empty;
// limit caps the result count (<= 0 → 12). It backs `grove ask --fast` and
// makes no LLM call.
func (s *SQLite) SearchDocuments(ctx context.Context, query, source string, limit int) ([]string, error) {
	hits, err := s.SearchDocumentsScored(ctx, query, source, limit)
	if err != nil || hits == nil {
		return nil, err
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out, nil
}

func (s *SQLite) SearchDocumentsScored(ctx context.Context, query, source string, limit int) ([]Hit, error) {
	match := ftsMatchQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 12
	}
	// bm25 weights, in column order: doc_id, source (both UNINDEXED, ignored),
	// title 10×, content 1×. Lower bm25 = better, so ascending order is best-first.
	q := `SELECT doc_id, bm25(fts_documents, 1.0, 1.0, 10.0, 1.0) AS score
	      FROM fts_documents WHERE fts_documents MATCH ?`
	args := []any{match}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, source)
	}
	q += ` ORDER BY score LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search %q: %w", query, err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQLite) CountDocuments(ctx context.Context, source string) (int, error) {
	var n int
	q := `SELECT COUNT(*) FROM documents`
	args := []any{}
	if source != "" {
		q += ` WHERE source = ?`
		args = append(args, source)
	}
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountDocumentsBySource returns doc counts keyed by source name in one query.
func (s *SQLite) CountDocumentsBySource(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, COUNT(*) FROM documents GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return nil, err
		}
		out[src] = n
	}
	return out, rows.Err()
}

func (s *SQLite) UpsertTree(ctx context.Context, t core.Tree) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO trees (id, source, collection, name, root_node_id, doc_count, node_count, created, modified, build_model, prompt_ver)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, root_node_id=excluded.root_node_id,
			doc_count=excluded.doc_count, node_count=excluded.node_count,
			modified=excluded.modified, build_model=excluded.build_model,
			prompt_ver=excluded.prompt_ver
	`, t.ID, t.Source, t.Collection, t.Name, t.RootNodeID, t.DocCount, t.NodeCount, t.Created.Unix(), t.Modified.Unix(), t.BuildModel, t.PromptVer)
	return err
}

func (s *SQLite) ListTrees(ctx context.Context) ([]core.Tree, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source, collection, name, root_node_id, doc_count, node_count, created, modified, build_model, prompt_ver FROM trees ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Tree
	for rows.Next() {
		var t core.Tree
		var created, modified int64
		if err := rows.Scan(&t.ID, &t.Source, &t.Collection, &t.Name, &t.RootNodeID, &t.DocCount, &t.NodeCount, &created, &modified, &t.BuildModel, &t.PromptVer); err != nil {
			return nil, err
		}
		t.Created = toTime(created)
		t.Modified = toTime(modified)
		out = append(out, t)
	}
	return out, rows.Err()
}

const (
	nodeScanCols = `id, tree_id, parent_id, title, depth, content_hash, payload_path, prompt_ver`

	// Subqueries selecting the node IDs in a scope; each takes one bound arg.
	nodeIDsByTree = `SELECT id FROM nodes WHERE tree_id = ?`
	nodeIDsByHash = `SELECT id FROM nodes WHERE content_hash = ?`
	nodeIDsByID   = `SELECT id FROM nodes WHERE id = ?`

	upsertNodeSQL = `
		INSERT INTO nodes (id, tree_id, parent_id, title, depth, content_hash, payload_path, prompt_ver)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			tree_id=excluded.tree_id, parent_id=excluded.parent_id, title=excluded.title,
			depth=excluded.depth, content_hash=excluded.content_hash,
			payload_path=excluded.payload_path, prompt_ver=excluded.prompt_ver`

	deleteNodeDocsSQL    = `DELETE FROM node_docs WHERE node_id = ?`
	insertNodeDocSQL     = `INSERT OR IGNORE INTO node_docs (node_id, doc_id) VALUES (?, ?)`
	deleteNodeSeeAlsoSQL = `DELETE FROM node_seealso WHERE from_node = ?`
	insertNodeSeeAlsoSQL = `
		INSERT INTO node_seealso (from_node, to_node, reason, strength) VALUES (?, ?, ?, ?)
		ON CONFLICT(from_node, to_node) DO UPDATE SET reason=excluded.reason, strength=excluded.strength`
)

func scanNode(r rowScanner) (*core.Node, error) {
	var n core.Node
	if err := r.Scan(&n.ID, &n.TreeID, &n.ParentID, &n.Title, &n.Depth, &n.ContentHash, &n.PayloadPath, &n.PromptVer); err != nil {
		return nil, err
	}
	return &n, nil
}

// replaceNodeEdges rewrites a node's node_docs and node_seealso rows: prior
// rows are deleted unconditionally, then the current slices reinserted, so a
// node that lost edges ends with none. The caller owns tx.
func replaceNodeEdges(ctx context.Context, tx *sql.Tx, n core.Node) error {
	if _, err := tx.ExecContext(ctx, deleteNodeDocsSQL, n.ID); err != nil {
		return fmt.Errorf("node %s: clear node_docs: %w", n.ID, err)
	}
	for _, docID := range n.DocIDs {
		if _, err := tx.ExecContext(ctx, insertNodeDocSQL, n.ID, docID); err != nil {
			return fmt.Errorf("node %s: insert node_doc %q: %w", n.ID, docID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, deleteNodeSeeAlsoSQL, n.ID); err != nil {
		return fmt.Errorf("node %s: clear node_seealso: %w", n.ID, err)
	}
	for _, ref := range n.SeeAlso {
		if _, err := tx.ExecContext(ctx, insertNodeSeeAlsoSQL, n.ID, ref.NodeID, ref.Reason, ref.Strength); err != nil {
			return fmt.Errorf("node %s: insert node_seealso %q: %w", n.ID, ref.NodeID, err)
		}
	}
	return nil
}

// UpsertNodes batches node rows and their node_docs/node_seealso edges in one
// transaction. Node payload JSON (summary, doc text) is written separately by
// the indexer; this persists only the queryable SQLite metadata and edges.
func (s *SQLite) UpsertNodes(ctx context.Context, nodes []core.Node) error {
	if len(nodes) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, upsertNodeSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, n := range nodes {
		if _, err := stmt.ExecContext(ctx,
			n.ID, n.TreeID, n.ParentID, n.Title, n.Depth, n.ContentHash, n.PayloadPath, n.PromptVer); err != nil {
			return fmt.Errorf("upsert node %s: %w", n.ID, err)
		}
		if err := replaceNodeEdges(ctx, tx, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) UpsertNode(ctx context.Context, n core.Node) error {
	return s.UpsertNodes(ctx, []core.Node{n})
}

// AddNodeSeeAlso inserts node_seealso edges grouped by source node. Unlike
// replaceNodeEdges it does not clear existing rows or touch node_docs: the
// cross-link pass runs after nodes are persisted, so it adds edges rather than
// rewriting them. Re-inserting an edge updates its reason/strength in place.
func (s *SQLite) AddNodeSeeAlso(ctx context.Context, edges map[string][]core.NodeRef) error {
	if len(edges) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, insertNodeSeeAlsoSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for from, refs := range edges {
		for _, ref := range refs {
			if _, err := stmt.ExecContext(ctx, from, ref.NodeID, ref.Reason, ref.Strength); err != nil {
				return fmt.Errorf("node %s: insert node_seealso %q: %w", from, ref.NodeID, err)
			}
		}
	}
	return tx.Commit()
}

// attachEdges populates DocIDs and SeeAlso on each node. idSubquery selects
// the node IDs in scope and takes one bound arg, so one pair of queries
// covers a whole tree, a content-hash group, or a single node — no N+1.
func (s *SQLite) attachEdges(ctx context.Context, nodes []*core.Node, idSubquery string, arg any) error {
	if len(nodes) == 0 {
		return nil
	}
	docRows, err := s.db.QueryContext(ctx,
		`SELECT node_id, doc_id FROM node_docs WHERE node_id IN (`+idSubquery+`) ORDER BY node_id, doc_id`, arg)
	if err != nil {
		return fmt.Errorf("query node_docs: %w", err)
	}
	docIDs := map[string][]string{}
	for docRows.Next() {
		var nodeID, docID string
		if err := docRows.Scan(&nodeID, &docID); err != nil {
			docRows.Close()
			return fmt.Errorf("scan node_docs: %w", err)
		}
		docIDs[nodeID] = append(docIDs[nodeID], docID)
	}
	if err := docRows.Err(); err != nil {
		docRows.Close()
		return err
	}
	docRows.Close()

	saRows, err := s.db.QueryContext(ctx,
		`SELECT from_node, to_node, COALESCE(reason, ''), COALESCE(strength, 0)
		 FROM node_seealso WHERE from_node IN (`+idSubquery+`) ORDER BY from_node, to_node`, arg)
	if err != nil {
		return fmt.Errorf("query node_seealso: %w", err)
	}
	defer saRows.Close()
	seeAlso := map[string][]core.NodeRef{}
	for saRows.Next() {
		var from string
		var ref core.NodeRef
		var strength float64
		if err := saRows.Scan(&from, &ref.NodeID, &ref.Reason, &strength); err != nil {
			return fmt.Errorf("scan node_seealso: %w", err)
		}
		ref.Strength = float32(strength)
		seeAlso[from] = append(seeAlso[from], ref)
	}
	if err := saRows.Err(); err != nil {
		return err
	}

	for _, n := range nodes {
		n.DocIDs = docIDs[n.ID]
		n.SeeAlso = seeAlso[n.ID]
	}
	return nil
}

// nodesByQuery runs a node SELECT (one bound arg) and attaches edges via
// idSubquery, which must select the same node IDs from the same arg.
func (s *SQLite) nodesByQuery(ctx context.Context, query string, arg any, idSubquery string) ([]core.Node, error) {
	rows, err := s.db.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ptrs []*core.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		ptrs = append(ptrs, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachEdges(ctx, ptrs, idSubquery, arg); err != nil {
		return nil, err
	}
	out := make([]core.Node, len(ptrs))
	for i, n := range ptrs {
		out[i] = *n
	}
	return out, nil
}

func (s *SQLite) GetNode(ctx context.Context, id string) (*core.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeScanCols+` FROM nodes WHERE id = ?`, id)
	n, err := scanNode(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.attachEdges(ctx, []*core.Node{n}, nodeIDsByID, id); err != nil {
		return nil, err
	}
	return n, nil
}

func (s *SQLite) ListNodesByTree(ctx context.Context, treeID string) ([]core.Node, error) {
	return s.nodesByQuery(ctx,
		`SELECT `+nodeScanCols+` FROM nodes WHERE tree_id = ? ORDER BY depth, id`,
		treeID, nodeIDsByTree)
}

// GetNodesByContentHash is the node-cache lookup: it returns every node whose
// payload content hash matches, across trees and prompt versions.
func (s *SQLite) GetNodesByContentHash(ctx context.Context, contentHash string) ([]core.Node, error) {
	return s.nodesByQuery(ctx,
		`SELECT `+nodeScanCols+` FROM nodes WHERE content_hash = ? ORDER BY id`,
		contentHash, nodeIDsByHash)
}

// DeleteNodesByTree removes a tree's nodes and their edges (for --rebuild).
// node_seealso rows are dropped if either endpoint is in the tree, so a
// cross-tree edge into the rebuilt tree doesn't dangle. The trees row itself
// is left alone — the caller re-upserts it.
func (s *SQLite) DeleteNodesByTree(ctx context.Context, treeID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM node_seealso WHERE from_node IN (`+nodeIDsByTree+`) OR to_node IN (`+nodeIDsByTree+`)`,
		treeID, treeID); err != nil {
		return 0, fmt.Errorf("delete node_seealso for tree %s: %w", treeID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM node_docs WHERE node_id IN (`+nodeIDsByTree+`)`, treeID); err != nil {
		return 0, fmt.Errorf("delete node_docs for tree %s: %w", treeID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_embeddings WHERE tree_id = ?`, treeID); err != nil {
		return 0, fmt.Errorf("delete node_embeddings for tree %s: %w", treeID, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE tree_id = ?`, treeID)
	if err != nil {
		return 0, fmt.Errorf("delete nodes for tree %s: %w", treeID, err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete nodes for tree %s: rows affected: %w", treeID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(deleted), nil
}

func (s *SQLite) GetDocLinks(ctx context.Context, docID string) ([]core.DocLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT to_ref, COALESCE(link_type, ''), COALESCE(anchor, '')
		 FROM doc_links WHERE from_doc = ? ORDER BY to_ref, anchor`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.DocLink
	for rows.Next() {
		var l core.DocLink
		var typ string
		if err := rows.Scan(&l.Target, &typ, &l.Anchor); err != nil {
			return nil, err
		}
		l.Type = core.LinkType(typ)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListDocLinksBySource returns every doc's links for a source, keyed by the
// source document ID — the batch read the cross-link pass consumes. Rows are
// not deduped (a doc that links the same target twice yields two entries).
func (s *SQLite) ListDocLinksBySource(ctx context.Context, source string) (map[string][]core.DocLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_doc, to_ref, COALESCE(link_type, ''), COALESCE(anchor, '')
		 FROM doc_links WHERE from_doc IN (SELECT id FROM documents WHERE source = ?)
		 ORDER BY from_doc, to_ref, anchor`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]core.DocLink{}
	for rows.Next() {
		var from string
		var l core.DocLink
		var typ string
		if err := rows.Scan(&from, &l.Target, &typ, &l.Anchor); err != nil {
			return nil, err
		}
		l.Type = core.LinkType(typ)
		out[from] = append(out[from], l)
	}
	return out, rows.Err()
}

func (s *SQLite) RecordBuild(ctx context.Context, r core.BuildRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO build_history
		   (started_at, finished_at, model, docs_processed, nodes_created, cost_usd, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nullableUnix(r.StartedAt), nullableUnix(r.FinishedAt), r.Model,
		r.DocsProcessed, r.NodesCreated, r.CostUSD, r.Status)
	return err
}

// ListBuildHistory returns the most recent build runs, newest first. A limit
// <= 0 returns all rows.
func (s *SQLite) ListBuildHistory(ctx context.Context, limit int) ([]core.BuildRecord, error) {
	query := `SELECT started_at, finished_at, model, docs_processed, nodes_created, cost_usd, status
	          FROM build_history ORDER BY id DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.BuildRecord
	for rows.Next() {
		var r core.BuildRecord
		var started, finished int64
		if err := rows.Scan(&started, &finished, &r.Model,
			&r.DocsProcessed, &r.NodesCreated, &r.CostUSD, &r.Status); err != nil {
			return nil, err
		}
		r.StartedAt = toTime(started)
		r.FinishedAt = toTime(finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullableUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}
