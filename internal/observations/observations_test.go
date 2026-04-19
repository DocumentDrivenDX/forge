package observations_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/observations"
	"github.com/DocumentDrivenDX/agent/internal/session"
	"github.com/stretchr/testify/require"
)

// TestRing_AddAndMean verifies ring buffer add and mean behavior including overwrite.
func TestRing_AddAndMean(t *testing.T) {
	r := observations.NewRing(3)

	// Empty ring: mean should be not-ok.
	_, ok := r.Mean()
	require.False(t, ok, "empty ring should return ok=false")

	// Add samples below capacity.
	r.Add(observations.Sample{OutputTokensPerSec: 10})
	r.Add(observations.Sample{OutputTokensPerSec: 20})
	mean, ok := r.Mean()
	require.True(t, ok)
	require.InDelta(t, 15.0, mean, 0.001)

	// Fill to capacity.
	r.Add(observations.Sample{OutputTokensPerSec: 30})
	mean, ok = r.Mean()
	require.True(t, ok)
	require.InDelta(t, 20.0, mean, 0.001) // (10+20+30)/3

	// Add past capacity — oldest (10) should be overwritten by 40.
	r.Add(observations.Sample{OutputTokensPerSec: 40})
	mean, ok = r.Mean()
	require.True(t, ok)
	require.InDelta(t, 30.0, mean, 0.001) // (20+30+40)/3

	// Verify sample count stayed at capacity.
	require.Len(t, r.Samples, 3)
}

// TestStore_AddAndMeanSpeed verifies multi-key store behavior.
func TestStore_AddAndMeanSpeed(t *testing.T) {
	store := observations.NewStore()

	keyA := observations.Key{ProviderSystem: "anthropic", Model: "claude-3-opus"}
	keyB := observations.Key{ProviderSystem: "openai", Model: "gpt-4o"}

	store.Add(keyA, observations.Sample{OutputTokensPerSec: 50})
	store.Add(keyA, observations.Sample{OutputTokensPerSec: 100})
	store.Add(keyB, observations.Sample{OutputTokensPerSec: 80})

	meanA, ok := store.MeanSpeed(keyA)
	require.True(t, ok)
	require.InDelta(t, 75.0, meanA, 0.001)

	meanB, ok := store.MeanSpeed(keyB)
	require.True(t, ok)
	require.InDelta(t, 80.0, meanB, 0.001)

	// Unknown key should return ok=false.
	_, ok = store.MeanSpeed(observations.Key{ProviderSystem: "unknown", Model: "none"})
	require.False(t, ok)
}

// TestLoadSaveRoundtrip verifies YAML persistence.
func TestLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observations.yaml")

	store := observations.NewStore()
	key := observations.Key{ProviderSystem: "anthropic", Model: "claude-3-haiku"}
	store.Add(key, observations.Sample{
		OutputTokensPerSec: 123.456,
		DurationMs:         5000,
		RecordedAt:         time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
	})
	store.Add(key, observations.Sample{
		OutputTokensPerSec: 200.0,
		DurationMs:         3000,
		RecordedAt:         time.Date(2025, 1, 16, 12, 0, 0, 0, time.UTC),
	})

	require.NoError(t, store.Save(path))

	loaded, err := observations.LoadStore(path)
	require.NoError(t, err)

	mean, ok := loaded.MeanSpeed(key)
	require.True(t, ok)
	require.InDelta(t, (123.456+200.0)/2, mean, 0.001)
}

// TestLoadStore_NotFound verifies that a missing file returns an empty store.
func TestLoadStore_NotFound(t *testing.T) {
	store, err := observations.LoadStore("/nonexistent/path/observations.yaml")
	require.NoError(t, err)
	require.NotNil(t, store)
	_, ok := store.MeanSpeed(observations.Key{ProviderSystem: "x", Model: "y"})
	require.False(t, ok)
}

// TestPopulateFromLogs verifies the JSONL pipeline.
func TestPopulateFromLogs(t *testing.T) {
	dir := t.TempDir()

	// Write a minimal JSONL session file with a session.end event.
	sessionID := "test-session-001"
	logPath := filepath.Join(dir, sessionID+".jsonl")

	costUSD := 0.01
	endData := session.SessionEndData{
		Status:           agent.StatusSuccess,
		SelectedProvider: "anthropic",
		ResolvedModel:    "claude-3-haiku-20240307",
		DurationMs:       4000,
		Tokens: agent.TokenUsage{
			Input:  100,
			Output: 800,
		},
		CostUSD: &costUSD,
	}
	endDataBytes, err := json.Marshal(endData)
	require.NoError(t, err)

	endEvent := agent.Event{
		SessionID: sessionID,
		Seq:       1,
		Type:      agent.EventSessionEnd,
		Timestamp: time.Now().UTC(),
		Data:      endDataBytes,
	}
	eventBytes, err := json.Marshal(endEvent)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(logPath, append(eventBytes, '\n'), 0o600))

	store := observations.NewStore()
	require.NoError(t, observations.PopulateFromLogs(store, dir))

	key := observations.Key{ProviderSystem: "anthropic", Model: "claude-3-haiku-20240307"}
	mean, ok := store.MeanSpeed(key)
	require.True(t, ok)
	// 800 tokens / 4 seconds = 200 tokens/sec
	require.InDelta(t, 200.0, mean, 0.001)
}

// TestPopulateFromLogs_SkipsNonSuccess verifies that non-success sessions are ignored.
func TestPopulateFromLogs_SkipsNonSuccess(t *testing.T) {
	dir := t.TempDir()

	sessionID := "fail-session"
	logPath := filepath.Join(dir, sessionID+".jsonl")

	endData := session.SessionEndData{
		Status:           agent.StatusError,
		SelectedProvider: "anthropic",
		ResolvedModel:    "claude-3-haiku-20240307",
		DurationMs:       1000,
		Tokens:           agent.TokenUsage{Input: 50, Output: 100},
	}
	endDataBytes, _ := json.Marshal(endData)
	endEvent := agent.Event{
		SessionID: sessionID,
		Seq:       0,
		Type:      agent.EventSessionEnd,
		Timestamp: time.Now().UTC(),
		Data:      endDataBytes,
	}
	eventBytes, _ := json.Marshal(endEvent)
	require.NoError(t, os.WriteFile(logPath, append(eventBytes, '\n'), 0o600))

	store := observations.NewStore()
	require.NoError(t, observations.PopulateFromLogs(store, dir))

	_, ok := store.MeanSpeed(observations.Key{ProviderSystem: "anthropic", Model: "claude-3-haiku-20240307"})
	require.False(t, ok)
}

// TestPopulateFromLogs_EmptyDir verifies no error on empty log dir.
func TestPopulateFromLogs_EmptyDir(t *testing.T) {
	store := observations.NewStore()
	require.NoError(t, observations.PopulateFromLogs(store, t.TempDir()))
}

// TestPopulateFromLogs_NonexistentDir verifies no error on missing log dir.
func TestPopulateFromLogs_NonexistentDir(t *testing.T) {
	store := observations.NewStore()
	require.NoError(t, observations.PopulateFromLogs(store, "/nonexistent/log/dir"))
}
