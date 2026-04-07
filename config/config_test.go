package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_NewFormat(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".forge")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
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
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".forge")
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
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".forge")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	t.Setenv("TEST_FORGE_KEY", "secret-key-123")

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(`
providers:
  test:
    type: anthropic
    api_key: ${TEST_FORGE_KEY}
    model: claude-sonnet-4-20250514
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	p, ok := cfg.GetProvider("test")
	require.True(t, ok)
	assert.Equal(t, "secret-key-123", p.APIKey)
}

func TestLoad_EnvExpansion_Unset(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".forge")
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
	cfg, err := Load(t.TempDir())
	require.NoError(t, err)

	assert.Equal(t, 20, cfg.MaxIterations)
	assert.Equal(t, ".forge/sessions", cfg.SessionLogDir)
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("FORGE_PROVIDER", "anthropic")
	t.Setenv("FORGE_API_KEY", "env-key")
	t.Setenv("FORGE_MODEL", "env-model")

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
					"X-Title":      "Forge",
				},
			},
		},
	}

	p, err := cfg.BuildProvider("openrouter")
	require.NoError(t, err)
	assert.NotNil(t, p)
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
