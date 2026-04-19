package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUsageWindow(t *testing.T) {
	now := time.Date(2026, 4, 9, 15, 30, 0, 0, time.UTC)

	t.Run("today", func(t *testing.T) {
		window, err := ParseUsageWindow("today", now)
		require.NoError(t, err)
		require.NotNil(t, window)
		assert.Equal(t, time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC), window.Start)
		assert.Equal(t, time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC), window.End)
	})

	t.Run("relative days", func(t *testing.T) {
		window, err := ParseUsageWindow("7d", now)
		require.NoError(t, err)
		require.NotNil(t, window)
		assert.Equal(t, now.Add(-7*24*time.Hour), window.Start)
		assert.Equal(t, now, window.End)
	})

	t.Run("date range", func(t *testing.T) {
		window, err := ParseUsageWindow("2026-04-01..2026-04-03", now)
		require.NoError(t, err)
		require.NotNil(t, window)
		assert.Equal(t, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), window.Start)
		assert.Equal(t, time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC), window.End)
	})

	t.Run("single date", func(t *testing.T) {
		window, err := ParseUsageWindow("2026-04-09", now)
		require.NoError(t, err)
		require.NotNil(t, window)
		assert.Equal(t, time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC), window.Start)
		assert.Equal(t, time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC), window.End)
	})
}

func TestAggregateUsage(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	writeSessionLog := func(t *testing.T, sessionID string, startAt, endAt time.Time, start SessionStartData, end SessionEndData) {
		t.Helper()

		logger := NewLogger(dir, sessionID)
		startEvent := NewEvent(sessionID, 0, agent.EventSessionStart, start)
		startEvent.Timestamp = startAt
		logger.Write(startEvent)

		endEvent := NewEvent(sessionID, 1, agent.EventSessionEnd, end)
		endEvent.Timestamp = endAt
		logger.Write(endEvent)

		require.NoError(t, logger.Close())
	}

	writeSessionLog(t, "recent-known", time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC), time.Date(2026, 4, 8, 10, 0, 1, 0, time.UTC), SessionStartData{
		Provider: "openai-compat",
		Model:    "qwen3.5-7b",
		Prompt:   "recent known",
	}, SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 10, Output: 5, Total: 15},
		CostUSD:    usageFloat64Ptr(0.25),
		DurationMs: 1000,
		Model:      "qwen3.5-7b",
	})

	writeSessionLog(t, "recent-unknown", time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC), time.Date(2026, 4, 8, 11, 0, 2, 0, time.UTC), SessionStartData{
		Provider: "openai-compat",
		Model:    "qwen3.5-7b",
		Prompt:   "recent unknown",
	}, SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 20, Output: 10, Total: 30},
		CostUSD:    usageFloat64Ptr(-1),
		DurationMs: 2000,
		Model:      "qwen3.5-7b",
	})

	writeSessionLog(t, "recent-known-only", time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC), time.Date(2026, 4, 8, 12, 0, 3, 0, time.UTC), SessionStartData{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
		Prompt:   "recent known only",
	}, SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:    usageFloat64Ptr(0.5),
		DurationMs: 3000,
		Model:      "claude-sonnet-4-20250514",
	})

	writeSessionLog(t, "old-session", time.Date(2026, 3, 25, 9, 0, 0, 0, time.UTC), time.Date(2026, 3, 25, 9, 0, 3, 0, time.UTC), SessionStartData{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
		Prompt:   "old",
	}, SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:    usageFloat64Ptr(1.0),
		DurationMs: 3000,
		Model:      "claude-sonnet-4-20250514",
	})

	report, err := AggregateUsage(dir, UsageOptions{Since: "7d", Now: now})
	require.NoError(t, err)
	require.NotNil(t, report)
	require.NotNil(t, report.Window)

	assert.Len(t, report.Rows, 2)

	var mixedRow, knownRow *UsageRow
	for i := range report.Rows {
		row := &report.Rows[i]
		switch {
		case row.Provider == "openai-compat" && row.Model == "qwen3.5-7b":
			mixedRow = row
		case row.Provider == "anthropic" && row.Model == "claude-sonnet-4-20250514":
			knownRow = row
		}
	}

	require.NotNil(t, mixedRow)
	assert.Equal(t, 2, mixedRow.Sessions)
	assert.Equal(t, 30, mixedRow.InputTokens)
	assert.Equal(t, 15, mixedRow.OutputTokens)
	assert.Equal(t, 45, mixedRow.TotalTokens)
	assert.Equal(t, int64(3000), mixedRow.DurationMs)
	assert.Nil(t, mixedRow.KnownCostUSD)
	assert.Equal(t, 1, mixedRow.UnknownCostSessions)
	assert.InDelta(t, 10.0, mixedRow.InputTokensPerSecond(), 0.01)
	assert.InDelta(t, 5.0, mixedRow.OutputTokensPerSecond(), 0.01)

	require.NotNil(t, knownRow)
	assert.Equal(t, 1, knownRow.Sessions)
	assert.Equal(t, 100, knownRow.InputTokens)
	assert.Equal(t, 50, knownRow.OutputTokens)
	assert.Equal(t, 150, knownRow.TotalTokens)
	assert.Equal(t, int64(3000), knownRow.DurationMs)
	require.NotNil(t, knownRow.KnownCostUSD)
	assert.InDelta(t, 0.5, *knownRow.KnownCostUSD, 0.0001)
	assert.Equal(t, 0, knownRow.UnknownCostSessions)

	assert.Equal(t, 3, report.Totals.Sessions)
	assert.Equal(t, 130, report.Totals.InputTokens)
	assert.Equal(t, 65, report.Totals.OutputTokens)
	assert.Equal(t, 195, report.Totals.TotalTokens)
	assert.Equal(t, int64(6000), report.Totals.DurationMs)
	assert.Nil(t, report.Totals.KnownCostUSD)
	assert.Equal(t, 1, report.Totals.UnknownCostSessions)
	assert.True(t, report.Window.Contains(time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)))
	assert.False(t, report.Window.Contains(time.Date(2026, 3, 25, 9, 0, 0, 0, time.UTC)))

	empty, err := AggregateUsage(filepath.Join(dir, "missing"), UsageOptions{})
	require.NoError(t, err)
	require.NotNil(t, empty)
	assert.Empty(t, empty.Rows)
	assert.Equal(t, 0, empty.Totals.Sessions)
}

