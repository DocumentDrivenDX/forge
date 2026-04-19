package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func TestLoad_NewFormat(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
model_catalog:
  manifest: /tmp/models.yaml
telemetry:
  enabled: true
  pricing:
    openai:
      gpt-4o:
        amount: 0.0125
        currency: USD
        pricing_ref: openai/gpt-4o
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
    model: qwen3.5-7b
  cloud:
    type: anthropic
    api_key: sk-test
    model: claude-sonnet-4-20250514
default: local
max_iterations: 30
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Len(t, cfg.Providers, 2)
	assert.Equal(t, "local", cfg.Default)
	assert.Equal(t, 30, cfg.MaxIterations)
	assert.Equal(t, "/tmp/models.yaml", cfg.ModelCatalog.Manifest)
	assert.True(t, cfg.Telemetry.Enabled)

	cost, ok := cfg.BuildTelemetry().ResolveCost("openai", "gpt-4o")
	require.True(t, ok)
	require.NotNil(t, cost.Amount)
	assert.Equal(t, "configured", cost.Source)
	assert.Equal(t, "USD", cost.Currency)
	assert.Equal(t, "openai/gpt-4o", cost.PricingRef)
	assert.Equal(t, 0.0125, *cost.Amount)

	local, ok := cfg.GetProvider("local")
	require.True(t, ok)
	assert.Equal(t, "openai-compat", local.Type)
	assert.Equal(t, "qwen3.5-7b", local.Model)

	cloud, ok := cfg.GetProvider("cloud")
	require.True(t, ok)
	assert.Equal(t, "anthropic", cloud.Type)
	assert.Equal(t, "sk-test", cloud.APIKey)
}

func TestLoad_LegacyMigration(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
provider: openai-compat
base_url: http://vidar:1234/v1
model: qwen3.5-7b
max_iterations: 15
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, "default", cfg.DefaultName())
	assert.Equal(t, 15, cfg.MaxIterations)

	p, ok := cfg.GetProvider("default")
	require.True(t, ok)
	assert.Equal(t, "openai-compat", p.Type)
	assert.Equal(t, "http://vidar:1234/v1", p.BaseURL)
	assert.Equal(t, "qwen3.5-7b", p.Model)
}

func TestLoad_EnvExpansion(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	t.Setenv("TEST_AGENT_KEY", "secret-key-123")

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  test:
    type: anthropic
    api_key: ${TEST_AGENT_KEY}
    model: claude-sonnet-4-20250514
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	p, ok := cfg.GetProvider("test")
	require.True(t, ok)
	assert.Equal(t, "secret-key-123", p.APIKey)
}

func TestLoad_EnvExpansion_Unset(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  test:
    type: anthropic
    api_key: ${UNSET_VAR_THAT_DOES_NOT_EXIST}
    model: test
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	p, _ := cfg.GetProvider("test")
	assert.Equal(t, "${UNSET_VAR_THAT_DOES_NOT_EXIST}", p.APIKey)
}

func TestLoad_MissingFile(t *testing.T) {
	isolateHome(t)
	cfg, err := Load(t.TempDir())
	require.NoError(t, err)

	assert.Equal(t, 20, cfg.MaxIterations)
	assert.Equal(t, ".agent/sessions", cfg.SessionLogDir)
}

