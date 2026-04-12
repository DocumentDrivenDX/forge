package picompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPiDir_UsesProcessHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/picompat-home")

	dir := DefaultPiDir()
	assert.Equal(t, filepath.Join("/tmp/picompat-home", ".pi"), dir)
}

func TestLoadAuth(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	// Write test auth.json using pi's actual field names:
	// oauth uses "access", api_key uses "key"
	authJSON := `{
		"anthropic": {
			"type": "oauth",
			"access": "sk-ant-test123",
			"expires": 1749331200000
		},
		"openrouter": {
			"type": "api_key",
			"key": "sk-or-test456"
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
	assert.Equal(t, "sk-or-test456", creds["openrouter"].Key)
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

func TestLoadModels_ObjectMapProviders(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	modelsJSON := `{
		"providers": {
			"vidar": {
				"baseUrl": "http://vidar:1234/v1",
				"api": "openai-completions",
				"models": [
					{ "id": "qwen3.5-27b" },
					{ "id": "openai/gpt-oss-20b" }
				]
			},
			"bragi": {
				"baseUrl": "http://bragi:1234/v1",
				"api": "openai-completions",
				"models": [
					{ "id": "qwen3.5-27b" }
				]
			}
		}
	}`
	err = os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644)
	require.NoError(t, err)

	cfg, err := LoadModels(tmpDir)
	require.NoError(t, err)
	require.Len(t, cfg.Providers, 2)

	vidar := cfg.GetProviderByName("vidar")
	require.NotNil(t, vidar)
	assert.Equal(t, "http://vidar:1234/v1", vidar.BaseURL)
	assert.Equal(t, []string{"qwen3.5-27b", "openai/gpt-oss-20b"}, vidar.Models)

	bragi := cfg.GetProviderByName("bragi")
	require.NotNil(t, bragi)
	assert.Equal(t, []string{"qwen3.5-27b"}, bragi.Models)
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

	// auth.json — use pi's actual field names
	authJSON := `{
		"anthropic": {"type": "oauth", "access": "sk-ant-api-key"},
		"openrouter": {"type": "api_key", "key": "sk-or-api-key"}
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

func TestTranslate_CurrentPiObjectMapShape(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	err := os.MkdirAll(agentDir, 0755)
	require.NoError(t, err)

	authJSON := `{
		"openrouter": {"api_key": "sk-or-api-key"}
	}`
	err = os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(authJSON), 0644)
	require.NoError(t, err)

	modelsJSON := `{
		"providers": {
			"vidar": {
				"baseUrl": "http://vidar:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [
					{ "id": "qwen3.5-27b" },
					{ "id": "openai/gpt-oss-20b" }
				]
			},
			"grendel": {
				"baseUrl": "http://grendel:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [
					{ "id": "qwen3.5-27b" }
				]
			}
		}
	}`
	err = os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644)
	require.NoError(t, err)

	settingsJSON := `{
		"defaultProvider": "grendel",
		"defaultModel": "qwen3.5-27b"
	}`
	err = os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(settingsJSON), 0644)
	require.NoError(t, err)

	result, err := Translate(tmpDir)
	require.NoError(t, err)

	require.Contains(t, result.Providers, "vidar")
	assert.Equal(t, "http://vidar:1234/v1", result.Providers["vidar"].BaseURL)
	assert.Equal(t, "qwen3.5-27b", result.Providers["vidar"].Model)
	assert.Equal(t, "lmstudio", result.Providers["vidar"].APIKey)

	require.Contains(t, result.Providers, "grendel")
	assert.Equal(t, "http://grendel:1234/v1", result.Providers["grendel"].BaseURL)
	assert.Equal(t, "qwen3.5-27b", result.Providers["grendel"].Model)
	assert.Equal(t, "grendel", result.Default)

	require.Contains(t, result.Providers, "openrouter")
	assert.Equal(t, "https://openrouter.ai/api/v1", result.Providers["openrouter"].BaseURL)
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

func TestAuthEntry_ResolvedKey(t *testing.T) {
	tests := []struct {
		name     string
		entry    AuthEntry
		expected string
	}{
		{"oauth access token", AuthEntry{AccessToken: "sk-ant-oat01-abc"}, "sk-ant-oat01-abc"},
		{"api_key field", AuthEntry{APIKey: "sk-or-v1-abc"}, "sk-or-v1-abc"},
		{"key field (pi api_key type)", AuthEntry{Key: "sk-z-abc"}, "sk-z-abc"},
		{"access takes priority over key", AuthEntry{AccessToken: "oauth-tok", Key: "api-key"}, "oauth-tok"},
		{"key takes priority over api_key", AuthEntry{Key: "key-field", APIKey: "api_key_field"}, "key-field"},
		{"empty entry", AuthEntry{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.entry.ResolvedKey())
		})
	}
}

