package core

import "strings"

// JSONObjectSpan returns the first {...} span of s — from the first '{' to the
// last '}' — and whether one was found. Models sometimes wrap their JSON in
// prose or markdown fences; callers Unmarshal the returned span.
func JSONObjectSpan(s string) (string, bool) {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return "", false
	}
	return s[i : j+1], true
}

// TruncateRunes returns the first max runes of s. The byte-length fast path
// holds because a string's rune count never exceeds its byte count, and it
// avoids allocating a []rune copy of the whole (possibly large) string.
func TruncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
