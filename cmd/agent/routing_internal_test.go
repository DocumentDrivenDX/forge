package main

import (
	"testing"
	"time"

	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestResolveRouteTarget_ExplicitProvider(t *testing.T) {
	cfg := &agentConfig.Config{}
	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "", "cloud", agentConfig.ProviderOverrides{})
	assert.NoError(t, err)
	assert.Equal(t, "", routeKey)
	assert.Equal(t, "", routeModelRef)
	assert.Equal(t, "", legacyBackend)
}

func TestResolveRouteTarget_ExplicitBackend(t *testing.T) {
	cfg := &agentConfig.Config{}
	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "my-backend", "", agentConfig.ProviderOverrides{})
	assert.NoError(t, err)
	assert.Equal(t, "", routeKey)
	assert.Equal(t, "", routeModelRef)
	assert.Equal(t, "my-backend", legacyBackend)
}

func TestResolveRouteTarget_ExplicitModel(t *testing.T) {
	cfg := &agentConfig.Config{}
	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "", "", agentConfig.ProviderOverrides{Model: "qwen3.5-27b"})
	assert.NoError(t, err)
	assert.Equal(t, "qwen3.5-27b", routeKey)
	assert.Equal(t, "", routeModelRef)
	assert.Equal(t, "", legacyBackend)
}

func TestResolveRouteTarget_DefaultBackendFallback(t *testing.T) {
	cfg := &agentConfig.Config{
		DefaultBackend: "fallback-pool",
	}
	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "", "", agentConfig.ProviderOverrides{})
	assert.NoError(t, err)
	assert.Equal(t, "", routeKey)
	assert.Equal(t, "", routeModelRef)
	assert.Equal(t, "fallback-pool", legacyBackend)
}

func TestResolveRouteTarget_RoutingDefaultModel(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			DefaultModel: "qwen3.5-27b",
		},
	}
	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "", "", agentConfig.ProviderOverrides{})
	assert.NoError(t, err)
	assert.Equal(t, "qwen3.5-27b", routeKey)
	assert.Equal(t, "", routeModelRef)
	assert.Equal(t, "", legacyBackend)
}

func TestResolveRouteTarget_ProviderTakesPrecedenceOverModel(t *testing.T) {
	cfg := &agentConfig.Config{}
	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "", "cloud", agentConfig.ProviderOverrides{Model: "qwen3.5-27b"})
	assert.NoError(t, err)
	assert.Equal(t, "", routeKey)
	assert.Equal(t, "", routeModelRef)
	assert.Equal(t, "", legacyBackend)
}

func TestResolveRouteTarget_BackendTakesPrecedenceOverModel(t *testing.T) {
	cfg := &agentConfig.Config{}
	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "my-backend", "", agentConfig.ProviderOverrides{Model: "qwen3.5-27b"})
	assert.NoError(t, err)
	assert.Equal(t, "", routeKey)
	assert.Equal(t, "", routeModelRef)
	assert.Equal(t, "my-backend", legacyBackend)
}

func TestResolveRouteTarget_PrecedenceOrder(t *testing.T) {
	cfg := &agentConfig.Config{
		DefaultBackend: "fallback-pool",
		Routing: agentConfig.RoutingConfig{
			DefaultModel: "default-route",
		},
	}

	// Provider wins
	routeKey, _, _, _ := resolveRouteTarget(cfg, "", "cloud", agentConfig.ProviderOverrides{})
	assert.Equal(t, "", routeKey)

	// Backend wins over model
	routeKey, _, _, _ = resolveRouteTarget(cfg, "my-backend", "", agentConfig.ProviderOverrides{Model: "qwen3.5-27b"})
	assert.Equal(t, "", routeKey)

	// Model wins over routing defaults
	routeKey, _, _, _ = resolveRouteTarget(cfg, "", "", agentConfig.ProviderOverrides{Model: "qwen3.5-27b"})
	assert.Equal(t, "qwen3.5-27b", routeKey)

	// Routing default model wins over backend fallback
	routeKey, _, _, _ = resolveRouteTarget(cfg, "", "", agentConfig.ProviderOverrides{})
	assert.Equal(t, "default-route", routeKey)
}

func TestShouldFailover_TransportErrors(t *testing.T) {
	testCases := []struct {
		err      error
		expectOk bool
	}{
		{err: assert.AnError, expectOk: false},
	}

	for _, tc := range testCases {
		result := shouldFailover(tc.err)
		assert.Equal(t, tc.expectOk, result)
	}
}

func TestShouldFailover_StatusCodes(t *testing.T) {
	testCases := []struct {
		errMsg   string
		failover bool
	}{
		{"401 unauthorized", true},
		{"403 forbidden", true},
		{"status code: 429", true},
		{"500 internal server error", true},
		{"502 bad gateway", true},
		{"connection refused", true},
		{"dial tcp: connection refused", true},
		{"no such host", true},
		{"timeout exceeded", true},
	}

	for _, tc := range testCases {
		t.Run(tc.errMsg, func(t *testing.T) {
			err := &testError{msg: tc.errMsg}
			result := shouldFailover(err)
			assert.Equal(t, tc.failover, result)
		})
	}
}

