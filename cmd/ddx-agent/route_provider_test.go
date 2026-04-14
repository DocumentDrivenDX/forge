package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentConfig "github.com/DocumentDrivenDX/agent/config"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
	"github.com/stretchr/testify/assert"
)

func TestRouteProvider_BuildCandidate(t *testing.T) {
	workDir := t.TempDir()
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "default-model",
			},
		},
	}

	rp := &routeProvider{
		cfg:               cfg,
		workDir:           workDir,
		routeKey:          "test-route",
		requestedModel:    "qwen3.5-27b",
		requestedModelRef: "",
		allowDeprecated:   false,
	}

	candidate := agentConfig.ModelRouteCandidateConfig{
		Provider: "local",
		Model:    "qwen3.5-27b",
	}

	provider, pc, err := rp.buildCandidate(candidate)
	assert.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, "qwen3.5-27b", pc.Model)
}

func TestRouteProvider_BuildCandidate_EmptyModel(t *testing.T) {
	workDir := t.TempDir()
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "default-model",
			},
		},
	}

	rp := &routeProvider{
		cfg:               cfg,
		workDir:           workDir,
		routeKey:          "test-route",
		requestedModel:    "default-model",
		requestedModelRef: "",
		allowDeprecated:   false,
	}

	candidate := agentConfig.ModelRouteCandidateConfig{
		Provider: "local",
		Model:    "", // Empty model uses provider default
	}

	provider, pc, err := rp.buildCandidate(candidate)
	assert.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, "default-model", pc.Model)
}

func TestRouteProvider_BuildCandidate_UnknownProvider(t *testing.T) {
	workDir := t.TempDir()
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
			},
		},
	}

	rp := &routeProvider{
		cfg:             cfg,
		workDir:         workDir,
		routeKey:        "test-route",
		allowDeprecated: false,
	}

	candidate := agentConfig.ModelRouteCandidateConfig{
		Provider: "unknown",
		Model:    "some-model",
	}

	_, _, err := rp.buildCandidate(candidate)
	assert.Error(t, err)
}

func TestRouteHealthState_SaveAndLoad(t *testing.T) {
	workDir := t.TempDir()
	routeKey := "test-route"

	state := routeHealthState{
		Failures: make(map[string]time.Time),
	}
	state.Failures["provider-1"] = time.Now().Add(-10 * time.Second)
	state.Failures["provider-2"] = time.Now().Add(-60 * time.Second)

	err := saveRouteHealthState(workDir, routeKey, state)
	assert.NoError(t, err)

	loaded, err := loadRouteHealthState(workDir, routeKey)
	assert.NoError(t, err)
	assert.Len(t, loaded.Failures, 2)
}

func TestLoadRouteHealthState_NotExists(t *testing.T) {
	workDir := t.TempDir()

	state, err := loadRouteHealthState(workDir, "nonexistent-route")
	assert.NoError(t, err)
	assert.NotNil(t, state.Failures)
	assert.Empty(t, state.Failures)
}

func TestLoadRouteHealthState_InvalidJSON(t *testing.T) {
	workDir := t.TempDir()
	routeKey := "invalid-route"

	// Write invalid JSON
	invalidPath := filepath.Join(workDir, ".agent", "route-health-invalid-route.json")
	err := safefs.MkdirAll(filepath.Dir(invalidPath), 0o750)
	assert.NoError(t, err)
	err = safefs.WriteFile(invalidPath, []byte("not valid json"), 0o600)
	assert.NoError(t, err)

	state, err := loadRouteHealthState(workDir, routeKey)
	assert.NoError(t, err)
	assert.NotNil(t, state.Failures)
	assert.Empty(t, state.Failures)
}

func TestSaveRouteHealthState_CreatesDir(t *testing.T) {
	workDir := t.TempDir()
	routeKey := "new-route"

	state := routeHealthState{
		Failures: make(map[string]time.Time),
	}
	state.Failures["provider-1"] = time.Now()

	err := saveRouteHealthState(workDir, routeKey, state)
	assert.NoError(t, err)

	// Verify file exists
	path := routeHealthStateFile(workDir, routeKey)
	_, err = os.Stat(path)
	assert.NoError(t, err)
}

