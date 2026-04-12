package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/DocumentDrivenDX/agent"
)

const (
	defaultBashTimeout = 120 * time.Second
)

// BashParams are the parameters for the bash tool.
type BashParams struct {
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

// BashTool executes shell commands.
type BashTool struct {
	WorkDir string
}

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	return "Execute a shell command. Returns stdout, stderr, and exit code."
}
func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command":    {"type": "string", "description": "Shell command to execute"},
			"timeout_ms": {"type": "integer", "description": "Timeout in milliseconds (default 120000)"}
		},
		"required": ["command"]
	}`)
}

func (t *BashTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p BashParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("bash: invalid params: %w", err)
	}

	timeout := defaultBashTimeout
	if p.TimeoutMs > 0 {
		timeout = time.Duration(p.TimeoutMs) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- the shell command is an explicit user-provided tool input.
	cmd := exec.CommandContext(ctx, "sh", "-c", p.Command)
	cmd.Dir = t.WorkDir
	cmd.Stdin = nil             // /dev/null
	cmd.WaitDelay = time.Second // don't hang waiting for pipe goroutines after kill

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	out := TruncateTail(string(stdout.Bytes()), truncMaxLines, truncMaxBytes)
	errOut := TruncateTail(string(stderr.Bytes()), truncMaxLines, truncMaxBytes)

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	result := fmt.Sprintf("exit_code: %d\nstdout:\n%s", exitCode, out)
	if len(errOut) > 0 {
		result += fmt.Sprintf("\nstderr:\n%s", errOut)
	}

	if ctx.Err() == context.DeadlineExceeded {
		result += "\n[timed out]"
		return result, fmt.Errorf("bash: command timed out after %v", timeout)
	}

	if err != nil {
		if ctx.Err() != nil {
			return result, fmt.Errorf("bash: %w", ctx.Err())
		}
		// Non-zero exit is not a Go error — the model can interpret the exit code.
		// Only return an error for actual execution failures (command not found, etc.)
		if cmd.ProcessState == nil {
			return "", fmt.Errorf("bash: %w", err)
		}
	}

	return result, nil
}


var _ agent.Tool = (*BashTool)(nil)
