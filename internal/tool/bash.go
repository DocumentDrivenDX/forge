package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
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
	WorkDir      string
	OutputFilter BashOutputFilterConfig
	Mode         string
}

type BashOutputFilterConfig struct {
	Mode         string
	RTKBinary    string
	MaxBytes     int
	RawOutputDir string
}

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	return "Execute a shell command for targeted repo operations such as builds, tests, or git. Do not use this for file discovery or file reading; use the find, grep, ls, and read tools instead. Returns stdout, stderr, and exit code."
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

	if violation := t.policyViolation(p.Command); violation != "" {
		return fmt.Sprintf("Exit code: 2\nWall time: 0.00s\nOutput:\n(policy blocked)\nStderr:\n%s", violation), nil
	}

	timeout := defaultBashTimeout
	if p.TimeoutMs > 0 {
		timeout = time.Duration(p.TimeoutMs) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	plan := t.planOutputFilter(p.Command)
	command := plan.Command

	// #nosec G204 -- the shell command is an explicit user-provided tool input.
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = t.WorkDir
	cmd.Stdin = nil             // /dev/null
	cmd.WaitDelay = time.Second // don't hang waiting for pipe goroutines after kill

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	stdoutText := applyFilterMaxBytes(string(stdout.Bytes()), t.OutputFilter.MaxBytes)
	stderrText := applyFilterMaxBytes(string(stderr.Bytes()), t.OutputFilter.MaxBytes)
	out := TruncateTail(stdoutText, truncMaxLines, truncMaxBytes)
	errOut := TruncateTail(stderrText, truncMaxLines, truncMaxBytes)

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	outSection := out
	if len(outSection) == 0 {
		outSection = "(no output)"
	}

	result := fmt.Sprintf("Exit code: %d\nWall time: %.2fs\n", exitCode, elapsed.Seconds())
	if plan.Marker != "" {
		result += plan.Marker + "\n"
	}
	result += fmt.Sprintf("Output:\n%s", outSection)
	if len(errOut) > 0 {
		result += fmt.Sprintf("\nStderr:\n%s", errOut)
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

func (t *BashTool) Parallel() bool { return false }

var _ agent.Tool = (*BashTool)(nil)

type bashFilterPlan struct {
	Command string
	Marker  string
}

func (t *BashTool) planOutputFilter(command string) bashFilterPlan {
	mode := strings.ToLower(strings.TrimSpace(t.OutputFilter.Mode))
	if mode == "" || mode == "off" {
		return bashFilterPlan{Command: command}
	}
	if mode != "rtk" && mode != "auto" {
		return bashFilterPlan{Command: command, Marker: fmt.Sprintf("[output filter unavailable: unsupported mode %q; used raw output]", t.OutputFilter.Mode)}
	}
	if !rtkCommandAllowed(command) {
		return bashFilterPlan{Command: command}
	}

	binary := strings.TrimSpace(t.OutputFilter.RTKBinary)
	if binary == "" {
		binary = "rtk"
	}
	path, err := resolveExecutable(binary)
	if err != nil {
		return bashFilterPlan{Command: command, Marker: fmt.Sprintf("[output filter unavailable: %s not found; used raw output]", binary)}
	}
	return bashFilterPlan{
		Command: shellQuote(path) + " " + command,
		Marker:  "[output filter: rtk " + rtkCommandSummary(command) + "]",
	}
}

func rtkCommandAllowed(command string) bool {
	trimmed := strings.TrimSpace(command)
	return strings.HasPrefix(trimmed, "git status") || strings.HasPrefix(trimmed, "go test")
}

func rtkCommandSummary(command string) string {
	fields := strings.Fields(command)
	if len(fields) >= 2 {
		return fields[0] + " " + fields[1]
	}
	return strings.TrimSpace(command)
}

func resolveExecutable(binary string) (string, error) {
	if strings.ContainsRune(binary, os.PathSeparator) {
		if st, err := os.Stat(binary); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return binary, nil
		}
		return "", os.ErrNotExist
	}
	return exec.LookPath(binary)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func applyFilterMaxBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + fmt.Sprintf("\n[output filter truncated: %d bytes omitted]", len(s)-maxBytes)
}

var benchmarkNavigationPatterns = []struct {
	re      *regexp.Regexp
	message string
}{
	{
		re:      regexp.MustCompile(`(^|[;&|]\s*)(?:command\s+)?find(?:\s|$)`),
		message: "benchmark policy: shell find is disabled for filesystem exploration; use the find tool instead.",
	},
	{
		re:      regexp.MustCompile(`(^|[;&|]\s*)ls\s+-R(?:\s|$)`),
		message: "benchmark policy: recursive ls is disabled for filesystem exploration; use the ls or find tool instead.",
	},
}

func (t *BashTool) policyViolation(command string) string {
	if !strings.EqualFold(strings.TrimSpace(t.Mode), "benchmark") {
		return ""
	}
	trimmed := strings.TrimSpace(command)
	for _, pattern := range benchmarkNavigationPatterns {
		if pattern.re.MatchString(trimmed) {
			return pattern.message
		}
	}
	return ""
}
