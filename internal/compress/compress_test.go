package compress

import (
	"errors"
	"testing"

	"grove/internal/core"
)

func TestParseLevel(t *testing.T) {
	ok := map[string]Level{
		"":           None,
		"none":       None,
		"NONE":       None,
		"  safe  ":   Safe,
		"safe":       Safe,
		"Aggressive": Aggressive,
	}
	for in, want := range ok {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
	// llm is recognized but not yet wired; an unknown value is misuse.
	for _, in := range []string{"llm", "bogus"} {
		_, err := ParseLevel(in)
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Kind != core.KindMisuse {
			t.Errorf("ParseLevel(%q) = %v; want a misuse error", in, err)
		}
	}
}

func TestCacheToken(t *testing.T) {
	// None must produce the empty token so existing (uncompressed) node caches
	// keep their filenames; non-default levels must produce a stable suffix.
	if None.CacheToken() != "" {
		t.Errorf("None.CacheToken() = %q, want empty", None.CacheToken())
	}
	if Safe.CacheToken() != "safe" || Aggressive.CacheToken() != "aggressive" {
		t.Errorf("CacheToken mismatch: safe=%q aggressive=%q", Safe.CacheToken(), Aggressive.CacheToken())
	}
}

func TestApplyNoneIsPassthrough(t *testing.T) {
	in := "# Title\n\n\nsome   body\n\n"
	out, stats, err := Apply(None, in)
	if err != nil || out != in || stats != (Stats{}) {
		t.Fatalf("Apply(None) = %q, %+v, %v; want input unchanged, zero stats, nil", out, stats, err)
	}
	// Empty text is a no-op at any level (no dependency call).
	if out, _, err := Apply(Aggressive, ""); err != nil || out != "" {
		t.Fatalf("Apply(Aggressive, \"\") = %q, %v; want \"\", nil", out, err)
	}
}

func TestApplyCompresses(t *testing.T) {
	// Boilerplate the deterministic tiers should strip: trailing whitespace,
	// runs of blank lines, an HTML comment.
	in := "# Heading   \n\n\n\n<!-- a tracking comment -->\n\nReal content here.   \n\n\n"
	out, stats, err := Apply(Aggressive, in)
	if err != nil {
		t.Fatalf("Apply(Aggressive) error: %v", err)
	}
	if stats.TokensBefore <= 0 {
		t.Errorf("TokensBefore = %d, want > 0 (input was tokenized)", stats.TokensBefore)
	}
	if len(out) > len(in) {
		t.Errorf("compressed output grew: %d > %d bytes", len(out), len(in))
	}
	if stats.Saved() < 0 {
		t.Errorf("Saved() = %d, want >= 0", stats.Saved())
	}
}
