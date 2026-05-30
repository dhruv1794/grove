package grove

import "testing"

func TestSplitSentences(t *testing.T) {
	got := splitSentences("First sentence. Second one! Third?\n\nNew paragraph here.")
	want := []string{"First sentence.", "Second one!", "Third?", "New paragraph here."}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// no terminal punctuation → whole line is one unit
	if s := splitSentences("a heading with no period"); len(s) != 1 {
		t.Errorf("no-punct line = %q, want 1 unit", s)
	}
}

func TestCosine32(t *testing.T) {
	if got := cosine32([]float32{1, 0}, []float32{1, 0}); got < 0.999 {
		t.Errorf("identical = %v, want ~1", got)
	}
	if got := cosine32([]float32{1, 0}, []float32{0, 1}); got > 0.001 {
		t.Errorf("orthogonal = %v, want ~0", got)
	}
	if got := cosine32([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero vector = %v, want 0", got)
	}
}
