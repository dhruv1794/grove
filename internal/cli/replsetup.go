package cli

import (
	"fmt"
	"io"
	"strings"

	"grove/internal/grove"
)

// isSetupCmd reports whether line is a /setup command and returns the source
// type. Empty type with ok=true means the user typed bare `/setup`, which
// prints usage.
func isSetupCmd(line string) (srcType string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	if c := "/" + strings.TrimLeft(fields[0], "/:"); c != "/setup" {
		return "", false
	}
	if len(fields) < 2 {
		return "", true
	}
	return strings.ToLower(fields[1]), true
}

// renderSetupStatus prints a setup status block for the user. Secrets are
// shown only as "set"/"unset", never the value.
func renderSetupStatus(w io.Writer, s *grove.SetupStatus) {
	fmt.Fprintf(w, "setup %s — edit %s\n", s.Type, s.Path)
	for _, f := range s.Fields {
		mark := "✗"
		val := f.Hint
		if f.Set {
			mark = "✓"
			if f.Secret {
				val = "set"
			} else if f.Display != "" {
				val = f.Display
			} else {
				val = "set"
			}
		}
		fmt.Fprintf(w, "  %s %-15s %s\n", mark, f.Key, val)
	}
}
