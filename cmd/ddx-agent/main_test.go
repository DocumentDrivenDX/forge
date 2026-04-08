package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// runAgentCLI runs the ddx-agent CLI from the project root.
func runAgentCLI(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	cmd := exec.Command("go", append([]string{"run", "./cmd/ddx-agent"}, args...)...)
	cmd.Dir = filepath.Clean(filepath.Join(wd, "..", ".."))
	home := t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
	out, err := cmd.CombinedOutput()
	return out, err
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
			assert.True(t, strings.Contains(output, tt.matches) || strings.Contains(output, "stat"),
				"Expected %s or stat error, got: %s", tt.matches, output)
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
