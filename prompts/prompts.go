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

//go:embed navigate.v2.txt
var navigateV2 string

// Navigate is the system prompt for choosing which options (trees, or a
// node's children) to descend into when answering a query. v2 is recall-
// oriented: greedy precision (v1) caused descent to commit to one branch and
// miss the answer (see the benchmark write-up).
var Navigate = Prompt{Name: "navigate", Version: "v2", Template: navigateV2}

//go:embed group.v1.txt
var groupV1 string

// Group is the system prompt for clustering a flat list of documents into
// topical sub-groups when a folder has many sibling files and no structure.
var Group = Prompt{Name: "group", Version: "v1", Template: groupV1}

//go:embed answer.v2.txt
var answerV2 string

// Answer is the system prompt for synthesizing a cited answer from retrieved
// source excerpts. v2 tightens citation discipline — markers must point to the
// excerpt that actually contains the claim — after the judge benchmark found v1
// answers faithful but mis-citing (citation_validity 3.7/10).
var Answer = Prompt{Name: "answer", Version: "v2", Template: answerV2}

//go:embed decompose.v1.txt
var decomposeV1 string

// Decompose is the system prompt for splitting a multi-aspect question into
// per-aspect sub-queries, each retrieved separately and fused. A single
// blended query under-retrieves docs that live under a different aspect.
var Decompose = Prompt{Name: "decompose", Version: "v1", Template: decomposeV1}

//go:embed prune.v1.txt
var pruneV1 string

// Prune is the system prompt for the binary relevance filter that runs after
// retrieval fusion: per candidate document, a YES/NO on whether it could
// contribute to answering the question. A precision stage over high-recall
// fused candidates.
var Prune = Prompt{Name: "prune", Version: "v1", Template: pruneV1}

//go:embed rerank.v1.txt
var rerankV1 string

// Rerank is the system prompt for the graded relevance reranker: per candidate
// document, a 0–10 score for how well it answers the question. Reorders the
// fused pool — the precision-at-1 stage decomposition's wider recall needs.
var Rerank = Prompt{Name: "rerank", Version: "v1", Template: rerankV1}