func TestUsageRowSuccessTracking(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	writeSession := func(t *testing.T, id string, startAt time.Time, status agent.Status, costUSD *float64) {
		t.Helper()
		logger := NewLogger(dir, id)
		startEvent := NewEvent(id, 0, agent.EventSessionStart, SessionStartData{
			Provider: "test-provider",
			Model:    "test-model",
			Prompt:   "test",
		})
		startEvent.Timestamp = startAt
		logger.Write(startEvent)
		endEvent := NewEvent(id, 1, agent.EventSessionEnd, SessionEndData{
			Status:     status,
			Tokens:     agent.TokenUsage{Input: 10, Output: 5, Total: 15},
			CostUSD:    costUSD,
			DurationMs: 1000,
			Model:      "test-model",
		})
		endEvent.Timestamp = startAt.Add(time.Second)
		logger.Write(endEvent)
		require.NoError(t, logger.Close())
	}

	base := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	writeSession(t, "s1", base, agent.StatusSuccess, usageFloat64Ptr(0.20))
	writeSession(t, "s2", base.Add(time.Minute), agent.StatusSuccess, usageFloat64Ptr(0.40))
	writeSession(t, "s3", base.Add(2*time.Minute), agent.StatusError, usageFloat64Ptr(0.10))

	report, err := AggregateUsage(dir, UsageOptions{Now: now})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	row := report.Rows[0]
	assert.Equal(t, 3, row.Sessions)
	assert.Equal(t, 2, row.SuccessSessions)
	assert.Equal(t, 1, row.FailedSessions)
	assert.InDelta(t, 2.0/3.0, row.SuccessRate(), 0.0001)

	cps := row.CostPerSuccess()
	require.NotNil(t, cps)
	// known_cost = 0.70, success_sessions = 2, so cost_per_success = 0.35
	assert.InDelta(t, 0.35, *cps, 0.0001)

	// Zero sessions case
	var empty UsageRow
	assert.Equal(t, 0.0, empty.SuccessRate())
	assert.Nil(t, empty.CostPerSuccess())
}

func usageFloat64Ptr(v float64) *float64 {
	return &v
}