func TestLoadModelCatalog_UsesDefaultInstalledManifestPath(t *testing.T) {
	isolateHome(t)
	configDir, err := os.UserConfigDir()
	require.NoError(t, err)
	manifestPath := filepath.Join(configDir, "agent", "models.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(manifestPath), 0o755))
	require.NoError(t, os.WriteFile(manifestPath, []byte(`
version: 2
generated_at: 2026-04-10T00:00:00Z
catalog_version: 2026-04-11.1
profiles:
  code-high:
    target: code-high
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
    surface_policy:
      agent.openai:
        reasoning_default: high
`), 0o644))

	cfg := Defaults()
	catalog, err := cfg.LoadModelCatalog()
	require.NoError(t, err)
	resolved, err := catalog.Resolve("code-high", modelcatalog.ResolveOptions{
		Surface: modelcatalog.SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.4", resolved.ConcreteModel)
	assert.Equal(t, manifestPath, resolved.ManifestSource)
	assert.Equal(t, "2026-04-11.1", resolved.CatalogVersion)
}

func TestLoad_EnvOverrides(t *testing.T) {
	isolateHome(t)
	t.Setenv("AGENT_PROVIDER", "anthropic")
	t.Setenv("AGENT_API_KEY", "env-key")
	t.Setenv("AGENT_MODEL", "env-model")

	cfg, err := Load(t.TempDir())
	require.NoError(t, err)

	p, ok := cfg.GetProvider(cfg.DefaultName())
	require.True(t, ok)
	assert.Equal(t, "anthropic", p.Type)
	assert.Equal(t, "env-key", p.APIKey)
	assert.Equal(t, "env-model", p.Model)
}

func TestProviderNames_DefaultFirst(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"zebra": {Type: "openai-compat"},
			"alpha": {Type: "anthropic"},
			"local": {Type: "openai-compat"},
		},
		Default: "local",
	}

	names := cfg.ProviderNames()
	require.Len(t, names, 3)
	assert.Equal(t, "local", names[0])
}

func TestProviderNames_MissingDefaultOmitsUnknownDefaultAndSortsExisting(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"zebra": {Type: "openai-compat"},
			"alpha": {Type: "anthropic"},
		},
		Default: "missing",
	}

	names := cfg.ProviderNames()
	assert.Equal(t, []string{"alpha", "zebra"}, names)
}

func TestBuildProvider_MissingConfiguredDefaultFails(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"alpha": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "test-model",
			},
		},
		Default: "missing",
	}

	_, err := cfg.DefaultProvider()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider "missing"`)
}

func TestBuildProvider(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"test": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "test-model",
			},
		},
	}

	p, err := cfg.BuildProvider("test")
	require.NoError(t, err)
	assert.NotNil(t, p)

	_, err = cfg.BuildProvider("nonexistent")
	require.Error(t, err)
}

func TestBuildProvider_WithHeaders(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"openrouter": {
				Type:    "openai-compat",
				BaseURL: "https://openrouter.ai/api/v1",
				APIKey:  "test",
				Model:   "test",
				Headers: map[string]string{
					"HTTP-Referer": "https://example.com",
					"X-Title":      "DDX Agent",
				},
			},
		},
	}

	p, err := cfg.BuildProvider("openrouter")
	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestResolveProviderConfig_ModelRefOpenAI(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		ModelCatalog: ModelCatalogConfig{},
		Providers: map[string]ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "old-model",
			},
		},
	}

	pc, resolved, err := cfg.ResolveProviderConfig("local", ProviderOverrides{
		ModelRef: "code-fast",
	})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "gpt-5.4-mini", pc.Model)
	assert.Equal(t, "code-medium", resolved.CanonicalID)
}

func TestResolveProviderConfig_ModelRefAnthropic(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
	}

	pc, resolved, err := cfg.ResolveProviderConfig("cloud", ProviderOverrides{
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "opus-4.6", pc.Model)
	assert.Equal(t, "code-high", resolved.CanonicalID)
}

func TestResolveProviderConfig_ExplicitModelBypassesCatalog(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
				Model:  "configured-model",
			},
		},
	}

	pc, resolved, err := cfg.ResolveProviderConfig("cloud", ProviderOverrides{
		Model:    "exact-model",
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	assert.Nil(t, resolved)
	assert.Equal(t, "exact-model", pc.Model)
}

func TestResolveProviderConfig_ExternalManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "models.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`
version: 1
generated_at: 2026-04-09T00:00:00Z
profiles:
  code-smart:
    target: gpt-4.1
targets:
  gpt-4.1:
    family: gpt
    aliases: [gpt-smart]
    surfaces:
      agent.openai: gpt-4.1
