package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolateCatalogHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func TestResolveProviderForRun_DefaultProvider(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "configured-model",
			},
		},
		Default: "local",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, "", "", "", agentConfig.ProviderOverrides{})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "local", selection.Route)
	assert.Equal(t, "local", selection.Provider)
	assert.Equal(t, "", selection.ResolvedModelRef)
	assert.Equal(t, "configured-model", selection.ResolvedModel)
	assert.Equal(t, "configured-model", pc.Model)
}

func TestResolvePreset(t *testing.T) {
	cfg := &agentConfig.Config{Preset: "codex"}

	assert.Equal(t, "benchmark", resolvePreset("benchmark", cfg))
	assert.Equal(t, "codex", resolvePreset("", cfg))
	assert.Equal(t, "agent", resolvePreset("", &agentConfig.Config{}))
}

func TestResolveRunReasoningNormalizesExplicitValues(t *testing.T) {
	cfg := &agentConfig.Config{}
	got, err := resolveRunReasoning(cfg, providerSelection{ReasoningDefault: agent.ReasoningHigh}, "x-high")
	require.NoError(t, err)
	assert.Equal(t, agent.ReasoningXHigh, got)

	got, err = resolveRunReasoning(cfg, providerSelection{ReasoningDefault: agent.ReasoningHigh}, "auto")
	require.NoError(t, err)
	assert.Equal(t, agent.ReasoningHigh, got)
}

func TestBuildToolsForPreset_BenchmarkExcludesTaskTool(t *testing.T) {
	tools := buildToolsForPreset(t.TempDir(), "benchmark")

	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name())
	}

	assert.NotContains(t, names, "task")
	assert.Contains(t, names, "patch")
	assert.Contains(t, names, "glob")
	assert.Contains(t, names, "grep")
	assert.Contains(t, names, "ls")
}

func TestBuildToolsForPreset_DefaultIncludesTaskTool(t *testing.T) {
	tools := buildToolsForPreset(t.TempDir(), "agent")

	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name())
	}

	assert.Contains(t, names, "task")
}

func TestResolveProviderForRun_ModelRef(t *testing.T) {
	isolateCatalogHome(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
		Default: "cloud",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "code-high", selection.Route)
	assert.Equal(t, "cloud", selection.Provider)
	assert.Equal(t, "code-high", selection.ResolvedModelRef)
	assert.Equal(t, "opus-4.6", selection.ResolvedModel)
	assert.Equal(t, "opus-4.6", pc.Model)
}

func TestResolveProviderForRun_DeprecatedModelRefRejectedByDefault(t *testing.T) {
	isolateCatalogHome(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
		Default: "cloud",
	}

	_, _, _, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{
		ModelRef: "claude-sonnet-3.7",
	})
	require.Error(t, err)
}

func TestResolveProviderForRun_DeprecatedModelRefAllowed(t *testing.T) {
	isolateCatalogHome(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
		Default: "cloud",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{
		ModelRef:        "claude-sonnet-3.7",
		AllowDeprecated: true,
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "claude-sonnet-3.7", selection.Route)
	assert.Equal(t, "cloud", selection.Provider)
	assert.Equal(t, "claude-sonnet-3.7", selection.ResolvedModelRef)
	assert.Equal(t, "claude-3-7-sonnet-20250219", selection.ResolvedModel)
	assert.Equal(t, "claude-3-7-sonnet-20250219", pc.Model)
}

func TestResolveProviderForRun_ModelIntentWithoutRouteUsesSmartSelection(t *testing.T) {
	isolateCatalogHome(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
				Model:  "configured-model",
			},
		},
		Default: "cloud",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{
		Model:    "exact-model",
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "exact-model", selection.Route)
	assert.Equal(t, "cloud", selection.Provider)
	assert.Equal(t, "", selection.ResolvedModelRef)
	assert.Equal(t, "exact-model", selection.ResolvedModel)
	assert.Equal(t, "exact-model", pc.Model)
}

func TestResolveProviderForRun_ExplicitProviderStillUsesExactModelPin(t *testing.T) {
	isolateCatalogHome(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
				Model:  "configured-model",
			},
		},
		Default: "cloud",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, workDir, "", "cloud", agentConfig.ProviderOverrides{
		Model:    "exact-model",
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "cloud", selection.Route)
	assert.Equal(t, "cloud", selection.Provider)
	assert.Equal(t, "", selection.ResolvedModelRef)
	assert.Equal(t, "exact-model", selection.ResolvedModel)
	assert.Equal(t, "exact-model", pc.Model)
}

