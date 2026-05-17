package cli

import "os"

const (
	ansiReset = "\x1b[0m"
	ansiRed   = "\x1b[31m"
	ansiDim   = "\x1b[2m"
)

// useColor reports whether colored output should be written to f. Color is
// off when --no-color is set, when NO_COLOR is present in the environment
// (https://no-color.org), or when f is not a terminal (piped/redirected).
func useColor(f *os.File) bool {
	if gflags.NoColor {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return isTerminal(f)
}

// isTerminal reports whether f is a character device, i.e. an interactive
// terminal rather than a pipe or regular file.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// colorize wraps s in an ANSI code when enabled, otherwise returns s as-is.
func colorize(enabled bool, code, s string) string {
	if !enabled {
		return s
	}
	return code + s + ansiReset
}