func TestRouteProvider_RecordAttempt(t *testing.T) {
	workDir := t.TempDir()
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
			},
		},
	}

	rp := &routeProvider{
		cfg:              cfg,
		workDir:          workDir,
		routeKey:         "test-route",
		attempted:        []string{},
		failoverCount:    0,
		selectedProvider: "",
	}

	// Record successful attempt
	rp.recordAttempt("local", []string{"local"}, 0, true)

	assert.Equal(t, "local", rp.selectedProvider)
	assert.Equal(t, 0, rp.failoverCount)
}

func TestRouteProvider_RecordAttempt_Failure(t *testing.T) {
	workDir := t.TempDir()
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
			},
		},
	}

	rp := &routeProvider{
		cfg:              cfg,
		workDir:          workDir,
		routeKey:         "test-route",
		attempted:        []string{},
		failoverCount:    0,
		selectedProvider: "",
	}

	// Record failed attempt
	rp.recordAttempt("", []string{"local"}, 1, false)

	assert.Equal(t, "", rp.selectedProvider)
	assert.Equal(t, 1, rp.failoverCount)
}

func TestRouteProvider_RoutingReport(t *testing.T) {
	cfg := &agentConfig.Config{}
	rp := &routeProvider{
		cfg:              cfg,
		routeKey:         "test-route",
		selectedProvider: "cloud",
		attempted:        []string{"local", "cloud"},
		failoverCount:    1,
	}

	report := rp.RoutingReport()

	assert.Equal(t, "cloud", report.SelectedProvider)
	assert.Equal(t, "test-route", report.SelectedRoute)
	assert.Equal(t, []string{"local", "cloud"}, report.AttemptedProviders)
	assert.Equal(t, 1, report.FailoverCount)
}

func TestRouteProvider_SessionStartMetadata(t *testing.T) {
	cfg := &agentConfig.Config{}
	rp := &routeProvider{
		cfg:              cfg,
		routeKey:         "test-route",
		selectedProvider: "local",
		candidates: []agentConfig.ModelRouteCandidateConfig{
			{Provider: "local", Model: "qwen3.5-27b"},
			{Provider: "cloud", Model: "claude-3-5-sonnet"},
		},
		order: []int{0, 1},
	}

	provider, model := rp.SessionStartMetadata()
	assert.Equal(t, "local", provider)
	assert.Equal(t, "qwen3.5-27b", model)
}

func TestRouteError(t *testing.T) {
	err := &routeError{
		Route:    "test-route",
		Attempts: []string{"provider-1: connection refused", "provider-2: 500 error"},
	}

	expected := "agent: route \"test-route\" failed after attempts: provider-1: connection refused | provider-2: 500 error"
	assert.Equal(t, expected, err.Error())
}

func TestRouteProvider_InitialProvider(t *testing.T) {
	cfg := &agentConfig.Config{}
	rp := &routeProvider{
		cfg:               cfg,
		routeKey:          "test-route",
		selectedProvider:  "cloud",
		requestedModel:    "qwen3.5-27b",
		requestedModelRef: "",
		candidates: []agentConfig.ModelRouteCandidateConfig{
			{Provider: "local", Model: "qwen3.5-27b"},
			{Provider: "cloud", Model: "claude-3-5-sonnet"},
		},
		order: []int{0, 1},
	}

	assert.Equal(t, "cloud", rp.selectedProvider)
	assert.Equal(t, "qwen3.5-27b", rp.requestedModel)
}

func TestRouteHealthStateFile(t *testing.T) {
	workDir := "/home/user/project"
	routeKey := "qwen3.5-27b"

	path := routeHealthStateFile(workDir, routeKey)
	assert.Contains(t, path, ".agent")
	assert.Contains(t, path, "route-health")
	assert.Contains(t, path, "qwen3.5-27b")
}

func TestRouteHealthState_JSON(t *testing.T) {
	state := routeHealthState{
		Failures: make(map[string]time.Time),
	}
	state.Failures["provider-1"] = time.Now().Add(-10 * time.Second)

	data, err := json.Marshal(state)
	assert.NoError(t, err)

	var loaded routeHealthState
	err = json.Unmarshal(data, &loaded)
	assert.NoError(t, err)
	assert.Len(t, loaded.Failures, 1)
}