`), 0o644))

	cfg := Config{
		ModelCatalog: ModelCatalogConfig{Manifest: manifestPath},
		Providers: map[string]ProviderConfig{
			"openrouter": {
				Type:    "openai-compat",
				BaseURL: "https://openrouter.ai/api/v1",
				APIKey:  "test",
			},
		},
	}

	pc, resolved, err := cfg.ResolveProviderConfig("openrouter", ProviderOverrides{
		ModelRef: "gpt-smart",
	})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "gpt-4.1", pc.Model)
	assert.Equal(t, manifestPath, resolved.ManifestSource)
}

func TestResolveProviderConfig_MissingSurface(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
	}

	_, _, err := cfg.ResolveProviderConfig("cloud", ProviderOverrides{
		ModelRef:        "qwen3-coder-next",
		AllowDeprecated: true,
	})
	require.Error(t, err)

	var missingSurfaceErr *modelcatalog.MissingSurfaceError
	require.ErrorAs(t, err, &missingSurfaceErr)
	assert.Equal(t, modelcatalog.SurfaceAgentAnthropic, missingSurfaceErr.Surface)
}

func TestBuildProviderWithOverrides(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
	}

	p, pc, resolved, err := cfg.BuildProviderWithOverrides("cloud", ProviderOverrides{
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "opus-4.6", pc.Model)
	require.NotNil(t, resolved)
	assert.Equal(t, "code-high", resolved.CanonicalID)
}

func TestResolveProviderConfig_AllowDeprecated(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
	}

	_, _, err := cfg.ResolveProviderConfig("cloud", ProviderOverrides{
		ModelRef: "claude-sonnet-3.7",
	})
	require.Error(t, err)

	pc, resolved, err := cfg.ResolveProviderConfig("cloud", ProviderOverrides{
		ModelRef:        "claude-sonnet-3.7",
		AllowDeprecated: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.True(t, resolved.Deprecated)
	assert.Equal(t, "claude-3-7-sonnet-20250219", pc.Model)
}

func TestLoad_LegacySaveRoundTripDoesNotReemitLegacyFields(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
provider: openai-compat
base_url: http://vidar:1234/v1
api_key: secret
model: qwen3.5-7b
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	data, err := cfg.Save()
	require.NoError(t, err)
	rendered := string(data)
	assert.Contains(t, rendered, "providers:")
	assert.NotContains(t, rendered, "\nprovider:")
	assert.NotContains(t, rendered, "\nbase_url: http://vidar:1234/v1\n")
	assert.NotContains(t, rendered, "\napi_key: secret\n")
}

func TestLoad_EnvOverridesCreateDeterministicDefaultProvider(t *testing.T) {
	isolateHome(t)
	t.Setenv("AGENT_MODEL", "env-model")

	cfg := Defaults()
	cfg.Providers = map[string]ProviderConfig{
		"alpha": {Type: "openai-compat", BaseURL: "http://alpha"},
		"zebra": {Type: "openai-compat", BaseURL: "http://zebra"},
	}
	cfg.applyEnvOverrides()

	assert.Equal(t, "default", cfg.Default)
	p, ok := cfg.GetProvider("default")
	require.True(t, ok)
	assert.Equal(t, "env-model", p.Model)
}

func TestExpandEnvVars(t *testing.T) {
	t.Setenv("FOO", "bar")
	assert.Equal(t, "bar", expandEnvVars("${FOO}"))
	assert.Equal(t, "prefix-bar-suffix", expandEnvVars("prefix-${FOO}-suffix"))
	assert.Equal(t, "${UNSET}", expandEnvVars("${UNSET}"))
	assert.Equal(t, "no vars", expandEnvVars("no vars"))
}

func TestSave(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"test": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "test-model",
			},
		},
		Default: "test",
	}

	// Test method Save
	data, err := cfg.Save()
	require.NoError(t, err)
	assert.Contains(t, string(data), "providers:")
	assert.Contains(t, string(data), "test:")

	// Test package-level Save
	data, err = Save(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "providers:")
}

func TestSaveToFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
			},
		},
		Default: "local",
	}

	path := filepath.Join(dir, "config.yaml")
	err := SaveToFile(path, cfg)
	require.NoError(t, err)

	// Verify file exists and has correct permissions
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Verify content
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "local:")
}

// — Backend pool tests —

func TestResolveBackend_FirstAvailable(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"vidar": {
				Type:    "openai-compat",
				BaseURL: "http://vidar:1234/v1",
				Model:   "qwen3",
			},
			"bragi": {
				Type:    "openai-compat",
				BaseURL: "http://bragi:1234/v1",
				Model:   "qwen3",
			},
		},
		Backends: map[string]BackendPoolConfig{
			"local-pool": {
				Providers: []string{"vidar", "bragi"},
				Strategy:  "first-available",
			},
		},
	}

	// All counters pick vidar (index 0) for first-available.
	for _, counter := range []int{0, 1, 2, 5, 99} {
		p, pc, resolved, err := cfg.ResolveBackend("local-pool", counter, ProviderOverrides{})
		require.NoError(t, err, "counter=%d", counter)
		assert.NotNil(t, p)
		assert.Equal(t, "qwen3", pc.Model)
		assert.Equal(t, "http://vidar:1234/v1", pc.BaseURL)
		assert.Nil(t, resolved)
	}
}

func TestResolveBackend_RoundRobin(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"vidar": {
				Type:    "openai-compat",
				BaseURL: "http://vidar:1234/v1",
				Model:   "qwen3",
			},
			"bragi": {
				Type:    "openai-compat",
				BaseURL: "http://bragi:1234/v1",
				Model:   "qwen3",
			},
		},
		Backends: map[string]BackendPoolConfig{
			"rr-pool": {
				Providers: []string{"vidar", "bragi"},
				Strategy:  "round-robin",
			},
		},
	}

	tests := []struct {
		counter  int
		wantBase string
	}{
		{0, "http://vidar:1234/v1"},
		{1, "http://bragi:1234/v1"},
		{2, "http://vidar:1234/v1"},
		{3, "http://bragi:1234/v1"},
		{4, "http://vidar:1234/v1"},
	}
	for _, tt := range tests {
		_, pc, _, err := cfg.ResolveBackend("rr-pool", tt.counter, ProviderOverrides{})
		require.NoError(t, err, "counter=%d", tt.counter)
		assert.Equal(t, tt.wantBase, pc.BaseURL, "counter=%d", tt.counter)
	}
}

func TestResolveBackend_RoundRobin_ThreeProviders(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"a": {Type: "openai-compat", BaseURL: "http://a/v1", Model: "m"},
			"b": {Type: "openai-compat", BaseURL: "http://b/v1", Model: "m"},
			"c": {Type: "openai-compat", BaseURL: "http://c/v1", Model: "m"},
		},
		Backends: map[string]BackendPoolConfig{
			"tri": {
				Providers: []string{"a", "b", "c"},
				Strategy:  "round-robin",
			},
		},
	}

	wantURLs := []string{"http://a/v1", "http://b/v1", "http://c/v1", "http://a/v1", "http://b/v1"}
	for i, want := range wantURLs {
		_, pc, _, err := cfg.ResolveBackend("tri", i, ProviderOverrides{})
		require.NoError(t, err, "counter=%d", i)
		assert.Equal(t, want, pc.BaseURL, "counter=%d", i)
	}
}

func TestResolveBackend_WithModelRef(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {Type: "anthropic", APIKey: "test"},
		},
		Backends: map[string]BackendPoolConfig{
			"smart": {
				ModelRef:  "code-smart",
				Providers: []string{"cloud"},
				Strategy:  "first-available",
			},
		},
	}

	_, pc, resolved, err := cfg.ResolveBackend("smart", 0, ProviderOverrides{})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "opus-4.6", pc.Model)
	assert.Equal(t, "code-high", resolved.CanonicalID)
}

func TestResolveBackend_OverrideModelRef(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {Type: "anthropic", APIKey: "test"},
		},
		Backends: map[string]BackendPoolConfig{
			"smart": {
				ModelRef:  "code-smart",
				Providers: []string{"cloud"},
				Strategy:  "first-available",
			},
		},
	}

	// overrides.ModelRef takes priority over backend model_ref
	_, pc, resolved, err := cfg.ResolveBackend("smart", 0, ProviderOverrides{ModelRef: "code-smart"})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "opus-4.6", pc.Model)
}

func TestResolveBackend_ExplicitModelBypassesCatalog(t *testing.T) {
	isolateHome(t)
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"cloud": {Type: "anthropic", APIKey: "test", Model: "default-model"},
		},
		Backends: map[string]BackendPoolConfig{
			"smart": {
				ModelRef:  "code-smart",
				Providers: []string{"cloud"},
				Strategy:  "first-available",
			},
		},
	}

	_, pc, resolved, err := cfg.ResolveBackend("smart", 0, ProviderOverrides{Model: "pinned-model"})
	require.NoError(t, err)
	assert.Nil(t, resolved)
	assert.Equal(t, "pinned-model", pc.Model)
}

func TestResolveBackend_UnknownBackend(t *testing.T) {
	cfg := Config{}
	_, _, _, err := cfg.ResolveBackend("nonexistent", 0, ProviderOverrides{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown backend pool "nonexistent"`)
}

