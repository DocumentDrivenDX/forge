package picompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAuth(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	// Write test auth.json
	authJSON := `{
		"anthropic": {
			"access_token": "sk-ant-test123",
			"expires": 1749331200000
		},
		"openrouter": {
			"api_key": "sk-or-test456"
		}
	}`
	err = os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(authJSON), 0644)
	require.NoError(t, err)

	// Load and verify
	creds, err := LoadAuth(tmpDir)
	require.NoError(t, err)
	assert.Len(t, creds, 2)

	assert.Equal(t, "sk-ant-test123", creds["anthropic"].AccessToken)
	assert.Equal(t, int64(1749331200000), creds["anthropic"].Expires)
	assert.Equal(t, "sk-or-test456", creds["openrouter"].APIKey)
}

func TestLoadModels(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	modelsJSON := `{
		"providers": [
			{
				"name": "vidar",
				"baseUrl": "http://vidar:1234/v1",
				"api": "openai-completions",
				"models": ["qwen3.5-7b"]
			}
		]
	}`
	err = os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644)
	require.NoError(t, err)

	cfg, err := LoadModels(tmpDir)
	require.NoError(t, err)
	require.Len(t, cfg.Providers, 1)

	prov := cfg.Providers[0]
	assert.Equal(t, "vidar", prov.Name)
	assert.Equal(t, "http://vidar:1234/v1", prov.BaseURL)
	assert.Equal(t, "openai-completions", prov.API)
	assert.Len(t, prov.Models, 1)
	assert.Equal(t, "qwen3.5-7b", prov.Models[0])
}

func TestLoadSettings(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	// settings.json is at the same level as agent/
	settingsJSON := `{
		"defaultProvider": "anthropic",
		"defaultModel": "claude-sonnet-4-20250514",
		"max_iterations": 30
	}`
	err = os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(settingsJSON), 0644)
	require.NoError(t, err)

	settings, err := LoadSettings(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", settings.DefaultProvider)
	assert.Equal(t, "claude-sonnet-4-20250514", settings.DefaultModel)
	assert.Equal(t, 30, settings.MaxIterations)
}

func TestLoadSettings_Optional(t *testing.T) {
	// settings.json is optional
	settings, err := LoadSettings("/nonexistent")
	assert.Error(t, err)
	assert.Nil(t, settings)
}

func TestTranslate_TwoSourceMerge(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	// auth.json
	authJSON := `{
		"anthropic": {"access_token": "sk-ant-api-key"},
		"openrouter": {"api_key": "sk-or-api-key"}
	}`
	err = os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(authJSON), 0644)
	require.NoError(t, err)

	// models.json
	modelsJSON := `{
		"providers": [
			{
				"name": "vidar",
				"baseUrl": "http://vidar:1234/v1",
				"api": "openai-completions",
				"models": ["qwen3.5-7b"]
			}
		]
	}`
	err = os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644)
	require.NoError(t, err)

	// settings.json with defaults
	settingsJSON := `{
		"defaultProvider": "anthropic",
		"defaultModel": "claude-sonnet-4-20250514"
	}`
	err = os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(settingsJSON), 0644)
	require.NoError(t, err)

	result, err := Translate(tmpDir)
	require.NoError(t, err)

	// Should have vidar from models.json
	assert.Contains(t, result.Providers, "vidar")
	assert.Equal(t, "openai-compat", result.Providers["vidar"].Type)
	assert.Equal(t, "http://vidar:1234/v1", result.Providers["vidar"].BaseURL)
	assert.Equal(t, "qwen3.5-7b", result.Providers["vidar"].Model)

	// Should have anthropic from auth.json (no model in models)
	assert.Contains(t, result.Providers, "anthropic")
	assert.Equal(t, "anthropic", result.Providers["anthropic"].Type)
	assert.Equal(t, "sk-ant-api-key", result.Providers["anthropic"].APIKey)

	// Should have openrouter from auth.json
	assert.Contains(t, result.Providers, "openrouter")
	assert.Equal(t, "openai-compat", result.Providers["openrouter"].Type)
	assert.Equal(t, "https://openrouter.ai/api/v1", result.Providers["openrouter"].BaseURL)

	// Default should be anthropic
	assert.Equal(t, "anthropic", result.Default)
}

func TestTranslate_SkipsUnsupported(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	authJSON := `{
		"google-gemini-cli": {"api_key": "gemini-key"},
		"github-copilot": {"api_key": "copilot-key"}
	}`
	err = os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(authJSON), 0644)
	require.NoError(t, err)

	modelsJSON := `{"providers": []}`
	err = os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644)
	require.NoError(t, err)

	result, err := Translate(tmpDir)
	require.NoError(t, err)

	// Unsupported providers should be skipped with warnings
	assert.Len(t, result.Warnings, 2)
	// Warnings may be in any order
	hasGemini := false
	hasCopilot := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "google-gemini-cli") {
			hasGemini = true
		}
		if strings.Contains(w, "github-copilot") {
			hasCopilot = true
		}
	}
	assert.True(t, hasGemini, "should have warning for google-gemini-cli")
	assert.True(t, hasCopilot, "should have warning for github-copilot")
}

func TestTranslate_SkipsCommandKeys(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	authJSON := `{
		"openai-codex": {"api_key": "!echo $API_KEY"}
	}`
	err = os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(authJSON), 0644)
	require.NoError(t, err)

	modelsJSON := `{"providers": []}`
	err = os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644)
	require.NoError(t, err)

	result, err := Translate(tmpDir)
	require.NoError(t, err)

	assert.Contains(t, result.Warnings[0], "shell-resolved key")
}

func TestComputeSourceHash(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte("test"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(agentDir, "models.json"), []byte("test"), 0644)
	require.NoError(t, err)

	hash, err := ComputeSourceHash(tmpDir)
	require.NoError(t, err)
	assert.Len(t, hash, 8) // truncated to 8 chars
}

func TestTokenExpiryStatus(t *testing.T) {
	tests := []struct {
		name     string
		entry    AuthEntry
		minHours float64
		hasWarn  bool
	}{
		{
			name:     "no expiry info",
			entry:    AuthEntry{},
			minHours: 24,
			hasWarn:  false,
		},
		{
			name:     "expires in 48 hours",
			entry:    AuthEntry{Expires: 48 * 60 * 60 * 1000},
			minHours: 47,
			hasWarn:  false,
		},
		{
			name:     "expires in 3 hours",
			entry:    AuthEntry{Expires: 3 * 60 * 60 * 1000},
			minHours: 2,
			hasWarn:  true,
		},
		{
			name:     "already expired",
			entry:    AuthEntry{Expires: -1000},
			minHours: -1,
			hasWarn:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hours, warn := tt.entry.TokenExpiryStatus()
			assert.GreaterOrEqual(t, hours, tt.minHours)
			if tt.minHours > 0 {
				assert.LessOrEqual(t, hours, tt.minHours+1)
			}
			assert.Equal(t, tt.hasWarn, warn != "")
		})
	}
}