func TestResolveProviderForRun_ModelRouteByExplicitModel(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"bragi": {
				Type:    "openai-compat",
				BaseURL: "http://bragi:1234/v1",
				Model:   "provider-default",
			},
		},
		ModelRoutes: map[string]agentConfig.ModelRouteConfig{
			"qwen3.5-27b": {
				Strategy: "ordered-failover",
				Candidates: []agentConfig.ModelRouteCandidateConfig{
					{Provider: "bragi", Model: "qwen3.5-27b"},
				},
			},
		},
		Default: "bragi",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{
		Model: "qwen3.5-27b",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "qwen3.5-27b", selection.Route)
	assert.Equal(t, "bragi", selection.Provider)
	assert.Equal(t, "qwen3.5-27b", selection.ResolvedModel)
	assert.Equal(t, "qwen3.5-27b", pc.Model)
}

func TestResolveProviderForRun_DefaultModelRouteOverridesDefaultProvider(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"vidar": {
				Type:    "openai-compat",
				BaseURL: "http://vidar:1234/v1",
				Model:   "provider-default",
			},
			"openrouter": {
				Type:    "openai-compat",
				BaseURL: "https://openrouter.ai/api/v1",
				Model:   "provider-fallback",
			},
		},
		Routing: agentConfig.RoutingConfig{
			DefaultModel: "qwen3.5-27b",
		},
		ModelRoutes: map[string]agentConfig.ModelRouteConfig{
			"qwen3.5-27b": {
				Strategy: "ordered-failover",
				Candidates: []agentConfig.ModelRouteCandidateConfig{
					{Provider: "openrouter", Model: "qwen/qwen3.5-27b"},
				},
			},
		},
		Default: "vidar",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "qwen3.5-27b", selection.Route)
	assert.Equal(t, "openrouter", selection.Provider)
	assert.Equal(t, "qwen/qwen3.5-27b", selection.ResolvedModel)
	assert.Equal(t, "qwen/qwen3.5-27b", pc.Model)
}

func TestResolveProviderForRun_ModelRefRouteUsesCanonicalTarget(t *testing.T) {
	isolateCatalogHome(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:    "openai-compat",
				BaseURL: "https://openrouter.ai/api/v1",
			},
		},
		ModelRoutes: map[string]agentConfig.ModelRouteConfig{
			"code-medium": {
				Strategy: "ordered-failover",
				Candidates: []agentConfig.ModelRouteCandidateConfig{
					{Provider: "cloud", Model: "gpt-5.4-mini"},
				},
			},
		},
		Default: "cloud",
	}

	selection, p, pc, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{
		ModelRef: "code-fast",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "code-medium", selection.Route)
	assert.Equal(t, "code-fast", selection.RequestedModelRef)
	assert.Equal(t, "code-medium", selection.ResolvedModelRef)
	assert.Equal(t, "gpt-5.4-mini", selection.ResolvedModel)
	assert.Equal(t, "gpt-5.4-mini", pc.Model)
}

func TestResolveProviderForRun_BackendRoundRobinSelectionAttribution(t *testing.T) {
	isolateCatalogHome(t)
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".agent"), 0o755))
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"vidar": {
				Type:    "openai-compat",
				BaseURL: "http://vidar:1234/v1",
			},
			"bragi": {
				Type:    "openai-compat",
				BaseURL: "http://bragi:1234/v1",
			},
		},
		Backends: map[string]agentConfig.BackendPoolConfig{
			"code-pool": {
				ModelRef:  "code-fast",
				Providers: []string{"vidar", "bragi"},
				Strategy:  "round-robin",
			},
		},
		DefaultBackend: "code-pool",
	}

	firstSelection, firstProvider, firstConfig, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{})
	require.NoError(t, err)
	assert.NotNil(t, firstProvider)
	assert.Equal(t, "code-pool", firstSelection.Route)
	assert.Equal(t, "vidar", firstSelection.Provider)
	assert.Equal(t, "code-medium", firstSelection.ResolvedModelRef)
	assert.Equal(t, "gpt-5.4-mini", firstSelection.ResolvedModel)
	assert.Equal(t, "gpt-5.4-mini", firstConfig.Model)

	secondSelection, secondProvider, secondConfig, err := resolveProviderForRun(cfg, workDir, "", "", agentConfig.ProviderOverrides{})
	require.NoError(t, err)
	assert.NotNil(t, secondProvider)
	assert.Equal(t, "code-pool", secondSelection.Route)
	assert.Equal(t, "bragi", secondSelection.Provider)
	assert.Equal(t, "code-medium", secondSelection.ResolvedModelRef)
	assert.Equal(t, "gpt-5.4-mini", secondSelection.ResolvedModel)
	assert.Equal(t, "gpt-5.4-mini", secondConfig.Model)
}