func TestResolveBackend_EmptyProviders(t *testing.T) {
	cfg := Config{
		Backends: map[string]BackendPoolConfig{
			"empty": {
				Strategy: "first-available",
			},
		},
	}
	_, _, _, err := cfg.ResolveBackend("empty", 0, ProviderOverrides{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no providers")
}

func TestResolveBackend_UnknownProvider(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{},
		Backends: map[string]BackendPoolConfig{
			"bad": {
				Providers: []string{"missing"},
				Strategy:  "first-available",
			},
		},
	}
	_, _, _, err := cfg.ResolveBackend("bad", 0, ProviderOverrides{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestSelectProviderIndex(t *testing.T) {
	tests := []struct {
		strategy string
		counter  int
		n        int
		want     int
	}{
		{"first-available", 0, 3, 0},
		{"first-available", 5, 3, 0},
		{"round-robin", 0, 3, 0},
		{"round-robin", 1, 3, 1},
		{"round-robin", 2, 3, 2},
		{"round-robin", 3, 3, 0},
		{"round-robin", 7, 3, 1},
		{"unknown", 5, 2, 0}, // unknown defaults to first-available
		{"", 5, 2, 0},        // empty defaults to first-available
	}
	for _, tt := range tests {
		got := selectProviderIndex(tt.strategy, tt.counter, tt.n)
		assert.Equal(t, tt.want, got, "strategy=%q counter=%d n=%d", tt.strategy, tt.counter, tt.n)
	}
}

func TestLoad_BackendPools(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  vidar:
    type: openai-compat
    base_url: http://vidar:1234/v1
    model: qwen3
  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
    model: qwen3
backends:
  local-pool:
    model_ref: code-fast
    providers: [vidar, bragi]
    strategy: round-robin
default_backend: local-pool
default: vidar
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, "local-pool", cfg.DefaultBackend)
	require.Len(t, cfg.Backends, 1)

	bc, ok := cfg.GetBackend("local-pool")
	require.True(t, ok)
	assert.Equal(t, "code-fast", bc.ModelRef)
	assert.Equal(t, []string{"vidar", "bragi"}, bc.Providers)
	assert.Equal(t, "round-robin", bc.Strategy)

	translated, ok := cfg.GetDeprecatedBackendRoute("local-pool")
	require.True(t, ok)
	assert.Equal(t, "priority-round-robin", translated.Strategy)
	require.Len(t, translated.Candidates, 2)
	assert.Equal(t, "vidar", translated.Candidates[0].Provider)
	assert.Equal(t, "bragi", translated.Candidates[1].Provider)
	assert.Contains(t, cfg.Warnings(), `backend "local-pool" is deprecated; use model_routes plus --model/--model-ref instead`)
	assert.Contains(t, cfg.Warnings(), "default_backend is deprecated; use routing.default_model or routing.default_model_ref")
}

func TestBackendNames(t *testing.T) {
	cfg := Config{
		Backends: map[string]BackendPoolConfig{
			"zebra":  {},
			"alpha":  {},
			"middle": {},
		},
	}
	assert.Equal(t, []string{"alpha", "middle", "zebra"}, cfg.BackendNames())
}

func TestBackendNames_Empty(t *testing.T) {
	cfg := Config{}
	assert.Nil(t, cfg.BackendNames())
}

func TestLoad_ModelRoutesAndRoutingDefaults(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
  grendel:
    type: openai-compat
    base_url: http://grendel:1234/v1
routing:
  default_model: qwen3.5-27b
  default_model_ref: code-fast
  health_cooldown: 45s
model_routes:
  qwen3.5-27b:
    strategy: priority-round-robin
    candidates:
      - provider: bragi
        model: qwen3.5-27b
        priority: 100
      - provider: grendel
        model: qwen3.5-27b
        priority: 50
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, "qwen3.5-27b", cfg.Routing.DefaultModel)
	assert.Equal(t, "code-fast", cfg.Routing.DefaultModelRef)
	assert.Equal(t, "45s", cfg.Routing.HealthCooldown)

	route, ok := cfg.GetModelRoute("qwen3.5-27b")
	require.True(t, ok)
	assert.Equal(t, "priority-round-robin", route.Strategy)
	require.Len(t, route.Candidates, 2)
	assert.Equal(t, "bragi", route.Candidates[0].Provider)
	assert.Equal(t, "qwen3.5-27b", route.Candidates[0].Model)
	assert.Equal(t, 100, route.Candidates[0].Priority)
	assert.Equal(t, []string{"qwen3.5-27b"}, cfg.ModelRouteNames())
}

func TestLoad_ModelRoutesRejectUnknownProvider(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
model_routes:
  qwen3.5-27b:
    strategy: ordered-failover
    candidates:
      - provider: missing
`), 0o644))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider "missing"`)
}

func TestLoad_ModelRoutesRejectEmptyCandidates(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
model_routes:
  qwen3.5-27b:
    strategy: ordered-failover
    candidates: []
`), 0o644))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `model route "qwen3.5-27b" has no candidates`)
}

func TestLoad_RoutingDefaultModelAllowsAutoDiscoveredIntentRoute(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
routing:
  default_model: missing-route
model_routes:
  qwen3.5-27b:
    strategy: ordered-failover
    candidates:
      - provider: bragi
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "missing-route", cfg.Routing.DefaultModel)
}

func TestLoad_BackendPoolsRejectUnknownProviderDuringTranslation(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
backends:
  local-pool:
    providers: [missing]
    strategy: first-available
`), 0o644))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `backend pool "local-pool" references unknown provider "missing"`)
}

