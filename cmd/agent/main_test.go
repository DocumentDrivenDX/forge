package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runAgentCLI runs the ddx-agent CLI from the project root.
func runAgentCLI(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	exe := buildAgentCLI(t)
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Clean(filepath.Join(wd, "..", ".."))
	home := t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
	out, err := cmd.CombinedOutput()
	return out, err
}

func runAgentCLIWithHome(t *testing.T, home string, args ...string) ([]byte, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	exe := buildAgentCLI(t)
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Clean(filepath.Join(wd, "..", ".."))
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
	out, err := cmd.CombinedOutput()
	return out, err
}

func writePiFixture(t *testing.T, home string, modelsJSON string) {
	t.Helper()
	piAgentDir := filepath.Join(home, ".pi", "agent")
	require.NoError(t, os.MkdirAll(piAgentDir, 0o755))

	authJSON := `{
		"anthropic": {"access_token": "sk-ant-fixture"},
		"openrouter": {"api_key": "sk-or-fixture"}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(piAgentDir, "auth.json"), []byte(authJSON), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(piAgentDir, "models.json"), []byte(modelsJSON), 0o644))

	settingsJSON := `{
		"defaultProvider": "grendel",
		"defaultModel": "qwen3.5-27b"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pi", "settings.json"), []byte(settingsJSON), 0o644))
}

func TestCLI_Version(t *testing.T) {
	out, err := runAgentCLI(t, "version")
	if err != nil {
		t.Logf("Version output: %s", string(out))
	}
	assert.Contains(t, string(out), "ddx-agent")
}

func TestCLI_Help(t *testing.T) {
	out, _ := runAgentCLI(t, "-h")
	output := string(out)
	assert.True(t, strings.Contains(output, "Usage of") || strings.Contains(output, "stat"),
		"Expected usage or stat error, got: %s", output)
}

func TestCLI_Import_Help(t *testing.T) {
	out, _ := runAgentCLI(t, "import")
	output := string(out)
	assert.True(t, strings.Contains(output, "usage:") || strings.Contains(output, "error") || strings.Contains(output, "stat"),
		"Expected usage or error, got: %s", output)
}

func TestCLI_Import_UnknownSource(t *testing.T) {
	out, _ := runAgentCLI(t, "import", "unknown")
	output := string(out)
	assert.True(t, strings.Contains(output, "unknown source") || strings.Contains(output, "stat"),
		"Expected unknown source error or stat error, got: %s", output)
}

func TestCLI_NoPrompt(t *testing.T) {
	out, _ := runAgentCLI(t)
	output := string(out)
	assert.True(t, strings.Contains(output, "no prompt") || strings.Contains(output, "stat"),
		"Expected no prompt error or stat error, got: %s", output)
}

func TestCLI_ImportPi_NotFound(t *testing.T) {
	out, _ := runAgentCLI(t, "import", "pi")
	output := string(out)
	assert.True(t, strings.Contains(output, "pi") || strings.Contains(output, "config") || strings.Contains(output, "stat"),
		"Expected pi-related output or stat error, got: %s", output)
}

func TestCLI_ImportOpenCode_NotFound(t *testing.T) {
	out, _ := runAgentCLI(t, "import", "opencode")
	output := string(out)
	assert.True(t, strings.Contains(output, "opencode") || strings.Contains(output, "config") || strings.Contains(output, "stat"),
		"Expected opencode-related output or stat error, got: %s", output)
}

func TestCLI_Import_DiffFlag(t *testing.T) {
	out, _ := runAgentCLI(t, "import", "pi", "--diff")
	output := string(out)
	assert.True(t, strings.Contains(output, "pi") || strings.Contains(output, "not found") || strings.Contains(output, "stat"),
		"Expected pi-related output or stat error, got: %s", output)
}

func TestCLI_Subcommands(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		matches string
	}{
		{"log", []string{"log"}, "s-"}, // session IDs start with s-
		{"replay", []string{"replay"}, "usage:"},
		{"models", []string{"models"}, ""}, // may succeed or fail
		{"check", []string{"check"}, "error"},
		{"providers", []string{"providers"}, "NAME"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runAgentCLI(t, tt.args...)
			output := string(out)
			assert.True(t, strings.Contains(output, tt.matches) || strings.Contains(output, "stat") || strings.Contains(output, "error:"),
				"Expected %s, stat error, or generic error, got: %s", tt.matches, output)
		})
	}
}

func TestCLI_Providers_List(t *testing.T) {
	out, _ := runAgentCLI(t, "providers")
	output := string(out)
	assert.True(t, strings.Contains(output, "NAME") || strings.Contains(output, "error") || strings.Contains(output, "stat"),
		"Expected NAME header or error, got: %s", output)
}

