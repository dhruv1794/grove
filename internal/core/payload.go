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

// WriteNodePayload encodes a node payload to disk, creating parent directories.
// The write is atomic (temp file + rename): two nodes with identical content
// hash to the same path, so a concurrent build (grove build --concurrency)
// could otherwise interleave two writers and leave a torn JSON file.
func WriteNodePayload(path string, p *NodePayload) error {
	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal node payload %s: %w", p.NodeID, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".node-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp node payload in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close() // best-effort; the write error is the real failure
		os.Remove(tmpName)
		return fmt.Errorf("write node payload %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close node payload %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename node payload %s: %w", path, err)
	}
	return nil
}