func TestShouldFailover_NoFailover(t *testing.T) {
	testCases := []string{
		"invalid request: missing required field",
		"tool schema error",
		"rate limit exceeded: user rate limit",
		"context canceled",
	}

	for _, msg := range testCases {
		t.Run(msg, func(t *testing.T) {
			err := &testError{msg: msg}
			result := shouldFailover(err)
			assert.False(t, result)
		})
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestHealthyCandidateIndexes_AllHealthy(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1"},
		{Provider: "local-2"},
		{Provider: "cloud"},
	}
	state := routeHealthState{Failures: map[string]time.Time{}}
	cooldown := 30 * time.Second

	indexes := healthyCandidateIndexes(candidates, state, cooldown)
	assert.Equal(t, []int{0, 1, 2}, indexes)
}

func TestHealthyCandidateIndexes_SomeUnhealthy(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1"},
		{Provider: "local-2"},
		{Provider: "cloud"},
	}
	state := routeHealthState{
		Failures: map[string]time.Time{
			"local-1": time.Now().Add(-15 * time.Second), // within cooldown
			"cloud":   time.Now().Add(-60 * time.Second), // outside cooldown
		},
	}
	cooldown := 30 * time.Second

	indexes := healthyCandidateIndexes(candidates, state, cooldown)
	assert.Equal(t, []int{1, 2}, indexes)
}

func TestHealthyCandidateIndexes_AllUnhealthy(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1"},
		{Provider: "local-2"},
	}
	state := routeHealthState{
		Failures: map[string]time.Time{
			"local-1": time.Now().Add(-10 * time.Second),
			"local-2": time.Now().Add(-20 * time.Second),
		},
	}
	cooldown := 30 * time.Second

	indexes := healthyCandidateIndexes(candidates, state, cooldown)
	assert.Empty(t, indexes)
}

func TestPriorityRoundRobinOrder_BasicRotation(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1", Priority: 100},
		{Provider: "local-2", Priority: 100},
		{Provider: "cloud", Priority: 50},
	}
	state := routeHealthState{}
	cooldown := 0 * time.Second

	// First call - local-1 first
	order := priorityRoundRobinOrder(candidates, 0, state, cooldown)
	assert.Equal(t, 0, order[0]) // local-1 first

	// Second call - local-2 first (rotation)
	order = priorityRoundRobinOrder(candidates, 1, state, cooldown)
	assert.Equal(t, 1, order[0]) // local-2 first

	// Third call - local-1 first again
	order = priorityRoundRobinOrder(candidates, 2, state, cooldown)
	assert.Equal(t, 0, order[0]) // local-1 first

	// High priority candidates come before low priority
	assert.Equal(t, 0, order[0]) // local-1 (100)
	assert.Equal(t, 1, order[1]) // local-2 (100)
	assert.Equal(t, 2, order[2]) // cloud (50)
}

func TestPriorityRoundRobinOrder_FiltersUnhealthy(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1", Priority: 100},
		{Provider: "local-2", Priority: 100},
		{Provider: "cloud", Priority: 50},
	}
	state := routeHealthState{
		Failures: map[string]time.Time{
			"local-1": time.Now().Add(-10 * time.Second), // within cooldown
		},
	}
	cooldown := 30 * time.Second

	order := priorityRoundRobinOrder(candidates, 0, state, cooldown)
	// local-1 filtered out, only local-2 and cloud remain
	assert.Equal(t, []int{1, 2}, order)
}

func TestPriorityRoundRobinOrder_AllUnhealthyFallsBack(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1", Priority: 100},
		{Provider: "local-2", Priority: 100},
	}
	state := routeHealthState{
		Failures: map[string]time.Time{
			"local-1": time.Now().Add(-10 * time.Second),
			"local-2": time.Now().Add(-10 * time.Second),
		},
	}
	cooldown := 30 * time.Second

	order := priorityRoundRobinOrder(candidates, 0, state, cooldown)
	// All unhealthy - should return all in original order
	assert.Equal(t, []int{0, 1}, order)
}

func TestOrderedFailoverOrder_AllHealthy(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1"},
		{Provider: "local-2"},
		{Provider: "cloud"},
	}
	state := routeHealthState{}
	cooldown := 30 * time.Second

	order := orderedFailoverOrder(candidates, state, cooldown)
	assert.Equal(t, []int{0, 1, 2}, order)
}

func TestOrderedFailoverOrder_SkipsUnhealthy(t *testing.T) {
	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "local-1"},
		{Provider: "local-2"},
		{Provider: "cloud"},
	}
	state := routeHealthState{
		Failures: map[string]time.Time{
			"local-1": time.Now().Add(-10 * time.Second), // within cooldown
		},
	}
	cooldown := 30 * time.Second

	order := orderedFailoverOrder(candidates, state, cooldown)
	assert.Equal(t, []int{1, 2}, order)
}

func TestRouteStateKey(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"qwen3.5-27b", "qwen3.5-27b"},
		{"anthropic/claude-opus-4", "anthropic_claude-opus-4"},
		{"openrouter:code-high", "openrouter_code-high"},
		{"model with spaces", "model_with_spaces"},
	}

	for _, tc := range testCases {
		result := routeStateKey(tc.input)
		assert.Equal(t, tc.expected, result)
	}
}

func TestRouteHealthCooldown_Config(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			HealthCooldown: "60s",
		},
	}

	cooldown := routeHealthCooldown(cfg)
	assert.Equal(t, 60*time.Second, cooldown)
}

func TestRouteHealthCooldown_Default(t *testing.T) {
	cfg := &agentConfig.Config{}
	cooldown := routeHealthCooldown(cfg)
	assert.Equal(t, defaultRouteHealthCooldown, cooldown)
}

func TestRouteHealthCooldown_Invalid(t *testing.T) {
	cfg := &agentConfig.Config{
		Routing: agentConfig.RoutingConfig{
			HealthCooldown: "invalid",
		},
	}

	cooldown := routeHealthCooldown(cfg)
	assert.Equal(t, defaultRouteHealthCooldown, cooldown)
}

func TestMax(t *testing.T) {
	assert.Equal(t, 5, max(3, 5))
	assert.Equal(t, 5, max(5, 3))
	assert.Equal(t, 5, max(5, 5))
	assert.Equal(t, 0, max(0, 0))
}
