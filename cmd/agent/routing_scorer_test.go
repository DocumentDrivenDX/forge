package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCandidateScorerFunc_Passthrough verifies the func adapter forwards correctly.
func TestCandidateScorerFunc_Passthrough(t *testing.T) {
	var capturedProvider, capturedModel string
	var capturedBase float64

	scorer := CandidateScorerFunc(func(provider, model string, baseScore float64) float64 {
		capturedProvider = provider
		capturedModel = model
		capturedBase = baseScore
		return baseScore
	})

	result := scorer.Score("anthropic", "claude-opus-4", 0.75)
	assert.Equal(t, 0.75, result)
	assert.Equal(t, "anthropic", capturedProvider)
	assert.Equal(t, "claude-opus-4", capturedModel)
	assert.Equal(t, 0.75, capturedBase)
}

// TestCandidateScorerFunc_QuotaExhaustion verifies returning -1 is passed through as-is.
func TestCandidateScorerFunc_QuotaExhaustion(t *testing.T) {
	scorer := CandidateScorerFunc(func(provider, model string, baseScore float64) float64 {
		return -1
	})

	result := scorer.Score("anthropic", "claude-opus-4", 0.9)
	assert.Equal(t, -1.0, result)
}

// TestScoreSmartRouteCandidates_ScorerExcludes verifies a scorer that returns -1 for
// one provider causes it to be excluded from the order.
func TestScoreSmartRouteCandidates_ScorerExcludes(t *testing.T) {
	plan := &smartRoutePlan{
		Candidates: []smartRouteCandidate{
			{
				Provider:    "cloud",
				Model:       "claude-opus-4",
				Healthy:     true,
				Reliability: 1.0,
			},
			{
				Provider:    "local",
				Model:       "qwen3.5-27b",
				Healthy:     true,
				Reliability: 0.9,
			},
		},
		scorer: CandidateScorerFunc(func(provider, model string, baseScore float64) float64 {
			if provider == "cloud" {
				return -1 // quota exhausted
			}
			return baseScore
		}),
	}

	order := scoreSmartRouteCandidates(plan, 0, nil)

	// cloud should be excluded, only local should remain
	assert.Len(t, order, 1)
	assert.Equal(t, 1, order[0]) // local is index 1

	// cloud should be marked unhealthy
	assert.False(t, plan.Candidates[0].Healthy)
	assert.Equal(t, "excluded by scorer", plan.Candidates[0].Reason)

	// local should still be healthy
	assert.True(t, plan.Candidates[1].Healthy)
}

// TestScoreSmartRouteCandidates_ScorerBoost verifies a scorer that boosts one provider's
// score causes it to rank first even if it had lower base score.
func TestScoreSmartRouteCandidates_ScorerBoost(t *testing.T) {
	plan := &smartRoutePlan{
		Candidates: []smartRouteCandidate{
			{
				Provider:    "cloud",
				Model:       "claude-opus-4",
				Healthy:     true,
				Reliability: 1.0, // high reliability → high base score
			},
			{
				Provider:    "local",
				Model:       "qwen3.5-27b",
				Healthy:     true,
				Reliability: 0.5, // lower reliability → lower base score
			},
		},
		scorer: CandidateScorerFunc(func(provider, model string, baseScore float64) float64 {
			if provider == "local" {
				return baseScore + 0.5 // priority quota boost
			}
			return baseScore
		}),
	}

	order := scoreSmartRouteCandidates(plan, 0, nil)

	assert.Len(t, order, 2)
	// local should rank first due to boost
	assert.Equal(t, 1, order[0]) // local is index 1
	assert.Equal(t, 0, order[1]) // cloud is index 0

	// Both should remain healthy
	assert.True(t, plan.Candidates[0].Healthy)
	assert.True(t, plan.Candidates[1].Healthy)
}