func TestTranslate_NewCloudProviders(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	require.NoError(t, os.MkdirAll(agentDir, 0755))

	authJSON := `{
		"qwen":     {"type": "api_key", "key": "sk-qwen-abc"},
		"dashscope":{"type": "api_key", "key": "sk-dash-abc"},
		"minimax":  {"type": "api_key", "key": "sk-mm-abc"},
		"z.ai":     {"type": "api_key", "key": "sk-zai-abc"}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(authJSON), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(`{"providers":[]}`), 0644))

	result, err := Translate(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, result.Warnings)

	qwen := result.Providers["qwen"]
	assert.Equal(t, "openai-compat", qwen.Type)
	assert.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", qwen.BaseURL)
	// Both qwen and dashscope map to agent name "qwen"; Go map iteration is random
	// so either key may win — just verify one of them was used.
	assert.True(t, qwen.APIKey == "sk-qwen-abc" || qwen.APIKey == "sk-dash-abc",
		"qwen provider should have one of the two keys, got %q", qwen.APIKey)

	// dashscope is an alias for qwen — both map to the same agent name "qwen"
	// the second entry overwrites; we just verify it resolves without error
	minimax := result.Providers["minimax"]
	assert.Equal(t, "openai-compat", minimax.Type)
	assert.Equal(t, "https://api.minimaxi.chat/v1", minimax.BaseURL)
	assert.Equal(t, "sk-mm-abc", minimax.APIKey)

	zai := result.Providers["z.ai"]
	assert.Equal(t, "openai-compat", zai.Type)
	assert.Equal(t, "https://api.z.ai/v1", zai.BaseURL)
	assert.Equal(t, "sk-zai-abc", zai.APIKey)
}

func TestTranslate_OAuthAccessToken(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	require.NoError(t, os.MkdirAll(agentDir, 0755))

	// Matches pi's actual auth.json shape for oauth providers
	authJSON := `{
		"anthropic": {
			"type": "oauth",
			"access": "sk-ant-oat01-real-token",
			"refresh": "sk-ant-ort01-refresh",
			"expires": 1775681733815
		},
		"openrouter": {
			"type": "api_key",
			"key": "sk-or-v1-real-key"
		}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(authJSON), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(`{"providers":[]}`), 0644))

	result, err := Translate(tmpDir)
	require.NoError(t, err)

	// Anthropic oauth access token should be used as the API key
	assert.Equal(t, "sk-ant-oat01-real-token", result.Providers["anthropic"].APIKey)
	// OpenRouter key field should be used
	assert.Equal(t, "sk-or-v1-real-key", result.Providers["openrouter"].APIKey)
}

func TestTranslate_ThinkingModelAutoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	require.NoError(t, os.MkdirAll(agentDir, 0755))

	modelsJSON := `{
		"providers": {
			"vidar": {
				"baseUrl": "http://vidar:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [{"id": "qwen3.5-27b"}]
			},
			"bragi": {
				"baseUrl": "http://bragi:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [{"id": "deepseek-r1-distill-qwen-32b"}]
			},
			"grendel": {
				"baseUrl": "http://grendel:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [{"id": "llama3.1-8b"}]
			}
		}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(`{}`), 0644))

	result, err := Translate(tmpDir)
	require.NoError(t, err)

	// qwen3.5-27b → thinking_level: medium
	assert.Equal(t, "medium", result.Providers["vidar"].ThinkingLevel, "qwen3 model should get thinking_level")
	// deepseek-r1 variant → thinking_level: medium
	assert.Equal(t, "medium", result.Providers["bragi"].ThinkingLevel, "deepseek-r1 model should get thinking_level")
	// llama3.1 → no thinking_level
	assert.Equal(t, "", result.Providers["grendel"].ThinkingLevel, "non-thinking model should not get thinking_level")
}

func TestIsThinkingModel(t *testing.T) {
	thinking := []string{"qwen3.5-27b", "qwen3-coder-30b", "Qwen3-72B", "deepseek-r1", "deepseek-r1-distill-qwen-32b", "deepseek_r1", "qwq-32b"}
	notThinking := []string{"qwen2.5-coder", "llama3.1-8b", "gpt-4o", "claude-sonnet-4-6", "gemma-4-26b", "qwen3.5-27b-claude-4.6-opus-distilled-mlx"}

	for _, m := range thinking {
		assert.True(t, isThinkingModel(m), "expected %q to be a thinking model", m)
	}
	for _, m := range notThinking {
		assert.False(t, isThinkingModel(m), "expected %q to NOT be a thinking model", m)
	}
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
