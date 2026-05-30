package store

// Migrations are applied in order. Each migration runs once; applied
// versions are tracked in the `schema_migrations` table.
var migrations = []string{
	// 0001 — initial schema (per 03-data-model.md)
	`
	CREATE TABLE IF NOT EXISTS sources (
	  name           TEXT PRIMARY KEY,
	  type           TEXT NOT NULL,
	  config_json    TEXT,
	  connected_at   INTEGER,
	  last_sync_at   INTEGER
	);

	CREATE TABLE IF NOT EXISTS collections (
	  source         TEXT NOT NULL REFERENCES sources(name),
	  name           TEXT NOT NULL,
	  path           TEXT,
	  PRIMARY KEY (source, name)
	);

	CREATE TABLE IF NOT EXISTS documents (
	  id             TEXT PRIMARY KEY,
	  source         TEXT NOT NULL REFERENCES sources(name),
	  collection     TEXT,
	  source_ref     TEXT NOT NULL,
	  title          TEXT,
	  hash           TEXT NOT NULL,
	  modified       INTEGER,
	  metadata_json  TEXT,
	  indexed_at     INTEGER,
	  UNIQUE(source, source_ref)
	);
	CREATE INDEX IF NOT EXISTS idx_docs_source ON documents(source);
	CREATE INDEX IF NOT EXISTS idx_docs_collection ON documents(source, collection);
	CREATE INDEX IF NOT EXISTS idx_docs_hash ON documents(hash);
	CREATE INDEX IF NOT EXISTS idx_docs_modified ON documents(modified);

	CREATE TABLE IF NOT EXISTS doc_links (
	  from_doc   TEXT NOT NULL,
	  to_ref     TEXT NOT NULL,
	  link_type  TEXT,
	  anchor     TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_doclinks_from ON doc_links(from_doc);

	CREATE TABLE IF NOT EXISTS trees (
	  id             TEXT PRIMARY KEY,
	  source         TEXT,
	  collection     TEXT,
	  name           TEXT,
	  root_node_id   TEXT,
	  doc_count      INTEGER,
	  node_count     INTEGER,
	  created        INTEGER,
	  modified       INTEGER,
	  build_model    TEXT,
	  prompt_ver     TEXT
	);

	CREATE TABLE IF NOT EXISTS nodes (
	  id             TEXT PRIMARY KEY,
	  tree_id        TEXT NOT NULL REFERENCES trees(id),
	  parent_id      TEXT,
	  title          TEXT,
	  depth          INTEGER,
	  content_hash   TEXT,
	  payload_path   TEXT,
	  prompt_ver     TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_nodes_tree ON nodes(tree_id);
	CREATE INDEX IF NOT EXISTS idx_nodes_parent ON nodes(parent_id);
	CREATE INDEX IF NOT EXISTS idx_nodes_hash ON nodes(content_hash);

	CREATE TABLE IF NOT EXISTS node_docs (
	  node_id  TEXT NOT NULL,
	  doc_id   TEXT NOT NULL,
	  PRIMARY KEY (node_id, doc_id)
	);

	CREATE TABLE IF NOT EXISTS node_seealso (
	  from_node  TEXT NOT NULL,
	  to_node    TEXT NOT NULL,
	  reason     TEXT,
	  strength   REAL,
	  PRIMARY KEY (from_node, to_node)
	);

	CREATE TABLE IF NOT EXISTS build_history (
	  id              INTEGER PRIMARY KEY AUTOINCREMENT,
	  started_at      INTEGER,
	  finished_at     INTEGER,
	  model           TEXT,
	  docs_processed  INTEGER,
	  nodes_created   INTEGER,
	  cost_usd        REAL,
	  status          TEXT
	);
	`,
	// 0002 — full-text index backing `grove ask --fast`. Standalone FTS5
	// table (not external-content): document content lives in the on-disk
	// payload, not a SQLite column, so rows are written explicitly by
	// UpsertDocument(s). doc_id/source are UNINDEXED — stored for lookup and
	// filtering, not tokenized. Pre-0002 documents are not indexed until
	// re-ingested (see TODO: FTS backfill).
	`
	CREATE VIRTUAL TABLE IF NOT EXISTS fts_documents USING fts5(
	  doc_id UNINDEXED,
	  source UNINDEXED,
	  title,
	  content,
	  tokenize = 'porter unicode61'
	);
	`,
	// 0003 — dense embeddings for semantic retrieval. Vectors are stored as a
	// little-endian float32 BLOB; grove brute-forces cosine similarity in Go
	// (no vector-DB dependency — fine at local-first corpus sizes). dim lets a
	// reader decode the blob and skip vectors from a different embedding model.
	`
	CREATE TABLE IF NOT EXISTS doc_embeddings (
	  doc_id  TEXT PRIMARY KEY,
	  source  TEXT NOT NULL,
	  model   TEXT NOT NULL,
	  dim     INTEGER NOT NULL,
	  vec     BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_doc_embeddings_source ON doc_embeddings(source);
	`,
	// 0004 — embeddings for internal tree-node summaries ("collapsed tree"
	// retrieval). A query that matches a node's abstractive summary surfaces that
	// subtree's leaf docs, so the tree contributes as a retrieval signal at every
	// level (fused via RRF) instead of only via greedy descent. Same little-endian
	// float32 BLOB / dim convention as doc_embeddings; tree_id lets the retriever
	// expand a matched node to its leaves.
	`
	CREATE TABLE IF NOT EXISTS node_embeddings (
	  node_id TEXT PRIMARY KEY,
	  tree_id TEXT NOT NULL,
	  source  TEXT NOT NULL,
	  model   TEXT NOT NULL,
	  dim     INTEGER NOT NULL,
	  vec     BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_node_embeddings_source ON node_embeddings(source);
	CREATE INDEX IF NOT EXISTS idx_node_embeddings_tree ON node_embeddings(tree_id);
	`,
	// 0005 — passage-level embeddings (semantic chunking experiment). One doc maps
	// to N chunk vectors so the semantic retriever can match a passage inside a
	// long document rather than the whole-doc average; search dedups to the parent
	// doc. Same blob/dim convention as doc_embeddings. Opt-in (grove embed --chunks
	// / ask --chunk-embed) — a gate on whether passage-level retrieval helps.
	`
	CREATE TABLE IF NOT EXISTS chunk_embeddings (
	  doc_id    TEXT NOT NULL,
	  chunk_idx INTEGER NOT NULL,
	  source    TEXT NOT NULL,
	  model     TEXT NOT NULL,
	  dim       INTEGER NOT NULL,
	  vec       BLOB NOT NULL,
	  PRIMARY KEY (doc_id, chunk_idx)
	);
	CREATE INDEX IF NOT EXISTS idx_chunk_embeddings_source ON chunk_embeddings(source);
	`,
}
