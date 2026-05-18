package core

import (
	"os"
	"path/filepath"
)

// DefaultWorkspace returns ~/.grove/default unless GROVE_WORKSPACE is set.
func DefaultWorkspace() (string, error) {
	if env := os.Getenv("GROVE_WORKSPACE"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grove", "default"), nil
}

// Layout returns the canonical paths within a workspace.
type Layout struct {
	Root       string
	ConfigTOML string
	DB         string
	Trees      string
	Docs       string
	Auth       string
	Logs       string
}

func NewLayout(root string) Layout {
	return Layout{
		Root:       root,
		ConfigTOML: filepath.Join(root, "config.toml"),
		DB:         filepath.Join(root, "grove.db"),
		Trees:      filepath.Join(root, "trees"),
		Docs:       filepath.Join(root, "docs"),
		Auth:       filepath.Join(root, "auth"),
		Logs:       filepath.Join(root, "logs"),
	}
}

func (l Layout) Exists() bool {
	if _, err := os.Stat(l.ConfigTOML); err != nil {
		return false
	}
	return true
}

// PayloadPath returns the sharded on-disk path for a content-addressed
// payload under root, e.g. hash "ab3f..." → <root>/ab/ab3f.../<filename>.
// The 2-character prefix shard keeps directory listings small. Both the
// store (doc payloads) and the indexer (node payloads) address payloads
// through this shared convention.
func PayloadPath(root, hash, filename string) string {
	if len(hash) < 2 {
		return filepath.Join(root, hash, filename)
	}
	return filepath.Join(root, hash[:2], hash, filename)
}
