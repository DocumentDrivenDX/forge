package core

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/DocumentDrivenDX/agent/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelPricingHasCacheFields is an AST guard pinning CacheReadPerM and
// CacheWritePerM on core.ModelPricing so cache-cost attribution has a typed
// home. Bead D (agent-6e2ebcdb) AC#1.
func TestModelPricingHasCacheFields(t *testing.T) {
	requireStructHasFieldCore(t, "pricing.go", "ModelPricing", "CacheReadPerM")
	requireStructHasFieldCore(t, "pricing.go", "ModelPricing", "CacheWritePerM")
}

func requireStructHasFieldCore(t *testing.T, file, structName, field string) {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var found bool
	ast.Inspect(parsed, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != structName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				if name.Name == field {
					found = true
					return false
				}
			}
		}
		return false
	})
	if !found {
		t.Fatalf("expected struct %s in %s to declare field %s", structName, file, field)
	}
}

// TestNativeLoopPopulatesCacheCostAttribution verifies that when the
// native loop falls back to per-MTok configured pricing, cache-read and
// cache-write costs are computed from the manifest cache rates and surfaced
// on CostAttribution.CacheReadAmount / CacheWriteAmount as non-nil pointers.
// Bead D AC#3.
func TestNativeLoopPopulatesCacheCostAttribution(t *testing.T) {
	const inputPerM, outputPerM = 3.0, 15.0
	const cacheReadPerM, cacheWritePerM = 0.30, 3.75

	provider := &mockProvider{
		responses: []Response{
			{
				Content: "done",
				Usage: TokenUsage{
					Input:      10_000,
					Output:     2_000,
					CacheRead:  1_000,
					CacheWrite: 500,
					Total:      13_500,
				},
				Model: "claude-sonnet-4.6",
				Attempt: &AttemptMetadata{
					ProviderName:   "anthropic",
					ProviderSystem: "anthropic",
					RequestedModel: "claude-sonnet-4.6",
					ResponseModel:  "claude-sonnet-4.6",
					ResolvedModel:  "claude-sonnet-4.6",
					Cost:           &CostAttribution{Source: CostSourceUnknown},
				},
			},
		},
	}

	tel := telemetry.New(telemetry.Config{
		Pricing: telemetry.RuntimePricing{
			"anthropic": {
				"claude-sonnet-4.6": {
					InputPerMTok:   inputPerM,
					OutputPerMTok:  outputPerM,
					CacheReadPerM:  cacheReadPerM,
					CacheWritePerM: cacheWritePerM,
					Currency:       "USD",
					PricingRef:     "anthropic/claude-sonnet-4.6",
				},
			},
		},
	})

	result, err := Run(context.Background(), Request{
		Prompt:    "test",
		Provider:  provider,
		Telemetry: tel,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)

	cost := provider.responses[0].Attempt.Cost
	require.NotNil(t, cost)
	assert.Equal(t, CostSourceConfigured, cost.Source)

	wantCacheRead := 1000.0 / 1_000_000 * cacheReadPerM
	wantCacheWrite := 500.0 / 1_000_000 * cacheWritePerM
	wantTotal := 10000.0/1_000_000*inputPerM +
		2000.0/1_000_000*outputPerM +
		wantCacheRead + wantCacheWrite

	require.NotNil(t, cost.CacheReadAmount, "CacheReadAmount must be non-nil when cache rates configured")
	require.NotNil(t, cost.CacheWriteAmount, "CacheWriteAmount must be non-nil when cache rates configured")
	assert.InDelta(t, wantCacheRead, *cost.CacheReadAmount, 1e-12)
	assert.InDelta(t, wantCacheWrite, *cost.CacheWriteAmount, 1e-12)

	require.NotNil(t, cost.Amount)
	assert.InDelta(t, wantTotal, *cost.Amount, 1e-12)
	assert.InDelta(t, wantTotal, result.CostUSD, 1e-12)
}

// TestCachePolicyOffZerosCacheAttribution verifies that when CachePolicy is
// "off", CacheReadAmount and CacheWriteAmount are emitted as explicit
// *float64(0.0) — distinguishing "caller opted out" from "harness/provider
// did not report" (nil). Bead D AC#4.
func TestCachePolicyOffZerosCacheAttribution(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{
				Content: "done",
				Usage:   TokenUsage{Input: 10_000, Output: 2_000, Total: 12_000},
				Model:   "claude-sonnet-4.6",
				Attempt: &AttemptMetadata{
					ProviderName:   "anthropic",
					ProviderSystem: "anthropic",
					RequestedModel: "claude-sonnet-4.6",
					ResponseModel:  "claude-sonnet-4.6",
					ResolvedModel:  "claude-sonnet-4.6",
					Cost:           &CostAttribution{Source: CostSourceUnknown},
				},
			},
		},
	}

	tel := telemetry.New(telemetry.Config{
		Pricing: telemetry.RuntimePricing{
			"anthropic": {
				"claude-sonnet-4.6": {
					InputPerMTok:   3.0,
					OutputPerMTok:  15.0,
					CacheReadPerM:  0.30,
					CacheWritePerM: 3.75,
					Currency:       "USD",
					PricingRef:     "anthropic/claude-sonnet-4.6",
				},
			},
		},
	})

	_, err := Run(context.Background(), Request{
		Prompt:      "test",
		Provider:    provider,
		Telemetry:   tel,
		CachePolicy: "off",
	})
	require.NoError(t, err)

	cost := provider.responses[0].Attempt.Cost
	require.NotNil(t, cost)
	require.NotNil(t, cost.CacheReadAmount, "CachePolicy=off must emit explicit *float64(0.0), not nil")
	require.NotNil(t, cost.CacheWriteAmount, "CachePolicy=off must emit explicit *float64(0.0), not nil")
	assert.Equal(t, 0.0, *cost.CacheReadAmount)
	assert.Equal(t, 0.0, *cost.CacheWriteAmount)
}
