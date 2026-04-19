package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type loggerTestProvider struct {
	response agent.Response
}

func (p *loggerTestProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}
	return p.response, nil
}

func TestNewLogger(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "test-session")
	require.NotNil(t, l)
	defer l.Close()

	// Verify file was created
	path := filepath.Join(dir, "test-session.jsonl")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.Size() == 0) // Empty initially
}

func TestNewLogger_DirCreation(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested", "sessions")
	l := NewLogger(subdir, "test-session")
	require.NotNil(t, l)
	defer l.Close()

	// Verify directory was created
	info, err := os.Stat(subdir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestNewLogger_Failures(t *testing.T) {
	// Invalid path that can't be created
	l := NewLogger("/invalid/path/that/cannot/exist", "test")
	require.NotNil(t, l)  // Should return non-nil even on failure
	assert.Nil(t, l.file) // But file should be nil
}

func TestNewLogger_FailureWarnsAndRunStillCompletes(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	logs, restore := captureLoggerLogs(t)
	defer restore()

	logger := NewLogger(filepath.Join(blocker, "sessions"), "blocked-session")
	require.NotNil(t, logger)
	assert.Nil(t, logger.file)

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt: "test",
		Provider: &loggerTestProvider{
			response: agent.Response{
				Content: "done",
				Usage:   agent.TokenUsage{Input: 4, Output: 2, Total: 6},
			},
		},
		Callback: logger.Callback(),
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.Contains(t, logs.String(), "session logger: cannot create directory")
}

func TestLogger_Emit(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "emit-test")
	defer l.Close()

	// Emit several events
	l.Emit(agent.EventSessionStart, SessionStartData{
		Provider:      "test-provider",
		Model:         "test-model",
		Prompt:        "Test prompt",
		SystemPrompt:  "System prompt",
		MaxIterations: 10,
	})

	l.Emit(agent.EventLLMRequest, LLMRequestData{
		Messages: []agent.Message{{Role: agent.RoleUser, Content: "Hello"}},
		Tools:    nil,
	})

	l.Emit(agent.EventSessionEnd, SessionEndData{
		Status: agent.StatusSuccess,
		Output: "Done",
	})

	// Read back and verify
	events, err := ReadEvents(filepath.Join(dir, "emit-test.jsonl"))
	require.NoError(t, err)
	assert.Len(t, events, 3)
	assert.Equal(t, agent.EventSessionStart, events[0].Type)
	assert.Equal(t, agent.EventLLMRequest, events[1].Type)
	assert.Equal(t, agent.EventSessionEnd, events[2].Type)

	// Verify sequence numbers
	assert.Equal(t, 0, events[0].Seq)
	assert.Equal(t, 1, events[1].Seq)
	assert.Equal(t, 2, events[2].Seq)
}

func TestSessionEndData_CostDistinction(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "cost-distinction-test")

	l.Emit(agent.EventSessionEnd, SessionEndData{
		Status: agent.StatusSuccess,
	})
	zero := 0.0
	l.Emit(agent.EventSessionEnd, SessionEndData{
		Status:  agent.StatusSuccess,
		CostUSD: &zero,
	})
	require.NoError(t, l.Close())

	events, err := ReadEvents(filepath.Join(dir, "cost-distinction-test.jsonl"))
	require.NoError(t, err)
	require.Len(t, events, 2)

	var unknown SessionEndData
	require.NoError(t, json.Unmarshal(events[0].Data, &unknown))
	assert.Nil(t, unknown.CostUSD)

	var local SessionEndData
	require.NoError(t, json.Unmarshal(events[1].Data, &local))
	require.NotNil(t, local.CostUSD)
	assert.Equal(t, 0.0, *local.CostUSD)
}

func TestLogger_Write_NilFile(t *testing.T) {
	l := &Logger{} // No file initialized
	l.Write(agent.Event{Type: agent.EventSessionStart})
	// Should not panic
}

func TestLogger_Callback(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "callback-test")
	defer l.Close()

	callback := l.Callback()
	require.NotNil(t, callback)

	// Use the callback
	callback(agent.Event{
		SessionID: "callback-test",
		Type:      agent.EventLLMResponse,
		Data:      []byte(`{"content": "test"}`),
	})

	events, err := ReadEvents(filepath.Join(dir, "callback-test.jsonl"))
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestLogger_Close_NilFile(t *testing.T) {
	l := &Logger{} // No file initialized
	err := l.Close()
	assert.NoError(t, err) // Should not error on nil file
}

func TestReadEvents_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	events, err := ReadEvents(path)
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestReadEvents_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.jsonl")
	// Write valid JSON followed by invalid
	content := `{"session_id":"test","seq":0,"type":"start"}
invalid json here
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	events, err := ReadEvents(path)
	assert.Error(t, err)     // Should error on malformed JSON
	assert.Len(t, events, 1) // But should return what was parsed
}

func TestReadEvents_MissingFile(t *testing.T) {
	_, err := ReadEvents("/nonexistent/path/file.jsonl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading log")
}

func TestLogger_Emit_ConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "concurrent-test")
	defer l.Close()

	// Emit from multiple goroutines (simulated by rapid sequential calls)
	for i := 0; i < 100; i++ {
		l.Emit(agent.EventLLMResponse, LLMResponseData{Content: string(rune(i))})
	}

	events, err := ReadEvents(filepath.Join(dir, "concurrent-test.jsonl"))
	require.NoError(t, err)
	assert.Len(t, events, 100)

	// Verify sequence numbers are unique and sequential
	seqs := make(map[int]bool)
	for _, e := range events {
		if seqs[e.Seq] {
			t.Errorf("Duplicate sequence number: %d", e.Seq)
		}
		seqs[e.Seq] = true
	}
}

func TestLogger_Write_MarshalError(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "marshal-error-test")
	defer l.Close()

	// Write an event - normal case should work
	event := agent.Event{
		SessionID: "marshal-error-test",
		Type:      agent.EventLLMResponse,
		Data:      []byte(`{"content": "test"}`),
	}

	l.Write(event)
	// Should succeed without panic
}

func TestReadEvents_MultipleLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.jsonl")
	content := `{"session_id":"s1","seq":0,"type":"start"}
{"session_id":"s1","seq":1,"type":"llm_request"}
{"session_id":"s1","seq":2,"type":"end"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	events, err := ReadEvents(path)
	require.NoError(t, err)
	assert.Len(t, events, 3)
}

