package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPricingTable_EstimateCost(t *testing.T) {
	pt := DefaultPricing

	t.Run("known cloud model", func(t *testing.T) {
		cost := pt.EstimateCost("claude-sonnet-4-20250514", 1000, 500)
		// 1000/1M * 3.00 + 500/1M * 15.00 = 0.003 + 0.0075 = 0.0105
		assert.InDelta(t, 0.0105, cost, 0.0001)
	})

	t.Run("local model is free", func(t *testing.T) {
		cost := pt.EstimateCost("qwen3.5-7b", 10000, 5000)
		assert.Equal(t, 0.0, cost)
	})

	t.Run("unknown model returns -1", func(t *testing.T) {
		cost := pt.EstimateCost("unknown-model-xyz", 1000, 500)
		assert.Equal(t, -1.0, cost)
	})

	t.Run("zero tokens", func(t *testing.T) {
		cost := pt.EstimateCost("gpt-4o", 0, 0)
		assert.Equal(t, 0.0, cost)
	})

	t.Run("large token counts", func(t *testing.T) {
		cost := pt.EstimateCost("gpt-4o", 1_000_000, 1_000_000)
		// 1M/1M * 2.50 + 1M/1M * 10.00 = 12.50
		assert.InDelta(t, 12.50, cost, 0.01)
	})
}
