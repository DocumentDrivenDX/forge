package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliRunResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runBuiltCLI(t *testing.T, exePath, workDir string, env []string, args ...string) cliRunResult {
	t.Helper()

	cmd := exec.Command(exePath, args...)
	cmd.Dir = workDir
	cmd.Env = env

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		require.True(t, ok, "expected ExitError, got %T: %v", err, err)
		exitCode = exitErr.ExitCode()
	}

	return cliRunResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

func runBuiltCLIAsync(t *testing.T, exePath, workDir string, env []string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	cmd := exec.Command(exePath, args...)
	cmd.Dir = workDir
	cmd.Env = env

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	require.NoError(t, cmd.Start())
	return cmd, &stdout, &stderr
}

func testEnvWithHome(home string, extra map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func writeGlobalConfig(t *testing.T, home, configBody string) {
	t.Helper()
	globalDir := filepath.Join(home, ".config", "agent")
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "config.yaml"), []byte(configBody), 0o644))
}

func newSlowOpenAIServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"stub-model"}]}`))
		case "/v1/chat/completions":
			select {
			case <-r.Context().Done():
				return
			case <-time.After(delay):
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-slow",
				"object":"chat.completion",
				"created":1712534400,
				"model":"stub-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"late"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

// newToolLoopStreamingServer returns a fake SSE streaming server that always
// responds with a bash tool call, forcing the agent to exhaust its iteration
// limit when max-iter is small.
func newToolLoopStreamingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"stub-model"}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Transfer-Encoding", "chunked")
			flusher, _ := w.(http.Flusher)
			// Emit SSE chunks that assemble a bash tool call.
			chunks := []string{
				`{"id":"c1","object":"chat.completion.chunk","created":1712534400,"model":"stub-model","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}`,
				`{"id":"c1","object":"chat.completion.chunk","created":1712534400,"model":"stub-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"echo hi\"}"}}]},"finish_reason":null}]}`,
				`{"id":"c1","object":"chat.completion.chunk","created":1712534400,"model":"stub-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			}
			for _, chunk := range chunks {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestCLI_IterationLimit_ExitsZero verifies that when the agent exhausts its
// iteration limit (StatusIterationLimit), the process exits with code 0.
// This is the benchmark harness case: the agent completes work within N turns
// and the harness should not see a NonZeroAgentExitCodeError.
func TestCLI_IterationLimit_ExitsZero(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()
	loop := newToolLoopStreamingServer(t)
	defer loop.Close()

	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+loop.URL+`/v1
    api_key: test
    model: stub-model
default: local
`)

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil),
		"--work-dir", workDir, "--max-iter", "1", "-p", "do something")
	assert.Equal(t, 0, res.exitCode, "iteration_limit should exit 0; stderr=%s", res.stderr)
	assert.Contains(t, res.stderr, "[iteration_limit]")
}

func TestCLI_Run_StrictStdoutStderrAndExitCode(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()
	fake := newFakeOpenAIServer(t)

	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
    model: gpt-4o
default: local
`)

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "-p", "hello")
	require.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)
	assert.NotContains(t, res.stdout, "[success] tokens:")
	assert.Contains(t, res.stderr, "[success] tokens:")
	assert.NotContains(t, res.stderr, "{")
}

func TestCLI_JSONOutput_IsMachineReadable(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()
	fake := newFakeOpenAIServer(t)

	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
    model: gpt-4o
default: local
`)

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--json", "--work-dir", workDir, "-p", "hello")
	require.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)
	assert.Contains(t, res.stderr, "[success] tokens:")

	var parsed struct {
		Status    string `json:"status"`
		Output    string `json:"output"`
		SessionID string `json:"session_id"`
		Tokens    struct {
			Input  int `json:"input"`
			Output int `json:"output"`
		} `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &parsed), "stdout=%s", res.stdout)
	assert.Equal(t, "success", parsed.Status)
	assert.NotEmpty(t, parsed.SessionID)
	assert.GreaterOrEqual(t, parsed.Tokens.Input, 0)
	assert.GreaterOrEqual(t, parsed.Tokens.Output, 0)
}

func TestCLI_ServiceContractSmoke(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()
	fake := newFakeOpenAIServer(t)

	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
    model: gpt-4o
default: local
`)

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, map[string]string{
		"DDX_AGENT_USE_SERVICE_CONTRACT": "1",
	}), "--json", "--work-dir", workDir, "-p", "hello")
	require.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)
	assert.Contains(t, res.stderr, "[success] tokens:")

	var parsed struct {
		Status    string `json:"status"`
		Output    string `json:"output"`
		SessionID string `json:"session_id"`
		Tokens    struct {
			Input  int `json:"input"`
			Output int `json:"output"`
		} `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &parsed), "stdout=%s", res.stdout)
	assert.Equal(t, "success", parsed.Status)
	assert.Equal(t, "stub ok", parsed.Output)
	assert.True(t, strings.HasPrefix(parsed.SessionID, "svc-"), "session_id=%q proves DdxAgent.Execute generated the run", parsed.SessionID)
	assert.Equal(t, "gpt-4o", fake.lastModel())
	assert.Equal(t, 10, parsed.Tokens.Input)
	assert.Equal(t, 2, parsed.Tokens.Output)
}