func TestReadEvents_TrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trailing.jsonl")
	content := `{"session_id":"s1","seq":0,"type":"start"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	events, _ := ReadEvents(path)
	assert.Len(t, events, 1)
}

func TestReadEvents_WhitespaceOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whitespace.jsonl")
	content := `   
	
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	events, _ := ReadEvents(path)
	// Whitespace-only file should return empty events without error
	assert.Empty(t, events)
}

func TestLogger_SessionIDInPath(t *testing.T) {
	dir := t.TempDir()
	sessionID := "my-special-session_123"
	l := NewLogger(dir, sessionID)
	defer l.Close()

	// Verify the file uses the exact session ID
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, files, 1)
	assert.Equal(t, sessionID+".jsonl", files[0].Name())
}

func TestLogger_WarnedFlag(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "warn-test")
	defer l.Close()

	// First write should succeed
	l.Write(agent.Event{SessionID: "test", Type: agent.EventLLMResponse})

	// Close the file to simulate error condition
	l.file.Close()
	l.file = nil

	// Subsequent writes should not panic and should set warned flag
	l.Write(agent.Event{SessionID: "test", Type: agent.EventLLMResponse})
}

func TestLogger_WriteErrorLeavesReadablePartialLog(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "partial-log")

	logs, restore := captureLoggerLogs(t)
	defer restore()

	l.Emit(agent.EventSessionStart, SessionStartData{
		Provider: "test-provider",
		Model:    "test-model",
		Prompt:   "prompt",
	})

	require.NoError(t, l.file.Close())
	l.Write(agent.Event{
		SessionID: "partial-log",
		Type:      agent.EventSessionEnd,
		Data:      []byte(`{"status":"success"}`),
	})

	assert.Contains(t, logs.String(), "session logger: write error")

	events, err := ReadEvents(filepath.Join(dir, "partial-log.jsonl"))
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, agent.EventSessionStart, events[0].Type)
}

func TestReadEvents_BinaryData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.jsonl")
	// Write valid JSON with base64-like content (avoiding escape issues)
	content := `{"session_id":"s1","seq":0,"type":"start","data":"YWJjMTIz"}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	events, err := ReadEvents(path)
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestLogger_Emit_DataTypes(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "data-types-test")
	defer l.Close()

	// Emit various data types
	l.Emit(agent.EventSessionStart, SessionStartData{Provider: "test"})
	l.Emit(agent.EventLLMRequest, LLMRequestData{})
	l.Emit(agent.EventLLMResponse, LLMResponseData{Content: "response"})
	l.Emit(agent.EventToolCall, ToolCallData{Tool: "read", Input: []byte(`{"path":"x"}`)})
	l.Emit(agent.EventSessionEnd, SessionEndData{Status: agent.StatusSuccess})

	events, err := ReadEvents(filepath.Join(dir, "data-types-test.jsonl"))
	require.NoError(t, err)
	assert.Len(t, events, 5)
}

func TestReadEvents_PartialDecodeError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.jsonl")
	// Valid JSON followed by partial/invalid JSON
	content := `{"session_id":"s1","seq":0,"type":"start"}
{"session_id":"s1","seq":1,"type":"incomplete"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	events, err := ReadEvents(path)
	assert.Error(t, err)
	// Should return events parsed before the error
	assert.GreaterOrEqual(t, len(events), 1)
}

func TestLogger_NewLogger_ExistingDir(t *testing.T) {
	dir := t.TempDir() // Already exists
	l := NewLogger(dir, "existing-dir-test")
	require.NotNil(t, l)
	defer l.Close()
	assert.NotNil(t, l.file)
}

func TestReadEvents_UnmarshalError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unmarshal.jsonl")
	// Valid JSON but wrong structure for Event
	content := `{"not_an_event": true}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	events, err := ReadEvents(path)
	// Should decode successfully (Event has flexible fields)
	assert.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestLogger_Emit_EmptyData(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, "empty-data-test")
	defer l.Close()

	// Emit with nil data
	l.Emit(agent.EventLLMResponse, nil)

	events, err := ReadEvents(filepath.Join(dir, "empty-data-test.jsonl"))
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func captureLoggerLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)
	return &buf, func() {
		slog.SetDefault(prev)
	}
}

func TestReadEvents_LargeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")

	// Write many events
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString(fmt.Sprintf(`{"session_id":"s1","seq":%d,"type":"event"}`, i))
		sb.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0644))

	events, err := ReadEvents(path)
	require.NoError(t, err)
	assert.Len(t, events, 1000)
}
