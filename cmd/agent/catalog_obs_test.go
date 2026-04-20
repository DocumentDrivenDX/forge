package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogObservations_NoFile verifies that when no observations file exists
// the command prints "no observations recorded yet" and exits 0.
func TestCatalogObservations_NoFile(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	// Point DDX_OBSERVATIONS_FILE at a path that does not exist.
	obsFile := filepath.Join(t.TempDir(), "observations.yaml")

	exe := buildAgentCLI(t)
	res := runBuiltCLI(t, exe, workDir,
		testEnvWithHome(home, map[string]string{
			"DDX_OBSERVATIONS_FILE": obsFile,
		}),
		"--work-dir", workDir, "catalog", "observations",
	)
	assert.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)
	assert.Contains(t, res.stdout, "no observations recorded yet")
}

// TestCatalogObservations_WithData writes a minimal observations YAML to a temp
// path and verifies the tabular output contains the expected rows.
func TestCatalogObservations_WithData(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()

	obsFile := filepath.Join(t.TempDir(), "observations.yaml")
	// Use the serialization format from observations.Store.MarshalYAML:
	// rings is a list of {provider_system, model, ring: {capacity, samples:[{output_tokens_per_sec,...}]}}
	obsYAML := `rings:
  - provider_system: anthropic
    model: claude-haiku-4-5
    ring:
      capacity: 200
      samples:
        - output_tokens_per_sec: 24.3
          duration_ms: 1000
          recorded_at: 2026-01-01T00:00:00Z
        - output_tokens_per_sec: 25.0
          duration_ms: 1000
          recorded_at: 2026-01-01T00:01:00Z
  - provider_system: anthropic
    model: claude-sonnet-4-5
    ring:
      capacity: 200
      samples:
        - output_tokens_per_sec: 18.7
          duration_ms: 1000
          recorded_at: 2026-01-01T00:00:00Z
`
	require.NoError(t, os.WriteFile(obsFile, []byte(obsYAML), 0o644))

	exe := buildAgentCLI(t)

	// Default (tabular) output.
	res := runBuiltCLI(t, exe, workDir,
		testEnvWithHome(home, map[string]string{
			"DDX_OBSERVATIONS_FILE": obsFile,
		}),
		"--work-dir", workDir, "catalog", "observations",
	)
	require.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)
	assert.Contains(t, res.stdout, "PROVIDER")
	assert.Contains(t, res.stdout, "MODEL")
	assert.Contains(t, res.stdout, "anthropic")
	assert.Contains(t, res.stdout, "claude-haiku-4-5")
	assert.Contains(t, res.stdout, "claude-sonnet-4-5")

	// JSON output.
	resJSON := runBuiltCLI(t, exe, workDir,
		testEnvWithHome(home, map[string]string{
			"DDX_OBSERVATIONS_FILE": obsFile,
		}),
		"--work-dir", workDir, "catalog", "observations", "--format", "json",
	)
	require.Equal(t, 0, resJSON.exitCode, "stderr=%s", resJSON.stderr)
	var rows []struct {
		ProviderSystem        string  `json:"provider_system"`
		Model                 string  `json:"model"`
		Samples               int     `json:"samples"`
		AvgOutputTokensPerSec float64 `json:"avg_output_tokens_per_sec"`
	}
	require.NoError(t, json.Unmarshal([]byte(resJSON.stdout), &rows), "stdout=%s", resJSON.stdout)
	require.Len(t, rows, 2)

	// Find haiku entry.
	var haiku, sonnet *struct {
		ProviderSystem        string  `json:"provider_system"`
		Model                 string  `json:"model"`
		Samples               int     `json:"samples"`
		AvgOutputTokensPerSec float64 `json:"avg_output_tokens_per_sec"`
	}
	for i := range rows {
		switch rows[i].Model {
		case "claude-haiku-4-5":
			haiku = &rows[i]
		case "claude-sonnet-4-5":
			sonnet = &rows[i]
		}
	}
	require.NotNil(t, haiku)
	require.NotNil(t, sonnet)
	assert.Equal(t, "anthropic", haiku.ProviderSystem)
	assert.Equal(t, 2, haiku.Samples)
	assert.InDelta(t, 24.65, haiku.AvgOutputTokensPerSec, 0.01)
	assert.Equal(t, "anthropic", sonnet.ProviderSystem)
	assert.Equal(t, 1, sonnet.Samples)
	assert.InDelta(t, 18.7, sonnet.AvgOutputTokensPerSec, 0.01)
}

// TestCatalogObservations_ProviderFilter verifies that --provider filters results.
func TestCatalogObservations_ProviderFilter(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()

	obsFile := filepath.Join(t.TempDir(), "observations.yaml")
	obsYAML := `rings:
  - provider_system: anthropic
    model: claude-haiku-4-5
    ring:
      capacity: 200
      samples:
        - output_tokens_per_sec: 24.3
          duration_ms: 1000
          recorded_at: 2026-01-01T00:00:00Z
  - provider_system: openai
    model: gpt-4o
    ring:
      capacity: 200
      samples:
        - output_tokens_per_sec: 30.0
          duration_ms: 1000
          recorded_at: 2026-01-01T00:00:00Z
`
	require.NoError(t, os.WriteFile(obsFile, []byte(obsYAML), 0o644))

	exe := buildAgentCLI(t)
	res := runBuiltCLI(t, exe, workDir,
		testEnvWithHome(home, map[string]string{
			"DDX_OBSERVATIONS_FILE": obsFile,
		}),
		"--work-dir", workDir, "catalog", "observations", "--provider", "anthropic",
	)
	require.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)
	assert.Contains(t, res.stdout, "claude-haiku-4-5")
	assert.NotContains(t, res.stdout, "gpt-4o")
}

// TestCatalogModels_ListsModels verifies that 'catalog models' lists at least
// the model entries present in the embedded v4 catalog.
func TestCatalogModels_ListsModels(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "models")
	require.NoError(t, err, string(out))
	output := string(out)
	assert.Contains(t, output, "MODEL")
	assert.Contains(t, output, "PROVIDER")
	// The embedded v4 catalog has model entries like haiku-5.5, sonnet-4.6, opus-4.6.
	assert.True(t,
		strings.Contains(output, "haiku-5.5") ||
			strings.Contains(output, "sonnet-4.6") ||
			strings.Contains(output, "opus-4.6"),
		"expected at least one v4 catalog model entry in output, got: %s", output,
	)
}

// TestCatalogModels_JSONOutput verifies --format json returns parseable JSON.
func TestCatalogModels_JSONOutput(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "models", "--format", "json")
	require.NoError(t, err, string(out))

	var rows []struct {
		ID             string  `json:"id"`
		ProviderSystem string  `json:"provider_system"`
		CostInput      float64 `json:"cost_input_per_mtok"`
	}
	require.NoError(t, json.Unmarshal(out, &rows), "stdout=%s", string(out))
	require.NotEmpty(t, rows)
	// All rows must have non-empty IDs.
	for _, r := range rows {
		assert.NotEmpty(t, r.ID)
	}
}

// TestCatalogModels_SingleModel verifies --model returns detail for a known v4 model ID.
func TestCatalogModels_SingleModel(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "models", "--model", "haiku-5.5")
	require.NoError(t, err, string(out))
	output := string(out)
	assert.Contains(t, output, "id:")
	assert.Contains(t, output, "haiku-5.5")
}
