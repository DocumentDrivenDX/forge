package session

import (
	"path/filepath"
	"testing"

	"github.com/anthropics/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogger_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	sessionID := "test-session-001"

	logger := NewLogger(dir, sessionID)
	require.NotNil(t, logger)

	// Emit a session.start event
	logger.Emit(forge.EventSessionStart, SessionStartData{
		Provider:      "openai-compat",
		Model:         "qwen3.5-7b",
		WorkDir:       "/tmp/test",
		MaxIterations: 20,
		Prompt:        "Read main.go",
	})

	// Emit an llm.response event
	logger.Emit(forge.EventLLMResponse, LLMResponseData{
		Content:   "I'll read that file for you.",
		Usage:     forge.TokenUsage{Input: 100, Output: 20, Total: 120},
		CostUSD:   0,
		LatencyMs: 500,
		Model:     "qwen3.5-7b",
	})

	// Emit a session.end event
	logger.Emit(forge.EventSessionEnd, SessionEndData{
		Status:     forge.StatusSuccess,
		Output:     "Done.",
		Tokens:     forge.TokenUsage{Input: 200, Output: 50, Total: 250},
		CostUSD:    0,
		DurationMs: 1500,
	})

	require.NoError(t, logger.Close())

	// Read events back
	logPath := filepath.Join(dir, sessionID+".jsonl")
	events, err := ReadEvents(logPath)
	require.NoError(t, err)
	require.Len(t, events, 3)

	assert.Equal(t, forge.EventSessionStart, events[0].Type)
	assert.Equal(t, sessionID, events[0].SessionID)
	assert.Equal(t, 0, events[0].Seq)

	assert.Equal(t, forge.EventLLMResponse, events[1].Type)
	assert.Equal(t, 1, events[1].Seq)

	assert.Equal(t, forge.EventSessionEnd, events[2].Type)
	assert.Equal(t, 2, events[2].Seq)
}

func TestLogger_UnwritableDir(t *testing.T) {
	// Logger should not panic when dir is unwritable
	logger := NewLogger("/nonexistent/path/that/cannot/exist", "test")
	require.NotNil(t, logger)

	// Should silently skip writes
	logger.Emit(forge.EventSessionStart, SessionStartData{Prompt: "test"})
	require.NoError(t, logger.Close())
}

func TestLogger_Callback(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(dir, "callback-test")

	cb := logger.Callback()
	require.NotNil(t, cb)

	cb(NewEvent("callback-test", 0, forge.EventSessionStart, SessionStartData{Prompt: "test"}))
	require.NoError(t, logger.Close())

	events, err := ReadEvents(filepath.Join(dir, "callback-test.jsonl"))
	require.NoError(t, err)
	require.Len(t, events, 1)
}
