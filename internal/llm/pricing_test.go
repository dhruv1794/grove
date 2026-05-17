package llm

import "testing"

func TestCostFor(t *testing.T) {
	u := Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000}

	t.Run("known cloud model", func(t *testing.T) {
		c := costFor(ModelSpec{Provider: "openai", Name: "gpt-4o-mini"}, u)
		if c.USD != 0.75 || c.Estimated {
			t.Errorf("got %+v, want {0.75 false}", c)
		}
	})

	t.Run("local provider is free and exact", func(t *testing.T) {
		c := costFor(ModelSpec{Provider: "ollama", Name: "qwen2.5:32b"}, u)
		if c.USD != 0 || c.Estimated {
			t.Errorf("got %+v, want {0 false}", c)
		}
	})

	t.Run("unknown cloud model is flagged estimated", func(t *testing.T) {
		c := costFor(ModelSpec{Provider: "openai", Name: "gpt-99"}, u)
		if !c.Estimated {
			t.Errorf("got %+v, want Estimated=true", c)
		}
	})

	t.Run("zero usage on a cloud call is flagged estimated", func(t *testing.T) {
		c := costFor(ModelSpec{Provider: "openai", Name: "gpt-4o-mini"}, Usage{})
		if !c.Estimated {
			t.Errorf("got %+v, want Estimated=true", c)
		}
	})
}

func TestTally(t *testing.T) {
	var tally Tally
	tally.Add(&Response{
		Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		Cost:  Cost{USD: 0.01},
	})
	tally.Add(&Response{
		Usage: Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
		Cost:  Cost{USD: 0.02, Estimated: true},
	})
	tally.Add(nil) // ignored

	if tally.Calls != 2 {
		t.Errorf("Calls = %d, want 2", tally.Calls)
	}
	if tally.Usage.TotalTokens != 45 {
		t.Errorf("TotalTokens = %d, want 45", tally.Usage.TotalTokens)
	}
	if tally.USD != 0.03 {
		t.Errorf("USD = %v, want 0.03", tally.USD)
	}
	if !tally.Estimated {
		t.Error("Estimated should be true once any call was estimated")
	}
}
