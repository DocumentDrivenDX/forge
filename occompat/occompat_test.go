package occompat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAuth(t *testing.T) {
	tmpDir := t.TempDir()

	authJSON := `{
		"openai": {
			"type": "api",
			"key": "sk-test-key"
		}
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "auth.json"), []byte(authJSON), 0644)
	require.NoError(t, err)

	creds, err := LoadAuth(tmpDir)
	require.NoError(t, err)
	assert.Len(t, creds, 1)
	assert.Equal(t, "api", creds["openai"].Type)
	assert.Equal(t, "sk-test-key", creds["openai"].Key)
}

func TestLoadConfig_Project(t *testing.T) {
	tmpDir := t.TempDir()

	configJSON := `{
		"options": {
			"baseURL": "https://api.example.com/v1",
			"apiKey": "test-key",
			"model": "custom-model"
		}
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "opencode.json"), []byte(configJSON), 0644)
	require.NoError(t, err)

	cfg, err := LoadConfig(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/v1", cfg.Options.BaseURL)
	assert.Equal(t, "test-key", cfg.Options.APIKey)
	assert.Equal(t, "custom-model", cfg.Options.Model)
}

func TestLoadConfig_Global(t *testing.T) {
	// Test that LoadGlobalConfig works when file exists
	// Note: This test may fail if ~/.config/opencode/opencode.json doesn't exist
	// Just verify the function structure works
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home directory")
	}

	// Create temp global config
	tmpDir := t.TempDir()
	globalDir := filepath.Join(tmpDir, ".config", "opencode")
	err := os.MkdirAll(globalDir, 0755)
	require.NoError(t, err)

	configJSON := `{
		"options": {
			"baseURL": "https://global.example.com/v1",
			"npm": "@ai-sdk/openai-compatible"
		}
	}`
	err = os.WriteFile(filepath.Join(globalDir, "opencode.json"), []byte(configJSON), 0644)
	require.NoError(t, err)

	// Temporarily change home for this test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	cfg, err := LoadGlobalConfig()
	require.NoError(t, err)
	assert.Equal(t, "https://global.example.com/v1", cfg.Options.BaseURL)
	assert.Equal(t, "@ai-sdk/openai-compatible", cfg.Options.NPM)
}

func TestLoadConfig_NotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent")
	assert.Error(t, err)

	_, err = LoadGlobalConfig()
	assert.Error(t, err)
}

func TestTranslate(t *testing.T) {
	tmpDir := t.TempDir()

	// Write opencode.json
	configJSON := `{
		"options": {
			"baseURL": "https://api.example.com/v1",
			"apiKey": "secret-key",
			"headers": {
				"X-Custom": "value"
			}
		}
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "opencode.json"), []byte(configJSON), 0644)
	require.NoError(t, err)

	result := Translate(tmpDir, "")

	assert.Equal(t, "openai-compat", result.Provider.Type)
	assert.Equal(t, "https://api.example.com/v1", result.Provider.BaseURL)
	assert.Equal(t, "secret-key", result.Provider.APIKey)
	assert.Equal(t, "value", result.Provider.Headers["X-Custom"])
}

func TestTranslate_WithAuthKey(t *testing.T) {
	tmpDir := t.TempDir()

	// Write opencode.json without apiKey
	configJSON := `{
		"options": {
			"baseURL": "https://api.example.com/v1"
		}
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "opencode.json"), []byte(configJSON), 0644)
	require.NoError(t, err)

	result := Translate(tmpDir, "auth-key-from-json")

	assert.Equal(t, "auth-key-from-json", result.Provider.APIKey)
}

func TestTranslate_NPMMapping(t *testing.T) {
	tmpDir := t.TempDir()

	configJSON := `{
		"options": {
			"npm": "@ai-sdk/openai-compatible"
		}
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "opencode.json"), []byte(configJSON), 0644)
	require.NoError(t, err)

	result := Translate(tmpDir, "")

	assert.Equal(t, "openai-compat", result.Provider.Type)
}

func TestTranslate_Headers(t *testing.T) {
	tmpDir := t.TempDir()

	configJSON := `{
		"options": {
			"headers": {
				"HTTP-Referer": "https://example.com",
				"X-Title": "My App"
			}
		}
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "opencode.json"), []byte(configJSON), 0644)
	require.NoError(t, err)

	result := Translate(tmpDir, "")

	assert.Equal(t, "https://example.com", result.Provider.Headers["HTTP-Referer"])
	assert.Equal(t, "My App", result.Provider.Headers["X-Title"])
}

func TestComputeSourceHash(t *testing.T) {
	tmpDir := t.TempDir()

	err := os.WriteFile(filepath.Join(tmpDir, "auth.json"), []byte("test"), 0644)
	require.NoError(t, err)

	// Create global config
	globalDir := filepath.Join(tmpDir, ".config", "opencode")
	err = os.MkdirAll(globalDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(globalDir, "opencode.json"), []byte("test"), 0644)
	require.NoError(t, err)

	// Temporarily change home for this test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	hash, err := ComputeSourceHash(tmpDir)
	require.NoError(t, err)
	assert.Len(t, hash, 8) // truncated to 8 chars
}

func TestDefaultOpenCodeDir(t *testing.T) {
	dir := DefaultOpenCodeDir()
	assert.Contains(t, dir, "opencode")
}

func TestCheckExists(t *testing.T) {
	// Without actual opencode config, should return false
	// This test just verifies the function doesn't panic
	exists := CheckExists()
	// Result depends on whether opencode is installed
	_ = exists // just verify it runs
}
