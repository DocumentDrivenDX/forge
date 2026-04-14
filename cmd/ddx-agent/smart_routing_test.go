package main

import (
	"testing"
	"time"

	agentConfig "github.com/DocumentDrivenDX/agent/config"
	"github.com/stretchr/testify/assert"
)

func TestRoutingWeights_Defaults(t *testing.T) {
	cfg := &agentConfig.Config{}
	rel, perf, load, cost, cap := routingWeights(cfg)
	assert.InDelta(t, 0.35, rel, 0.001)
	assert.InDelta(t, 0.20, perf, 0.001)
	assert.InDelta(t, 0.15, load, 0.001)
	assert.InDelta(t, 0.20, cost, 0.001)
	assert.InDelta(t, 0.10, cap, 0.001)
}

func TestRoutingWeights_Custom(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			ReliabilityWeight: 0.5,
			PerformanceWeight: 0.2,
			LoadWeight:        0.1,
			CostWeight:        0.1,
			CapabilityWeight:  0.1,
		},
	}
	rel, perf, load, cost, cap := routingWeights(cfg)
	// Should be normalized (total = 1.0)
	assert.InDelta(t, 0.50, rel, 0.001)
	assert.InDelta(t, 0.20, perf, 0.001)
	assert.InDelta(t, 0.10, load, 0.001)
	assert.InDelta(t, 0.10, cost, 0.001)
	assert.InDelta(t, 0.10, cap, 0.001)
}

func TestRoutingWeights_SumToOne(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			ReliabilityWeight: 0.3,
			PerformanceWeight: 0.3,
			LoadWeight:        0.2,
			CostWeight:        0.1,
			CapabilityWeight:  0.1,
		},
	}
	rel, perf, load, cost, cap := routingWeights(cfg)
	total := rel + perf + load + cost + cap
	assert.InDelta(t, 1.0, total, 0.001)
}

func TestSmartRouteHistory_ReliabilityScore_Empty(t *testing.T) {
	h := smartRouteHistory{}
	assert.Equal(t, 0.5, h.ReliabilityScore())
}

func TestSmartRouteHistory_ReliabilityScore_AllSuccess(t *testing.T) {
	h := smartRouteHistory{
		Samples:   10,
		Successes: 10,
		Failures:  0,
	}
	assert.Equal(t, 1.0, h.ReliabilityScore())
}

func TestSmartRouteHistory_ReliabilityScore_Mixed(t *testing.T) {
	h := smartRouteHistory{
		Samples:   10,
		Successes: 7,
		Failures:  3,
	}
	assert.Equal(t, 0.7, h.ReliabilityScore())
}

func TestSmartRouteHistory_ReliabilityScore_AllFailure(t *testing.T) {
	h := smartRouteHistory{
		Samples:   10,
		Successes: 0,
		Failures:  10,
	}
	assert.Equal(t, 0.0, h.ReliabilityScore())
}

func TestRoutingHistoryWindow_Config(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			HistoryWindow: "48h",
		},
	}
	window := routingHistoryWindow(cfg)
	assert.Equal(t, 48*time.Hour, window)
}

func TestRoutingHistoryWindow_Default(t *testing.T) {
	cfg := &agentConfig.Config{}
	window := routingHistoryWindow(cfg)
	assert.Equal(t, defaultRoutingHistoryWindow, window)
}

func TestRoutingHistoryWindow_Invalid(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			HistoryWindow: "invalid",
		},
	}
	window := routingHistoryWindow(cfg)
	assert.Equal(t, defaultRoutingHistoryWindow, window)
}

func TestRoutingProbeTimeout_Config(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			ProbeTimeout: "10s",
		},
	}
	timeout := routingProbeTimeout(cfg)
	assert.Equal(t, 10*time.Second, timeout)
}

func TestRoutingProbeTimeout_Default(t *testing.T) {
	cfg := &agentConfig.Config{}
	timeout := routingProbeTimeout(cfg)
	assert.Equal(t, defaultRoutingProbeTimeout, timeout)
}