func TestNewRouteProvider(t *testing.T) {
	cfg := &agentConfig.Config{}
	route := agentConfig.ModelRouteConfig{
		Strategy: "ordered-failover",
		Candidates: []agentConfig.ModelRouteCandidateConfig{
			{Provider: "local", Model: "qwen3.5-27b"},
			{Provider: "cloud", Model: "claude-3-5-sonnet"},
		},
	}

	rp := newRouteProvider(cfg, "/work/dir", "qwen3.5-27b", "qwen3.5-27b", "code-fast", route, []int{0, 1}, "local", true)

	assert.Equal(t, cfg, rp.cfg)
	assert.Equal(t, "/work/dir", rp.workDir)
	assert.Equal(t, "qwen3.5-27b", rp.routeKey)
	assert.Equal(t, "qwen3.5-27b", rp.requestedModel)
	assert.Equal(t, "code-fast", rp.requestedModelRef)
	assert.True(t, rp.allowDeprecated)
	assert.Len(t, rp.candidates, 2)
	assert.Equal(t, []int{0, 1}, rp.order)
	assert.Equal(t, "local", rp.selectedProvider)
}

func TestNewRouteProvider_CopiesData(t *testing.T) {
	cfg := &agentConfig.Config{}
	route := agentConfig.ModelRouteConfig{
		Candidates: []agentConfig.ModelRouteCandidateConfig{
			{Provider: "local", Model: "model-1"},
		},
	}

	rp := newRouteProvider(cfg, "", "route", "model", "", route, []int{0}, "local", false)

	// Modify the original route
	route.Candidates[0].Provider = "modified"

	// rp should not be affected
	assert.Equal(t, "local", rp.candidates[0].Provider)
	assert.Equal(t, 0, rp.order[0])
}

// TestRouteHealthState_CooldownEnforcement verifies the full save/load/enforce
// cycle: a provider recorded as failed within cooldown is excluded; one whose
// failure timestamp is older than the cooldown window is re-admitted.
func TestRouteHealthState_CooldownEnforcement(t *testing.T) {
	workDir := t.TempDir()
	routeKey := "cooldown-test-route"
	cooldown := 30 * time.Second

	candidates := []agentConfig.ModelRouteCandidateConfig{
		{Provider: "in-cooldown"},
		{Provider: "expired-cooldown"},
		{Provider: "healthy"},
	}

	// Persist health state: in-cooldown failed 10s ago, expired-cooldown 60s ago.
	state := routeHealthState{
		Failures: map[string]time.Time{
			"in-cooldown":      time.Now().Add(-10 * time.Second),
			"expired-cooldown": time.Now().Add(-60 * time.Second),
		},
	}
	err := saveRouteHealthState(workDir, routeKey, state)
	assert.NoError(t, err)

	// Load it back and compute eligible candidates, simulating what
	// buildSmartRoutePlan / routeAttemptOrder do on the next invocation.
	loaded, err := loadRouteHealthState(workDir, routeKey)
	assert.NoError(t, err)

	eligible := healthyCandidateIndexes(candidates, loaded, cooldown)

	// "in-cooldown" must be excluded; "expired-cooldown" and "healthy" must be included.
	assert.NotContains(t, eligible, 0, "in-cooldown provider should be excluded during cooldown window")
	assert.Contains(t, eligible, 1, "expired-cooldown provider should be re-admitted after cooldown expires")
	assert.Contains(t, eligible, 2, "healthy provider should always be eligible")
}

// TestRouteHealthState_AtomicWrite verifies that saveRouteHealthState produces
// a valid, loadable file (confirming atomic rename path completes without error).
func TestRouteHealthState_AtomicWrite(t *testing.T) {
	workDir := t.TempDir()
	routeKey := "atomic-write-route"

	state := routeHealthState{
		Failures: map[string]time.Time{
			"provider-a": time.Now().Add(-5 * time.Second),
		},
	}

	err := saveRouteHealthState(workDir, routeKey, state)
	assert.NoError(t, err)

	loaded, err := loadRouteHealthState(workDir, routeKey)
	assert.NoError(t, err)
	assert.Len(t, loaded.Failures, 1)
	_, ok := loaded.Failures["provider-a"]
	assert.True(t, ok, "provider-a failure timestamp must survive save/load roundtrip")
}
