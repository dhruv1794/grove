package cli

import (
	"os"
	"strings"
	"testing"
)

func TestColorize(t *testing.T) {
	if got := colorize(false, ansiRed, "hi"); got != "hi" {
		t.Errorf("disabled colorize = %q, want %q", got, "hi")
	}
	got := colorize(true, ansiRed, "hi")
	if !strings.HasPrefix(got, ansiRed) || !strings.HasSuffix(got, ansiReset) {
		t.Errorf("enabled colorize = %q, want %s-wrapped", got, "ANSI")
	}
}

func TestUseColor_NoColorFlag(t *testing.T) {
	t.Cleanup(func() { gflags.NoColor = false })
	gflags.NoColor = true
	if useColor(os.Stdout) {
		t.Error("useColor returned true with --no-color set")
	}
}

func TestUseColor_NoColorEnv(t *testing.T) {
	t.Cleanup(func() { gflags.NoColor = false })
	gflags.NoColor = false
	t.Setenv("NO_COLOR", "1")
	if useColor(os.Stdout) {
		t.Error("useColor returned true with NO_COLOR set")
	}
}

// A regular file is never a terminal, so color stays off for piped or
// redirected output.
func TestUseColor_NonTerminal(t *testing.T) {
	t.Cleanup(func() { gflags.NoColor = false })
	gflags.NoColor = false
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if useColor(f) {
		t.Error("useColor returned true for a regular file")
	}
}
