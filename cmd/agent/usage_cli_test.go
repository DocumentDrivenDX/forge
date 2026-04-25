package main_test

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	buildCLIOnce sync.Once
	buildCLIPath string
	buildCLIErr  error
)

func seedMixedUsageLogs(t *testing.T, logDir string) {
	t.Helper()

	writeUsageLog := func(t *testing.T, sessionID string, startAt, endAt time.Time, start agent.SessionStartData, end agent.SessionEndData) {
		t.Helper()

		logger := agent.NewSessionLogger(logDir, sessionID)
		startEvent := agent.NewSessionEvent(sessionID, 0, agent.EventSessionStart, start)
		startEvent.Timestamp = startAt
		logger.Write(startEvent)

		endEvent := agent.NewSessionEvent(sessionID, 1, agent.EventSessionEnd, end)
		endEvent.Timestamp = endAt
		logger.Write(endEvent)

		require.NoError(t, logger.Close())
	}

	now := time.Now().UTC()
	recentDay := now.AddDate(0, 0, -2).Truncate(24 * time.Hour)
	oldDay := now.AddDate(0, 0, -30).Truncate(24 * time.Hour)

	writeUsageLog(t, "recent-known", recentDay.Add(10*time.Hour), recentDay.Add(10*time.Hour+time.Second), agent.SessionStartData{
		Provider: "lmstudio",
		Model:    "qwen3.5-7b",
		Prompt:   "recent known",
	}, agent.SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 10, Output: 5, Total: 15},
		CostUSD:    usageFloat64Ptr(0.25),
		DurationMs: 1000,
		Model:      "qwen3.5-7b",
	})

	writeUsageLog(t, "recent-unknown", recentDay.Add(11*time.Hour), recentDay.Add(11*time.Hour+2*time.Second), agent.SessionStartData{
		Provider: "lmstudio",
		Model:    "qwen3.5-7b",
		Prompt:   "recent unknown",
	}, agent.SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 20, Output: 10, Total: 30},
		CostUSD:    usageFloat64Ptr(-1),
		DurationMs: 2000,
		Model:      "qwen3.5-7b",
	})

	writeUsageLog(t, "old-session", oldDay.Add(9*time.Hour), oldDay.Add(9*time.Hour+3*time.Second), agent.SessionStartData{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
		Prompt:   "old",
	}, agent.SessionEndData{
		Status:     agent.StatusSuccess,
		Output:     "ok",
		Tokens:     agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:    usageFloat64Ptr(0.5),
		DurationMs: 3000,
		Model:      "claude-sonnet-4-20250514",
	})
}

func TestCLI_Usage(t *testing.T) {
	workDir := t.TempDir()
	logDir := filepath.Join(workDir, ".agent", "sessions")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	seedMixedUsageLogs(t, logDir)

	out, err := runAgentCLI(t, "--work-dir", workDir, "usage", "--since=7d")
	require.NoError(t, err, string(out))

	output := string(out)
	assert.Contains(t, output, "PROVIDER")
	assert.Contains(t, output, "TOTAL")
	assert.Contains(t, output, "lmstudio")
	assert.Contains(t, output, "qwen3.5-7b")
	assert.Contains(t, output, "Window:")
	assert.Contains(t, output, "unknown")
	assert.NotContains(t, output, "$0.2500")
}

func TestCLI_Usage_JSON_MixedCost(t *testing.T) {
	workDir := t.TempDir()
	logDir := filepath.Join(workDir, ".agent", "sessions")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	seedMixedUsageLogs(t, logDir)

	out, err := runAgentCLI(t, "--work-dir", workDir, "usage", "--since=7d", "--json")
	require.NoError(t, err, string(out))

	var report struct {
		Rows []struct {
			Provider            string   `json:"provider"`
			Model               string   `json:"model"`
			KnownCostUSD        *float64 `json:"known_cost_usd"`
			UnknownCostSessions int      `json:"unknown_cost_sessions"`
		} `json:"rows"`
		Totals struct {
			KnownCostUSD        *float64 `json:"known_cost_usd"`
			UnknownCostSessions int      `json:"unknown_cost_sessions"`
		} `json:"totals"`
	}
	require.NoError(t, json.Unmarshal(out, &report))

	require.Len(t, report.Rows, 1)
	assert.Equal(t, "lmstudio", report.Rows[0].Provider)
	assert.Equal(t, "qwen3.5-7b", report.Rows[0].Model)
	assert.Nil(t, report.Rows[0].KnownCostUSD)
	assert.Equal(t, 1, report.Rows[0].UnknownCostSessions)
	assert.Nil(t, report.Totals.KnownCostUSD)
	assert.Equal(t, 1, report.Totals.UnknownCostSessions)
}

func TestCLI_Usage_CSV_MixedCost(t *testing.T) {
	workDir := t.TempDir()
	logDir := filepath.Join(workDir, ".agent", "sessions")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	seedMixedUsageLogs(t, logDir)

	out, err := runAgentCLI(t, "--work-dir", workDir, "usage", "--since=7d", "--csv")
	require.NoError(t, err, string(out))

	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 3)

	assert.Equal(t, []string{
		"provider",
		"model",
		"sessions",
		"success_sessions",
		"failed_sessions",
		"input_tokens",
		"output_tokens",
		"total_tokens",
		"duration_ms",
		"known_cost_usd",
		"unknown_cost_sessions",
		"success_rate",
		"cost_per_success",
		"input_tokens_per_second",
		"output_tokens_per_second",
		"cache_read_tokens",
		"cache_write_tokens",
	}, rows[0])

	assert.Equal(t, "lmstudio", rows[1][0])
	assert.Equal(t, "qwen3.5-7b", rows[1][1])
	assert.Equal(t, "2", rows[1][2])
	assert.Equal(t, "1", rows[1][10]) // unknown_cost_sessions now at index 10
	assert.Equal(t, "", rows[1][9], "known cost must remain blank when unknown-cost sessions exist")

	assert.Equal(t, "TOTAL", rows[2][0])
	assert.Equal(t, "1", rows[2][10])
	assert.Equal(t, "", rows[2][9], "total known cost must remain blank when unknown-cost sessions exist")
}

func TestCLI_Usage_MutuallyExclusiveJSONAndCSV(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	cmd := exec.Command(exe, "--work-dir", workDir, "usage", "--json", "--csv")
	home := t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))

	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected process exit error, got %T: %v", err, err)
	assert.Equal(t, 2, exitErr.ExitCode())
	assert.Contains(t, string(out), "choose only one of --json or --csv")
}

func TestCLI_Usage_InvalidSince_ExitCode(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	cmd := exec.Command(exe, "--work-dir", workDir, "usage", "--since=bad-window")
	home := t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))

	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected process exit error, got %T: %v", err, err)
	assert.Equal(t, 2, exitErr.ExitCode())
	assert.Contains(t, string(out), "invalid time window")
}

func buildAgentCLI(t *testing.T) string {
	t.Helper()

	buildCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ddx-agent-cli-*")
		if err != nil {
			buildCLIErr = err
			return
		}
		exe := filepath.Join(dir, "ddx-agent")
		wd, err := os.Getwd()
		if err != nil {
			buildCLIErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", exe, "./cmd/agent")
		cmd.Dir = filepath.Clean(filepath.Join(wd, "..", ".."))
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildCLIErr = fmt.Errorf("build ddx-agent CLI: %w\n%s", err, strings.TrimSpace(string(out)))
			return
		}
		buildCLIPath = exe
	})
	require.NoError(t, buildCLIErr)
	return buildCLIPath
}

func usageFloat64Ptr(v float64) *float64 {
	return &v
}
