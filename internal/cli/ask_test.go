package cli

import "testing"

func TestStagesForMode(t *testing.T) {
	cases := map[string]askStages{
		"fast":     {fast: true},
		"balanced": {fast: true, decompose: true},
		"quality":  {fast: true, decompose: true, rerank: true},
		"deep":     {decompose: true, rerank: true},
	}
	for mode, want := range cases {
		got, err := stagesForMode(mode)
		if err != nil {
			t.Fatalf("stagesForMode(%q): %v", mode, err)
		}
		if got != want {
			t.Errorf("stagesForMode(%q) = %+v, want %+v", mode, got, want)
		}
	}
	if _, err := stagesForMode("turbo"); err == nil {
		t.Error("stagesForMode(\"turbo\") = nil error, want misuse error")
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
		got := citedNums(answer)
		if len(got) != len(want) {
			t.Errorf("citedNums(%q) = %v, want %v", answer, got, want)
			continue
		}
		for _, n := range want {
			if !got[n] {
				t.Errorf("citedNums(%q) missing %d (got %v)", answer, n, got)
			}
		}
	}
}

func TestCitationDisplayAbstain(t *testing.T) {
	// Abstention answers carry no [n] markers but promise a list — must not suppress.
	_, showAll, suppress := citationDisplay("none is a strong match — listed below", false, true)
	if suppress || !showAll {
		t.Errorf("abstain: showAll=%v suppress=%v, want showAll=true suppress=false", showAll, suppress)
	}
}