func TestImportMetadata(t *testing.T) {
	cfg := &Config{
		ImportedFrom: &ImportMetadata{
			Source:     "pi",
			Timestamp:  "2026-04-07T12:00:00Z",
			SourceHash: "a1b2c3d4",
		},
	}

	data, err := cfg.Save()
	require.NoError(t, err)
	assert.Contains(t, string(data), "imported_from:")
	assert.Contains(t, string(data), "source: pi")
	assert.Contains(t, string(data), "a1b2c3d4")
}

func TestBuildProvider_Reasoning_PropagatesConfig(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"reasoning": {
				Type:      "openai-compat",
				BaseURL:   "http://localhost:1234/v1",
				Model:     "qwen3.5-27b",
				Reasoning: reasoning.ReasoningMedium,
			},
		},
		Default: "reasoning",
	}

	pc, ok := cfg.GetProvider("reasoning")
	require.True(t, ok)
	assert.Equal(t, reasoning.ReasoningMedium, pc.Reasoning)

	p, err := cfg.BuildProvider("reasoning")
	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestLoad_Reasoning_ParsedFromYAML(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  vidar:
    type: openai-compat
    base_url: http://vidar:1234/v1
    model: qwen3.5-27b
    reasoning: medium
default: vidar
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	pc, ok := cfg.GetProvider("vidar")
	require.True(t, ok)
	assert.Equal(t, reasoning.ReasoningMedium, pc.Reasoning)

	p, err := cfg.BuildProvider("vidar")
	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestLoad_LegacyProviderReasoningKeysRejected(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  vidar:
    type: openai-compat
    base_url: http://vidar:1234/v1
    model: qwen3.5-27b
    `+"thinking"+`_level: medium
default: vidar
`), 0o644))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use reasoning")
}

func TestLoad_ReasoningThresholds(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
reasoning_byte_limit: 524288
reasoning_stall_timeout: "5m"
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, 524288, cfg.ReasoningByteLimit)
	assert.Equal(t, "5m", cfg.ReasoningStallTimeout)

	d, err := cfg.ParseReasoningStallTimeout()
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, d)
}

func TestLoad_ReasoningThresholds_ZeroUnlimited(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
reasoning_byte_limit: 0
reasoning_stall_timeout: "0s"
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	assert.Equal(t, 0, cfg.ReasoningByteLimit)
	assert.Equal(t, "0s", cfg.ReasoningStallTimeout)

	d, err := cfg.ParseReasoningStallTimeout()
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), d)
}

func TestLoad_ReasoningThresholds_Defaults(t *testing.T) {
	isolateHome(t)
	cfg, err := Load(t.TempDir())
	require.NoError(t, err)

	// Defaults should be applied
	assert.Equal(t, 256*1024, cfg.ReasoningByteLimit)
	assert.Equal(t, "5m0s", cfg.ReasoningStallTimeout)
}

func TestParseReasoningStallTimeout_Invalid(t *testing.T) {
	cfg := Defaults()
	cfg.ReasoningStallTimeout = "not-a-duration"
	_, err := cfg.ParseReasoningStallTimeout()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reasoning_stall_timeout")
}

func TestCompactionPercent_Parsed(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
default: local
compaction_percent: 80
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 80, cfg.CompactionPercent)
}

func TestCompactionPercent_AbsentUsesZero(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
default: local
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.CompactionPercent) // 0 means "use compaction default (95%)"
}

func TestCompactionPercent_OutOfRangeRejected(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
default: local
compaction_percent: 101
`), 0o644))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compaction_percent")
}

