package catalogdist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuild_WritesStableAndVersionedBundles(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "models.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`
version: 2
generated_at: 2026-04-10T00:00:00Z
catalog_version: 2026-04-10.1
profiles:
  code-high:
    target: code-high
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
    surface_policy:
      agent.openai:
        reasoning_default: high
`), 0o644))

	outDir := t.TempDir()
	index, err := Build(BuildOptions{
		ManifestPath:    manifestPath,
		OutputDir:       outDir,
		Channel:         "stable",
		PublishedAt:     time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
		MinAgentVersion: "v0.2.0",
		Notes:           "fixture build",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, index.SchemaVersion)
	assert.Equal(t, "2026-04-10.1", index.CatalogVersion)
	assert.Equal(t, "stable", index.Channel)
	assert.Equal(t, "0.2.0", index.MinAgentVersion)

	for _, dir := range []string{
		filepath.Join(outDir, "stable"),
		filepath.Join(outDir, "versions", "2026-04-10.1"),
	} {
		_, err = os.Stat(filepath.Join(dir, "models.yaml"))
		require.NoError(t, err)
		checksum, err := os.ReadFile(filepath.Join(dir, "models.sha256"))
		require.NoError(t, err)
		assert.NotEmpty(t, string(checksum))

		data, err := os.ReadFile(filepath.Join(dir, "index.json"))
		require.NoError(t, err)
		var got Index
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, "2026-04-10.1", got.CatalogVersion)
		assert.Equal(t, "models.yaml", got.ManifestPath)
		assert.NotEmpty(t, got.ManifestSHA256)
	}
}

func TestBuild_RejectsManifestWithoutCatalogVersion(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "models.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`
version: 2
generated_at: 2026-04-10T00:00:00Z
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
`), 0o644))

	_, err := Build(BuildOptions{
		ManifestPath: manifestPath,
		OutputDir:    t.TempDir(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing catalog_version")
}
