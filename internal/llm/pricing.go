package llm

// priceRate is USD per 1,000,000 tokens.
type priceRate struct {
	inputPerMTok  float64
	outputPerMTok float64
}

// modelPricing is keyed by ModelSpec.String(). Figures are indicative list
// prices and drift as providers update them — keep this table current, and
// note that any cloud model absent here yields Cost.Estimated=true so the CLI
// flags the cost as unknown rather than implying $0.
var modelPricing = map[string]priceRate{
	"openai/gpt-4o-mini":          {0.15, 0.60},
	"openai/gpt-4o":               {2.50, 10.00},
	"anthropic/claude-haiku-4-5":  {1.00, 5.00},
	"anthropic/claude-sonnet-4-5": {3.00, 15.00},
}

// localProviders run on the user's own hardware — no per-token bill.
var localProviders = map[string]bool{
	"ollama":   true,
	"vllm":     true,
	"llamacpp": true,
}

// costFor computes the USD cost of one call. Local providers cost $0 exactly.
// A cloud model with no pricing entry, or a call with no reported usage,
// returns Estimated=true.
func costFor(spec ModelSpec, u Usage) Cost {
	if localProviders[spec.Provider] {
		return Cost{USD: 0, Estimated: false}
	}
	rate, ok := modelPricing[spec.String()]
	if !ok {
		return Cost{USD: 0, Estimated: true}
	}
	usd := float64(u.PromptTokens)/1e6*rate.inputPerMTok +
		float64(u.CompletionTokens)/1e6*rate.outputPerMTok
	return Cost{USD: usd, Estimated: u.TotalTokens == 0}
}