func TestLoad_Flavor_ParsedFromYAML(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  vidar-omlx:
    type: openai-compat
    base_url: http://vidar:1235/v1
    model: Qwen3.5-27B-4bit
    flavor: omlx
default: vidar-omlx
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	pc, ok := cfg.GetProvider("vidar-omlx")
	require.True(t, ok)
	assert.Equal(t, "omlx", pc.Flavor)
}

func TestLoad_ContextWindow_ParsedFromYAML(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
    model: qwen3.5-27b
    context_window: 262144
default: local
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	pc, ok := cfg.GetProvider("local")
	require.True(t, ok)
	assert.Equal(t, 262144, pc.ContextWindow)
}

func TestLoad_MaxTokens_ParsedFromYAML(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  local:
    type: openai-compat
    base_url: http://localhost:1234/v1
    model: qwen3.5-27b
    max_tokens: 8192
default: local
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	pc, ok := cfg.GetProvider("local")
	require.True(t, ok)
	assert.Equal(t, 8192, pc.MaxTokens)
}

func TestLoad_NewFields_AllTogether(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  vidar-omlx:
    type: openai-compat
    base_url: http://vidar:1235/v1
    model: Qwen3.5-27B-4bit
    flavor: omlx
    context_window: 262144
    max_tokens: 32768
    reasoning: medium
default: vidar-omlx
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	pc, ok := cfg.GetProvider("vidar-omlx")
	require.True(t, ok)
	assert.Equal(t, "omlx", pc.Flavor)
	assert.Equal(t, 262144, pc.ContextWindow)
	assert.Equal(t, 32768, pc.MaxTokens)
	assert.Equal(t, reasoning.ReasoningMedium, pc.Reasoning)

	// Building the provider succeeds — validates that no field breaks wiring.
	p, err := cfg.BuildProvider("vidar-omlx")
	require.NoError(t, err)
	assert.NotNil(t, p)
}

func TestLoad_NewFields_AbsentUseZeroDefaults(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  plain:
    type: openai-compat
    base_url: http://localhost:1234/v1
    model: qwen3.5-27b
default: plain
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	pc, ok := cfg.GetProvider("plain")
	require.True(t, ok)
	assert.Empty(t, pc.Flavor)
	assert.Zero(t, pc.ContextWindow)
	assert.Zero(t, pc.MaxTokens)
}