func TestCLI_UnknownSubcommand_NoPromptUsageExitCode(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "unknown-subcommand")
	require.Equal(t, 2, res.exitCode, "stderr=%s", res.stderr)
	assert.Contains(t, res.stderr, "error: no prompt provided")
	assert.Contains(t, res.stderr, "Usage of")
}

func TestCLI_ConfigPrecedence_GlobalProjectEnvAndFlagModel(t *testing.T) {
	exe := buildAgentCLI(t)
	home := t.TempDir()
	workDir := t.TempDir()

	globalFake := newFakeOpenAIServer(t)
	projectFake := newFakeOpenAIServer(t)
	envFake := newFakeOpenAIServer(t)

	writeGlobalConfig(t, home, `
providers:
  local:
    type: lmstudio
    base_url: `+globalFake.baseURL()+`
    api_key: test
    model: global-model
default: local
`)

	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+projectFake.baseURL()+`
    api_key: test
    model: project-model
default: local
`)

	env := testEnvWithHome(home, map[string]string{
		"AGENT_BASE_URL": envFake.baseURL(),
		"AGENT_MODEL":    "env-model",
	})

	first := runBuiltCLI(t, exe, workDir, env, "--work-dir", workDir, "-p", "first")
	require.Equal(t, 0, first.exitCode, "stderr=%s", first.stderr)
	assert.Equal(t, "env-model", envFake.lastModel())
	assert.Equal(t, "", projectFake.lastModel())
	assert.Equal(t, "", globalFake.lastModel())

	second := runBuiltCLI(t, exe, workDir, env, "--work-dir", workDir, "--model", "cli-model", "-p", "second")
	require.Equal(t, 0, second.exitCode, "stderr=%s", second.stderr)
	assert.Equal(t, "cli-model", envFake.lastModel(), "CLI --model should override env/config model")
}

func TestCLI_Providers_JSON_RedactsSecrets(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	writeTempConfig(t, workDir, `
providers:
  openrouter:
    type: lmstudio
    base_url: https://openrouter.ai/api/v1
    api_key: secret-key
    model: qwen/qwen3-coder-next
    headers:
      HTTP-Referer: https://example.com
      X-Title: DDX Agent
default: openrouter
`)

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "--json", "providers")
	require.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)

	var parsed map[string]struct {
		Type    string            `json:"type"`
		BaseURL string            `json:"base_url"`
		Model   string            `json:"model"`
		Headers map[string]string `json:"headers"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &parsed), "stdout=%s", res.stdout)
	require.Contains(t, parsed, "openrouter")
	assert.Equal(t, "lmstudio", parsed["openrouter"].Type)
	assert.Equal(t, "https://openrouter.ai/api/v1", parsed["openrouter"].BaseURL)
	assert.Equal(t, "qwen/qwen3-coder-next", parsed["openrouter"].Model)
	assert.Equal(t, "[redacted]", parsed["openrouter"].Headers["HTTP-Referer"])
	assert.Equal(t, "[redacted]", parsed["openrouter"].Headers["X-Title"])
	assert.NotContains(t, res.stdout, "secret-key")
	assert.NotContains(t, res.stdout, "https://example.com")
	assert.NotContains(t, res.stdout, "APIKey")
}

func TestCLI_Replay_NoArgs_UsageExitCode2(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "replay")
	require.Equal(t, 2, res.exitCode)
	assert.Contains(t, res.stderr, "usage: ddx-agent replay <session-id>")
	assert.Equal(t, "", res.stdout)
}

func TestCLI_Replay_UnknownSession_StrictError(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "replay", "does-not-exist")
	require.Equal(t, 1, res.exitCode)
	assert.Contains(t, res.stderr, "error:")
	assert.Contains(t, res.stderr, "does-not-exist")
	assert.Equal(t, "", res.stdout)
}

func TestCLI_Log_UnknownSession_StrictError(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "log", "does-not-exist")
	require.Equal(t, 1, res.exitCode)
	assert.Contains(t, res.stderr, "error:")
	assert.Contains(t, res.stderr, "does-not-exist")
	assert.Equal(t, "", res.stdout)
}

func TestCLI_CancelSignal_WritesSessionEndEvent(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()
	slow := newSlowOpenAIServer(t, 3*time.Second)
	defer slow.Close()

	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+slow.URL+`/v1
    api_key: test
    model: gpt-4o
default: local
session_log_dir: .agent/sessions
`)

	cmd, _, stderr := runBuiltCLIAsync(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "-p", "slow request")
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
	err := cmd.Wait()
	require.Error(t, err, "expected non-zero exit after interrupt")

	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected ExitError, got %T", err)
	assert.NotEqual(t, 0, exitErr.ExitCode())
	assert.Contains(t, stderr.String(), "[cancelled]")

	logs, globErr := filepath.Glob(filepath.Join(workDir, ".agent", "sessions", "*.jsonl"))
	require.NoError(t, globErr)
	require.Len(t, logs, 1, "expected one session log")

	events, readErr := session.ReadEvents(logs[0])
	require.NoError(t, readErr)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, agent.EventSessionEnd, last.Type)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(last.Data, &payload))
	assert.Equal(t, "cancelled", strings.ToLower(payload["status"].(string)))
}
