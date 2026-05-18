// Package prompts holds grove's versioned prompt templates. Prompts are
// treated as code: each is embedded into the binary and carries a stable
// version. That version is part of the node cache key, so bumping a prompt
// cleanly invalidates exactly the slice of the forest it built.
package prompts

import _ "embed"

// Prompt is a versioned prompt template.
type Prompt struct {
	Name     string
	Version  string
	Template string
}

// Ver is the cache-key identifier for this prompt, e.g. "node/v1".
func (p Prompt) Ver() string { return p.Name + "/" + p.Version }

//go:embed node.v1.txt
var nodeV1 string

// Node is the system prompt for generating a tree node's title and summary.
// It is used for both leaf nodes (one document) and internal nodes (a section
// grouping subsections) — the task is identical, only the supplied content
// differs.
var Node = Prompt{Name: "node", Version: "v1", Template: nodeV1}

//go:embed navigate.v1.txt
var navigateV1 string

// Navigate is the system prompt for choosing which options (trees, or a
// node's children) to descend into when answering a query.
var Navigate = Prompt{Name: "navigate", Version: "v1", Template: navigateV1}

//go:embed answer.v1.txt
var answerV1 string

// Answer is the system prompt for synthesizing a cited answer from retrieved
// source excerpts.
var Answer = Prompt{Name: "answer", Version: "v1", Template: answerV1}
