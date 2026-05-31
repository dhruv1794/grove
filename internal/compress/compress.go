// Package compress is grove's thin seam over the mdcompress library: an opt-in
// transform that strips boilerplate from document text before it is fed to node
// summarization (build) and embedding, to cut tokens/cost and noise. Connectors
// stay pure-normalization; this runs at build/embed time, not at ingest.
//
// Only the deterministic tiers (safe, aggressive) are wired. The LLM tier is a
// planned follow-up (it needs a model + cost accounting).
package compress

import (
	"fmt"
	"strings"

	md "github.com/dhruv1794/mdcompress/pkg/compress"
	tiktoken "github.com/pkoukk/tiktoken-go"
	tiktokenloader "github.com/pkoukk/tiktoken-go-loader"
	"grove/internal/core"
)

func init() {
	// mdcompress counts tokens via tiktoken-go, which by default downloads its
	// BPE vocabulary from the network on first use — a hidden cloud call grove
	// forbids (no command hits the network unless the user picked a model that
	// needs it). Register the offline loader (vocabulary embedded in the binary)
	// so the deterministic tiers stay fully local.
	tiktoken.SetBpeLoader(tiktokenloader.NewOfflineLoader())
}

// Level selects how aggressively text is compressed. None is a no-op and makes
// no call into the dependency.
type Level string

const (
	None       Level = "none"
	Safe       Level = "safe"       // mdcompress Tier 1: deterministic, conservative
	Aggressive Level = "aggressive" // mdcompress Tier 2: deterministic, more rules
)

// ParseLevel resolves a flag/env/config string to a Level. Empty or "none" is
// None; "llm" is recognized but not yet wired (deferred); other values error.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(None):
		return None, nil
	case string(Safe):
		return Safe, nil
	case string(Aggressive):
		return Aggressive, nil
	case "llm":
		return None, core.NewError(core.KindMisuse,
			"compression tier \"llm\" is not yet wired",
			"use --compress safe or aggressive (deterministic); the LLM tier is a planned follow-up")
	default:
		return None, core.NewError(core.KindMisuse,
			fmt.Sprintf("unknown compression level %q", s),
			"use one of: none, safe, aggressive")
	}
}

// tier maps a Level to its mdcompress tier; ok is false for None.
func (l Level) tier() (md.Tier, bool) {
	switch l {
	case Safe:
		return md.TierSafe, true
	case Aggressive:
		return md.TierAggressive, true
	default:
		return 0, false
	}
}

// CacheToken is the token folded into the node cache key so toggling
// compression invalidates cached summaries. Empty for None, which preserves
// existing cache filenames for the common uncompressed case.
func (l Level) CacheToken() string {
	if l == None {
		return ""
	}
	return string(l)
}

// Stats reports the token delta from one compression.
type Stats struct {
	TokensBefore int
	TokensAfter  int
}

// Saved is the token reduction (before − after); may be ≤0 if compression
// didn't help a particular document.
func (s Stats) Saved() int { return s.TokensBefore - s.TokensAfter }

// Apply compresses text at level. None (or empty text) returns the input
// unchanged with zero stats and no dependency call. On a compression error the
// original text is returned alongside the error so the caller can fall back to
// uncompressed input rather than abort the run.
func Apply(level Level, text string) (string, Stats, error) {
	tier, ok := level.tier()
	if !ok || text == "" {
		return text, Stats{}, nil
	}
	res, err := md.Compress([]byte(text), md.Options{Tier: tier})
	if err != nil {
		return text, Stats{}, fmt.Errorf("mdcompress (%s): %w", level, err)
	}
	return string(res.Output), Stats{TokensBefore: res.TokensBefore, TokensAfter: res.TokensAfter}, nil
}
