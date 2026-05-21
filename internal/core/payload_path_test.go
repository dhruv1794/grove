package core

import (
	"path/filepath"
	"testing"
)

func TestPayloadPath_Sharding(t *testing.T) {
	got := PayloadPath("/root", "ab3f9c", "doc.json")
	want := filepath.Join("/root", "ab", "ab3f9c", "doc.json")
	if got != want {
		t.Errorf("PayloadPath = %q, want %q", got, want)
	}
}

func TestPayloadPath_ShortHash(t *testing.T) {
	// A hash shorter than the 2-char shard prefix must not panic; it falls
	// back to an unsharded path.
	got := PayloadPath("/root", "a", "doc.json")
	want := filepath.Join("/root", "a", "doc.json")
	if got != want {
		t.Errorf("PayloadPath short hash = %q, want %q", got, want)
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "hel"},
		{"héllo", 2, "hé"}, // multibyte: cuts on rune boundary, not byte
		{"", 5, ""},
	}
	for _, tt := range tests {
		if got := TruncateRunes(tt.in, tt.max); got != tt.want {
			t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}
