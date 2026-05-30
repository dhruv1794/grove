package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// NodeSchemaVersion is the on-disk schema version of a NodePayload. Bump it
// on payload-shape changes so a stale payload can be detected.
const NodeSchemaVersion = 1

// NodePayload is the content-addressed JSON written per tree node under
// Layout.Trees. The indexer writes it; query and the MCP server read it.
// Its on-disk location is addressed by (content_hash, prompt_ver,
// build_model) — see PayloadPath.
type NodePayload struct {
	SchemaVersion int       `json:"schema_version"`
	NodeID        string    `json:"node_id"`
	Title         string    `json:"title"`
	Summary       string    `json:"summary"`
	DocIDs        []string  `json:"doc_ids,omitempty"`
	ContentHash   string    `json:"content_hash"`
	PromptVer     string    `json:"prompt_ver"`
	BuildModel    string    `json:"build_model"`
	BuiltAt       time.Time `json:"built_at"`
}

// ReadNodePayload reads and decodes a node payload from disk.
func ReadNodePayload(path string) (*NodePayload, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p NodePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("node payload %s: %w", path, err)
	}
	return &p, nil
}

// WriteNodePayload encodes a node payload to disk via WriteJSONAtomic.
func WriteNodePayload(path string, p *NodePayload) error {
	return WriteJSONAtomic(path, p)
}

// WriteJSONAtomic marshals v (indented) and writes it to path atomically
// (temp file + rename), creating parent directories. The atomicity matters
// because content-addressed payloads share a path: two concurrent builders
// writing identical content must not interleave into a torn JSON file.
func WriteJSONAtomic(path string, v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".payload-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp payload in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close() // best-effort; the write error is the real failure
		os.Remove(tmpName)
		return fmt.Errorf("write payload %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close payload %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename payload %s: %w", path, err)
	}
	return nil
}
