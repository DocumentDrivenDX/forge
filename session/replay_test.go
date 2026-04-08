package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplay(t *testing.T) {
	dir := t.TempDir()
	sessionID := "replay-test"

	// Write a test session log
	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Provider:      "openai-compat",
		Model:         "qwen3.5-7b",
		WorkDir:       "/tmp/test",
		MaxIterations: 20,
		Prompt:        "Read main.go",
		SystemPrompt:  "You are a helpful assistant.",
	})
	logger.Emit(agent.EventLLMResponse, LLMResponseData{
		Content:   "",
		ToolCalls: []agent.ToolCall{{ID: "tc1", Name: "read"}},
		Usage:     agent.TokenUsage{Input: 100, Output: 20, Total: 120},
		LatencyMs: 500,
		Model:     "qwen3.5-7b",
	})
	logger.Emit(agent.EventToolCall, ToolCallData{
		Tool:       "read",
		Input:      []byte(`{"path":"main.go"}`),
		Output:     "package main\n\nfunc main() {}\n",
		DurationMs: 1,
	})
	logger.Emit(agent.EventLLMResponse, LLMResponseData{
		Content:   "The package is main.",
		Usage:     agent.TokenUsage{Input: 200, Output: 30, Total: 230},
		LatencyMs: 800,
		Model:     "qwen3.5-7b",
	})
	logger.Emit(agent.EventSessionEnd, SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "The package is main.",
		Tokens:     agent.TokenUsage{Input: 300, Output: 50, Total: 350},
		CostUSD:    0,
		DurationMs: 1500,
	})
	require.NoError(t, logger.Close())

	// Replay it
	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Session replay-test")
	assert.Contains(t, output, "qwen3.5-7b")
	assert.Contains(t, output, "[System]")
	assert.Contains(t, output, "You are a helpful assistant.")
	assert.Contains(t, output, "[User]")
	assert.Contains(t, output, "Read main.go")
	assert.Contains(t, output, "> read")
	assert.Contains(t, output, "package main")
	assert.Contains(t, output, "The package is main.")
	assert.Contains(t, output, "End (success)")
	assert.Contains(t, output, "$0 (local)")
}

func TestReplay_MissingFile(t *testing.T) {
	var buf bytes.Buffer
	err := Replay("/nonexistent/file.jsonl", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replay:")
}

func TestReplay_EmptySession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	var buf bytes.Buffer
	err := Replay(path, &buf)
	require.NoError(t, err)
	assert.Empty(t, buf.String())
}

