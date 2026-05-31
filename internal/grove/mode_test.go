package grove

import "testing"

func TestStagesForMode(t *testing.T) {
	cases := map[string]Stages{
		"fast":     {Fast: true},
		"balanced": {Fast: true, Decompose: true},
		"quality":  {Fast: true, Decompose: true, Rerank: true},
		"deep":     {Decompose: true, Rerank: true},
	}
	for mode, want := range cases {
		got, err := StagesForMode(mode)
		if err != nil {
			t.Fatalf("StagesForMode(%q): %v", mode, err)
		}
		if got != want {
			t.Errorf("StagesForMode(%q) = %+v, want %+v", mode, got, want)
		}
	}
	if _, err := StagesForMode("turbo"); err == nil {
		t.Error("StagesForMode(\"turbo\") = nil error, want misuse error")
	}
}

func TestCitedNums(t *testing.T) {
	cases := map[string][]int{
		"see [2][5] and [2]":   {2, 5},
		"see [1, 2] and [3,4]": {1, 2, 3, 4},
		"no citations here":    {},
		"mixed [1] and [2, 3]": {1, 2, 3},
	}
	for answer, want := range cases {
		got := CitedNums(answer)
		if len(got) != len(want) {
			t.Errorf("CitedNums(%q) = %v, want %v", answer, got, want)
			continue
		}
		for _, n := range want {
			if !got[n] {
				t.Errorf("CitedNums(%q) missing %d (got %v)", answer, n, got)
			}
		}
	}
}

func TestCitationDisplayAbstain(t *testing.T) {
	// Abstention answers carry no [n] markers but promise a list — must not suppress.
	_, showAll, suppress := CitationDisplay("none is a strong match — listed below", false, true)
	if suppress || !showAll {
		t.Errorf("abstain: showAll=%v suppress=%v, want showAll=true suppress=false", showAll, suppress)
	}
}