func TestSynthesizeIntentRoute(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {Type: "openai-compat"},
			"cloud": {Type: "anthropic"},
		},
	}

	route := synthesizeIntentRoute(cfg, "qwen3.5-27b", "")
	assert.Equal(t, "smart", route.Strategy)
	assert.Len(t, route.Candidates, 2)

	// Check that both providers are present (order depends on alphabetical sorting)
	providerMap := make(map[string]string)
	for _, c := range route.Candidates {
		providerMap[c.Provider] = c.Model
	}
	assert.Contains(t, providerMap, "local")
	assert.Contains(t, providerMap, "cloud")
	assert.Equal(t, "qwen3.5-27b", providerMap["local"])
	assert.Equal(t, "qwen3.5-27b", providerMap["cloud"])
}

func TestSynthesizeIntentRoute_WithModelRef(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {Type: "openai-compat"},
		},
	}

	route := synthesizeIntentRoute(cfg, "qwen3.5-27b", "code-fast")
	assert.Equal(t, "smart", route.Strategy)
	assert.Len(t, route.Candidates, 1)
	assert.Equal(t, "local", route.Candidates[0].Provider)
	assert.Equal(t, "", route.Candidates[0].Model) // Model should be empty when modelRef is set
}

func TestModelFamily(t *testing.T) {
	testCases := []struct {
		model    string
		expected string
	}{
		{"claude-3-5-sonnet-20241022", "claude"},
		{"claude-opus-4-20250514", "claude"},
		{"qwen3.5-7b", "qwen"},
		{"qwen2.5-72b", "qwen"},
		{"gpt-5.4", "gpt"},
		{"gpt-4o", "gpt"},
		{"gemini-2.0-flash", "gemini"},
		{"llama-3.2-3b", "llama"},
		{"unknown-model", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.model, func(t *testing.T) {
			result := modelFamily(tc.model)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestComparableModelName(t *testing.T) {
	testCases := []struct {
		model    string
		expected string
	}{
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		{"anthropic/claude-opus-4", "claude-opus-4"},
		{"qwen3.5-27b", "qwen3.5-27b"},
		{"gpt-4-turbo-2024-04-09", "gpt-4-turbo"},
		{"opus-4.6", "opus-4.6"},
		{"", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.model, func(t *testing.T) {
			result := comparableModelName(tc.model)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestSameModelIntent(t *testing.T) {
	testCases := []struct {
		requested string
		candidate string
		expected  bool
	}{
		{"claude-3-5-sonnet", "claude-3-5-sonnet-20241022", true},
		{"anthropic/claude-3-5-sonnet", "claude-3-5-sonnet", true},
		{"qwen3.5-27b", "qwen3.5-27b-instruct", true},
		{"claude-3-5-sonnet", "claude-opus-4", false},
		{"gpt-4", "claude-4", false},
	}

	for _, tc := range testCases {
		t.Run(tc.requested+"_"+tc.candidate, func(t *testing.T) {
			result := sameModelIntent(tc.requested, tc.candidate)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestBestModelMatch(t *testing.T) {
	testCases := []struct {
		name          string
		requested     string
		configured    string
		listed        []string
		expectedMatch string
	}{
		{
			name:          "exact match in listed",
			requested:     "qwen3.5-27b",
			configured:    "",
			listed:        []string{"qwen3.5-27b", "qwen3.5-7b"},
			expectedMatch: "qwen3.5-27b",
		},
		{
			name:          "configured takes precedence",
			requested:     "qwen3.5-27b",
			configured:    "qwen3.5-27b-instruct",
			listed:        []string{"qwen3.5-27b"},
			expectedMatch: "qwen3.5-27b-instruct",
		},
		{
			name:          "no match returns requested",
			requested:     "unknown-model",
			configured:    "fallback-model",
			listed:        []string{"qwen3.5-27b"},
			expectedMatch: "unknown-model",
		},
		{
			name:          "fallback to first listed",
			requested:     "",
			configured:    "",
			listed:        []string{"qwen3.5-27b", "qwen3.5-7b"},
			expectedMatch: "qwen3.5-27b",
		},
		{
			name:          "requested takes precedence over fallback",
			requested:     "qwen3.5-27b",
			configured:    "",
			listed:        []string{"qwen3.5-7b", "qwen3.5-27b"},
			expectedMatch: "qwen3.5-27b",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := bestModelMatch(tc.requested, tc.configured, tc.listed)
			assert.Equal(t, tc.expectedMatch, result)
		})
	}
}

func TestOrderedCandidates(t *testing.T) {
	plan := smartRoutePlan{
		Candidates: []smartRouteCandidate{
			{Provider: "local"},
			{Provider: "cloud"},
			{Provider: "fallback"},
		},
		Order: []int{0, 2}, // local, fallback (cloud not in order)
	}

	result := orderedCandidates(plan)
	assert.Len(t, result, 3)
	assert.Equal(t, "local", result[0].Provider)
	assert.Equal(t, "fallback", result[1].Provider)
	assert.Equal(t, "cloud", result[2].Provider) // cloud comes last as it wasn't in order
}

func TestOrderedCandidates_EmptyOrder(t *testing.T) {
	plan := smartRoutePlan{
		Candidates: []smartRouteCandidate{
			{Provider: "local"},
			{Provider: "cloud"},
		},
		Order: []int{},
	}

	result := orderedCandidates(plan)
	assert.Len(t, result, 2)
	assert.Equal(t, "local", result[0].Provider)
	assert.Equal(t, "cloud", result[1].Provider)
}

func TestTruncate(t *testing.T) {
	testCases := []struct {
		input    string
		n        int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly", 7, "exactly"},
		{"toolong", 4, "to.."},
		{"tiny", 2, "ti"},
		{"", 5, ""},
		{"toolong", 0, "toolong"},
		{"toolong", -1, "toolong"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := truncate(tc.input, tc.n)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestAbs(t *testing.T) {
	assert.Equal(t, 5.0, abs(5.0))
	assert.Equal(t, 5.0, abs(-5.0))
	assert.Equal(t, 0.0, abs(0.0))
	assert.InDelta(t, 0.001, 3.14, abs(-3.14))
}

func TestMinDuration(t *testing.T) {
	assert.Equal(t, 10*time.Second, minDuration(10*time.Second, 20*time.Second))
	assert.Equal(t, 10*time.Second, minDuration(20*time.Second, 10*time.Second))
	assert.Equal(t, 10*time.Second, minDuration(10*time.Second, 10*time.Second))
	assert.Equal(t, 20*time.Second, minDuration(0, 20*time.Second))
	assert.Equal(t, 15*time.Second, minDuration(15*time.Second, 0))
}

func TestProviderModelProbe_Available(t *testing.T) {
	probe := providerModelProbe{
		models: []string{"model-1", "model-2"},
		err:    nil,
	}
	assert.True(t, probe.available())

	probe.err = assert.AnError
	assert.False(t, probe.available())
}

func TestScoreSmartRouteCandidates_CapabilityBreaksTie(t *testing.T) {
	// Two candidates with same reliability and cost but different SWEBenchVerified;
	// the higher benchmark score candidate should win.
	plan := &smartRoutePlan{
		Candidates: []smartRouteCandidate{
			{
				Provider:         "provider-a",
				Model:            "model-a",
				Healthy:          true,
				Reliability:      0.9,
				SWEBenchVerified: 40.0, // lower capability
			},
			{
				Provider:         "provider-b",
				Model:            "model-b",
				Healthy:          true,
				Reliability:      0.9,
				SWEBenchVerified: 70.0, // higher capability
			},
		},
	}

	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			// Give capability significant weight to ensure it breaks the tie.
			ReliabilityWeight: 0.35,
			PerformanceWeight: 0.20,
			LoadWeight:        0.15,
			CostWeight:        0.20,
			CapabilityWeight:  0.10,
		},
	}

	order := scoreSmartRouteCandidates(plan, 0, cfg)
	assert.NotEmpty(t, order)
	// provider-b has higher SWEBenchVerified so it should be ranked first.
	assert.Equal(t, "provider-b", plan.Candidates[order[0]].Provider)
	assert.Greater(t, plan.Candidates[order[0]].CapabilityScore, plan.Candidates[order[1]].CapabilityScore)
}

func TestRoutingWeights_CapabilityDefault(t *testing.T) {
	cfg := &agentConfig.Config{}
	rel, perf, load, cost, cap := routingWeights(cfg)
	total := rel + perf + load + cost + cap
	assert.InDelta(t, 1.0, total, 0.001, "default weights must sum to 1.0")
	assert.Greater(t, cap, 0.0, "capability weight must be positive")
	assert.InDelta(t, 0.10, cap, 0.001, "default capability weight is ~0.10")
}
