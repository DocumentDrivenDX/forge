package session

// ModelPricing holds per-million-token costs for a model.
type ModelPricing struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// PricingTable maps model IDs to their pricing.
type PricingTable map[string]ModelPricing

// DefaultPricing contains built-in pricing for common models.
var DefaultPricing = PricingTable{
	// Anthropic
	"claude-sonnet-4-20250514": {InputPerMTok: 3.00, OutputPerMTok: 15.00},
	"claude-haiku-4-20250414":  {InputPerMTok: 0.80, OutputPerMTok: 4.00},
	"claude-opus-4-20250515":   {InputPerMTok: 15.00, OutputPerMTok: 75.00},

	// OpenAI
	"gpt-4o":      {InputPerMTok: 2.50, OutputPerMTok: 10.00},
	"gpt-4o-mini": {InputPerMTok: 0.15, OutputPerMTok: 0.60},
	"gpt-4.1":     {InputPerMTok: 2.00, OutputPerMTok: 8.00},
	"o3-mini":     {InputPerMTok: 1.10, OutputPerMTok: 4.40},

	// Local models (free)
	"qwen3.5-7b":   {InputPerMTok: 0, OutputPerMTok: 0},
	"llama-3.2-8b": {InputPerMTok: 0, OutputPerMTok: 0},
}

// EstimateCost returns the estimated cost in USD for the given token usage.
// Returns -1 if the model is not in the pricing table (unknown).
// Returns 0 if the model is free (local).
func (pt PricingTable) EstimateCost(model string, inputTokens, outputTokens int) float64 {
	pricing, ok := pt[model]
	if !ok {
		return -1
	}
	inputCost := float64(inputTokens) / 1_000_000 * pricing.InputPerMTok
	outputCost := float64(outputTokens) / 1_000_000 * pricing.OutputPerMTok
	return inputCost + outputCost
}