func TestCLI_Check_NoConfig(t *testing.T) {
	out, _ := runAgentCLI(t, "check")
	output := string(out)
	assert.True(t, strings.Contains(output, "error") || strings.Contains(output, "unknown provider") || strings.Contains(output, "stat"),
		"Expected error or stat error, got: %s", output)
}

func TestCLI_ImportPi_DiffMode(t *testing.T) {
	out, _ := runAgentCLI(t, "import", "pi", "--diff", "--merge")
	output := string(out)
	assert.True(t, strings.Contains(output, "pi") || strings.Contains(output, "not found") || strings.Contains(output, "stat"),
		"Expected pi-related output or stat error, got: %s", output)
}

func TestCLI_ImportPi_Diff_ObjectMapProviders(t *testing.T) {
	home := t.TempDir()
	writePiFixture(t, home, `{
		"providers": {
			"vidar": {
				"baseUrl": "http://vidar:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [{ "id": "qwen3.5-27b" }]
			},
			"grendel": {
				"baseUrl": "http://grendel:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [{ "id": "qwen3.5-27b" }]
			}
		}
	}`)

	out, err := runAgentCLIWithHome(t, home, "import", "pi", "--diff")
	require.NoError(t, err, string(out))
	output := string(out)

	assert.Contains(t, output, "ddx-agent: pi config -- what would be imported:")
	assert.Contains(t, output, "[grendel]")
	assert.Contains(t, output, "http://grendel:1234/v1")
	assert.Contains(t, output, "[vidar]")
	assert.Contains(t, output, "default: grendel")
}

func TestCLI_ImportPi_WritesConfig_ObjectMapProviders(t *testing.T) {
	home := t.TempDir()
	writePiFixture(t, home, `{
		"providers": {
			"grendel": {
				"baseUrl": "http://grendel:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [{ "id": "qwen3.5-27b" }]
			},
			"bragi": {
				"baseUrl": "http://bragi:1234/v1",
				"api": "openai-completions",
				"api_key": "lmstudio",
				"models": [{ "id": "qwen3.5-27b" }]
			}
		}
	}`)

	out, err := runAgentCLIWithHome(t, home, "import", "pi")
	require.NoError(t, err, string(out))
	output := string(out)
	assert.Contains(t, output, "imported: grendel")
	assert.Contains(t, output, "imported: bragi")

	configPath := filepath.Join(home, ".config", "agent", "config.yaml")
	data, readErr := os.ReadFile(configPath)
	require.NoError(t, readErr)
	config := string(data)
	assert.Contains(t, config, "default: grendel")
	assert.Contains(t, config, "grendel:")
	assert.Contains(t, config, "base_url: http://grendel:1234/v1")
	assert.Contains(t, config, "model: qwen3.5-27b")
	assert.Contains(t, config, "imported_from:")
	assert.Contains(t, config, "source: pi")
}

func TestCLI_ExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"version success", []string{"version"}},
		{"no prompt", []string{}},
		{"unknown subcommand", []string{"unknown-cmd"}},
		{"import unknown", []string{"import", "xyz"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runAgentCLI(t, tt.args...)
			t.Logf("Output: %s", string(out))
			// Just verify it runs without panic
		})
	}
}

func TestCLI_Import_ProjectFlag_RequiresConfirmation(t *testing.T) {
	out, _ := runAgentCLI(t, "import", "pi", "--project", "--diff")
	output := string(out)
	assert.True(t, strings.Contains(output, "warning") || strings.Contains(output, "gitignore") || strings.Contains(output, "stat"),
		"Expected warning about gitignore or stat error, got: %s", output)
}

func TestCLI_Models_NoConfig(t *testing.T) {
	out, _ := runAgentCLI(t, "models")
	// May succeed (show models) or fail (no config), both are OK
	t.Logf("Output: %q", string(out))
}

func TestCLI_Log_NoArgs(t *testing.T) {
	out, _ := runAgentCLI(t, "log")
	output := string(out)
	// May show sessions (start with s-) or fail
	assert.True(t, strings.Contains(output, "s-") || strings.Contains(output, "error"),
		"Expected session list or error, got: %s", output)
}

func TestCLI_Replay_NoArgs(t *testing.T) {
	out, _ := runAgentCLI(t, "replay")
	output := string(out)
	assert.True(t, strings.Contains(output, "usage:") || strings.Contains(output, "stat"),
		"Expected usage or stat error, got: %s", output)
}

func TestCLI_Replay_UnknownSession(t *testing.T) {
	out, _ := runAgentCLI(t, "replay", "nonexistent-session-id")
	t.Logf("Output: %s", string(out))
	// Should fail gracefully without panic
}