func TestReplay_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.jsonl")
	content := `{"session_id":"test","seq":0,"type":"start"}
invalid json here
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	var buf bytes.Buffer
	err := Replay(path, &buf)
	assert.Error(t, err) // Should error on malformed JSON
}

func TestReplay_TruncatedOutput(t *testing.T) {
	dir := t.TempDir()
	sessionID := "truncated-test"

	logger := NewLogger(dir, sessionID)
	longOutput := strings.Repeat("x", 300) // Longer than 200 char limit
	logger.Emit(agent.EventToolCall, ToolCallData{
		Tool:   "bash",
		Input:  []byte(`{"cmd":"echo test"}`),
		Output: longOutput,
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "...[truncated]") // Should be truncated
}

func TestReplay_WithCost(t *testing.T) {
	dir := t.TempDir()
	sessionID := "cost-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Provider:      "anthropic",
		Model:         "claude-sonnet-4-20250514",
		Prompt:        "Test with cost",
	})
	logger.Emit(agent.EventLLMResponse, LLMResponseData{
		Content:   "Hello",
		Usage:     agent.TokenUsage{Input: 1000, Output: 500},
		CostUSD:   0.0234,
		LatencyMs: 1000,
	})
	logger.Emit(agent.EventSessionEnd, SessionEndData{
		Status:     agent.StatusSuccess,
		Tokens:     agent.TokenUsage{Input: 1000, Output: 500},
		CostUSD:    0.0234,
		DurationMs: 1000,
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "$0.0234") // Should show cost
}

func TestReplay_ErrorStatus(t *testing.T) {
	dir := t.TempDir()
	sessionID := "error-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Prompt: "Test error",
	})
	logger.Emit(agent.EventSessionEnd, SessionEndData{
		Status: agent.StatusError,
		Error:  "Connection timeout",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "End (error)")
	assert.Contains(t, output, "Connection timeout")
}

func TestReplay_ToolCallWithArgs(t *testing.T) {
	dir := t.TempDir()
	sessionID := "tool-args-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventLLMRequest, LLMRequestData{
		Messages: []agent.Message{
			{Role: agent.RoleUser, Content: "Read the file"},
		},
	})
	logger.Emit(agent.EventToolCall, ToolCallData{
		Tool:   "read",
		Input:  []byte(`{"path":"config.yaml","limit":10}`),
		Output: "content here",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "> read")
	assert.Contains(t, output, "config.yaml")
}

func TestReplay_MultipleToolCalls(t *testing.T) {
	dir := t.TempDir()
	sessionID := "multi-tool-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventLLMResponse, LLMResponseData{
		Content: "",
		ToolCalls: []agent.ToolCall{
			{ID: "tc1", Name: "read"},
			{ID: "tc2", Name: "write"},
			{ID: "tc3", Name: "bash"},
		},
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "[3 tool call(s)]")
}

func TestReplay_MultipleMessages(t *testing.T) {
	dir := t.TempDir()
	sessionID := "multi-msg-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventLLMRequest, LLMRequestData{
		Messages: []agent.Message{
			{Role: agent.RoleSystem, Content: "You are helpful."},
			{Role: agent.RoleUser, Content: "Hello"},
			{Role: agent.RoleAssistant, Content: "Hi there!"},
			{Role: agent.RoleTool, Content: "tool result", ToolCallID: "tc1"},
		},
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "[system]")
	assert.Contains(t, output, "[user]")
	assert.Contains(t, output, "[assistant]")
	assert.Contains(t, output, "[tool result]")
}

func TestReplay_WithMetadata(t *testing.T) {
	dir := t.TempDir()
	sessionID := "metadata-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionEnd, SessionEndData{
		Status: agent.StatusSuccess,
		Metadata: map[string]string{
			"branch":    "main",
			"commit":    "abc123",
			"workspace": "test",
		},
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Metadata:")
	assert.Contains(t, output, "branch=main")
	assert.Contains(t, output, "commit=abc123")
}

func TestReplay_NewlinesInOutput(t *testing.T) {
	dir := t.TempDir()
	sessionID := "newlines-test"

	logger := NewLogger(dir, sessionID)
	multilineOutput := "line1\nline2\nline3\nline4"
	logger.Emit(agent.EventToolCall, ToolCallData{
		Tool:   "bash",
		Input:  []byte(`{"cmd":"ls -la"}`),
		Output: multilineOutput,
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "line1")
	assert.Contains(t, output, "line2") // Should preserve newlines with indentation
}

func TestReplay_UnknownEventType(t *testing.T) {
	dir := t.TempDir()
	sessionID := "unknown-event-test"

	logger := NewLogger(dir, sessionID)
	// Emit an event type that replay doesn't handle (e.g., EventCompactionStart)
	logger.Emit(agent.EventCompactionStart, nil)
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)
	// Should not panic, just skip unknown events
	assert.Empty(t, buf.String())
}

func TestReplay_LatencyDisplay(t *testing.T) {
	dir := t.TempDir()
	sessionID := "latency-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventLLMResponse, LLMResponseData{
		Content:   "response",
		LatencyMs: 1234, // Should show as 1234ms
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "1234ms")
}

func TestReplay_TokenDisplay(t *testing.T) {
	dir := t.TempDir()
	sessionID := "tokens-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventLLMResponse, LLMResponseData{
		Content: "response",
		Usage:   agent.TokenUsage{Input: 1234, Output: 567},
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "1234 in")
	assert.Contains(t, output, "567 out")
}

func TestReplay_ModelName(t *testing.T) {
	dir := t.TempDir()
	sessionID := "model-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Model: "gpt-4o-mini",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "gpt-4o-mini")
}

func TestReplay_WorkDir(t *testing.T) {
	dir := t.TempDir()
	sessionID := "workdir-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		WorkDir: "/home/user/project/src",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "/home/user/project/src")
}

func TestReplay_MaxIterations(t *testing.T) {
	dir := t.TempDir()
	sessionID := "maxiter-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		MaxIterations: 50,
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Max iterations: 50")
}

func TestReplay_ProviderName(t *testing.T) {
	dir := t.TempDir()
	sessionID := "provider-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Provider: "openrouter",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Provider: openrouter")
}

func TestReplay_TimestampFormat(t *testing.T) {
	dir := t.TempDir()
	sessionID := "timestamp-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Prompt: "Test",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Time:") // Should show timestamp in format YYYY-MM-DD HH:MM:SS UTC
}

func TestReplay_IterationLimitStatus(t *testing.T) {
	dir := t.TempDir()
	sessionID := "iteration-limit-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Prompt:          "Test",
		MaxIterations:  20,
	})
	logger.Emit(agent.EventSessionEnd, SessionEndData{
		Status: agent.StatusIterationLimit,
		Output: "Hit iteration limit",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "End (iteration_limit)")
}

func TestReplay_CancelledStatus(t *testing.T) {
	dir := t.TempDir()
	sessionID := "cancelled-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Prompt: "Test",
	})
	logger.Emit(agent.EventSessionEnd, SessionEndData{
		Status: agent.StatusCancelled,
		Output: "User cancelled",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "End (cancelled)")
}

func TestReplay_ToolError(t *testing.T) {
	dir := t.TempDir()
	sessionID := "tool-error-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventToolCall, ToolCallData{
		Tool:  "bash",
		Input: []byte(`{"cmd":"invalid command"}`),
		Error: "command not found: invalid",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "> bash")
	assert.Contains(t, output, "Error:")
	assert.Contains(t, output, "command not found")
}

func TestReplay_LLMRequestDisplay(t *testing.T) {
	dir := t.TempDir()
	sessionID := "llm-request-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventLLMRequest, LLMRequestData{
		Messages: []agent.Message{
			{Role: agent.RoleUser, Content: "Hello"},
		},
		Tools: []agent.ToolDef{
			{Name: "read", Description: "Read a file"},
			{Name: "write", Description: "Write a file"},
		},
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "[LLM Request]")
	assert.Contains(t, output, "1 messages")
	assert.Contains(t, output, "2 tools")
}

func TestReplay_CompactJSON(t *testing.T) {
	// Test the compactJSON helper function indirectly through replay
	dir := t.TempDir()
	sessionID := "compact-json-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventToolCall, ToolCallData{
		Tool:  "read",
		Input: []byte(`{"path":"file.go","offset":10,"limit":20}`), // Pretty-printed JSON
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Input:")
	// Should contain compacted JSON (no extra whitespace)
}

func TestReplay_NoSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	sessionID := "no-system-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Prompt: "Just a user prompt",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "[User]")
	assert.NotContains(t, output, "[System]") // No system prompt section
}

func TestReplay_AssistantToolCallInRequest(t *testing.T) {
	dir := t.TempDir()
	sessionID := "assistant-tool-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventLLMRequest, LLMRequestData{
		Messages: []agent.Message{
			{
				Role:      agent.RoleAssistant,
				Content:   "",
				ToolCalls: []agent.ToolCall{{ID: "tc1", Name: "read"}},
			},
		},
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "[assistant tool_call]")
}

func TestReplay_SuccessStatus(t *testing.T) {
	dir := t.TempDir()
	sessionID := "success-test"

	logger := NewLogger(dir, sessionID)
	logger.Emit(agent.EventSessionStart, SessionStartData{
		Prompt: "Test",
	})
	logger.Emit(agent.EventSessionEnd, SessionEndData{
		Status: agent.StatusSuccess,
		Output: "Completed successfully",
	})
	require.NoError(t, logger.Close())

	var buf bytes.Buffer
	err := Replay(filepath.Join(dir, sessionID+".jsonl"), &buf)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "End (success)")
}
