package main_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLI_Usage(t *testing.T) {
	workDir := t.TempDir()
	logDir := filepath.Join(workDir, ".agent", "sessions")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	writeUsageLog := func(t *testing.T, sessionID string, startAt, endAt time.Time, start session.SessionStartData, end session.SessionEndData) {
		t.Helper()

		logger := session.NewLogger(logDir, sessionID)
		startEvent := session.NewEvent(sessionID, 0, agent.EventSessionStart, start)
		startEvent.Timestamp = startAt
		logger.Write(startEvent)

		endEvent := session.NewEvent(sessionID, 1, agent.EventSessionEnd, end)
		endEvent.Timestamp = endAt
		logger.Write(endEvent)

		require.NoError(t, logger.Close())
	}

	writeUsageLog(t, "recent-known", time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC), time.Date(2026, 4, 8, 10, 0, 1, 0, time.UTC), session.SessionStartData{
		Provider: "openai-compat",
		Model:    "qwen3.5-7b",
		Prompt:   "recent known",
	}, session.SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 10, Output: 5, Total: 15},
		CostUSD:    usageFloat64Ptr(0),
		DurationMs: 1000,
		Model:      "qwen3.5-7b",
	})

	writeUsageLog(t, "recent-unknown", time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC), time.Date(2026, 4, 8, 11, 0, 2, 0, time.UTC), session.SessionStartData{
		Provider: "openai-compat",
		Model:    "qwen3.5-7b",
		Prompt:   "recent unknown",
	}, session.SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 20, Output: 10, Total: 30},
		CostUSD:    usageFloat64Ptr(-1),
		DurationMs: 2000,
		Model:      "qwen3.5-7b",
	})

	writeUsageLog(t, "old-session", time.Date(2026, 3, 25, 9, 0, 0, 0, time.UTC), time.Date(2026, 3, 25, 9, 0, 3, 0, time.UTC), session.SessionStartData{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
		Prompt:   "old",
	}, session.SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:    usageFloat64Ptr(0.5),
		DurationMs: 3000,
		Model:      "claude-sonnet-4-20250514",
	})

	out, err := runAgentCLI(t, "--work-dir", workDir, "usage", "--since=7d")
	require.NoError(t, err, string(out))

	output := string(out)
	assert.Contains(t, output, "PROVIDER")
	assert.Contains(t, output, "TOTAL")
	assert.Contains(t, output, "openai-compat")
	assert.Contains(t, output, "qwen3.5-7b")
	assert.Contains(t, output, "Window:")
}

func usageFloat64Ptr(v float64) *float64 {
	return &v
}
