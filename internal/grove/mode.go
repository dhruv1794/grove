package grove

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"grove/internal/core"
)

// Stages is the set of retrieval stages a --mode selects. It lives in the core
// library so every adapter (CLI, REPL, web) shares one mode→stages mapping;
// query.Options stays primitive booleans, with no notion of "modes".
type Stages struct {
	Fast      bool // no LLM tree descent (FTS + embeddings only)
	Decompose bool // split a multi-aspect question into sub-queries
	Rerank    bool // graded LLM reorder of fused candidates
}

// StagesForMode maps a mode name to its stages. Defaults derive from the
// retrieval benchmark (documents/benchmark-findings.md):
//   - fast: instant, model-free with retrieve-only.
//   - balanced (default): + decompose — big coverage win, model-robust (8b ok).
//   - quality: + rerank — best ranking, but needs a STRONG model (8b is harmful).
//   - deep: tree descent + decompose + rerank — explainable navigation path.
func StagesForMode(mode string) (Stages, error) {
	switch mode {
	case "fast":
		return Stages{Fast: true}, nil
	case "balanced":
		return Stages{Fast: true, Decompose: true}, nil
	case "quality":
		return Stages{Fast: true, Decompose: true, Rerank: true}, nil
	case "deep":
		return Stages{Decompose: true, Rerank: true}, nil
	default:
		return Stages{}, core.NewError(core.KindMisuse,
			fmt.Sprintf("unknown mode %q", mode),
			"use one of: fast, balanced, quality, deep")
	}
}

// citationMarkerRe matches a bracketed run of comma/space-separated numbers so
// that both the prompt's instructed form ([2][5]) and the common deviation
// [1, 2] are captured. The inner numbers are split out by CitedNums.
var citationMarkerRe = regexp.MustCompile(`\[([\d,\s]+)\]`)

// CitedNums returns the citation numbers actually referenced in an answer via
// [n] markers, including comma-joined forms like [1, 2].
func CitedNums(answer string) map[int]bool {
	out := map[int]bool{}
	for _, m := range citationMarkerRe.FindAllStringSubmatch(answer, -1) {
		for _, f := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }) {
			if n, err := strconv.Atoi(f); err == nil {
				out[n] = true
			}
		}
	}
	return out
}

// CitationDisplay decides which retrieved citations to show. With a synthesized
// answer, show only those the answer cites via [n]; if it cites none (e.g. the
// answer says the topic isn't in the corpus), suppress the list entirely — the
// retrieved candidates are just noise. In retrieve-only mode there is no answer
// to cite from, so show every retrieved doc (that's the point of the mode); on
// abstention the answer promises a list of closest matches, so show them all.
func CitationDisplay(answer string, retrieveOnly, abstained bool) (cited map[int]bool, showAll, suppress bool) {
	if retrieveOnly || abstained {
		return nil, true, false
	}
	cited = CitedNums(answer)
	if len(cited) == 0 {
		return nil, false, true
	}
	return cited, false, false
}
