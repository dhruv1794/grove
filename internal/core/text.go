package core

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
