package modelcatalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFixtureManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	return path
}

func loadFixtureCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := Load(LoadOptions{
		ManifestPath: writeFixtureManifest(t, `
version: 7
generated_at: 2026-04-09T00:00:00Z
profiles:
  code-smart:
    target: alpha-smart
  code-fast:
    target: beta-fast
targets:
  alpha-smart:
    family: alpha
    aliases: [alpha, alpha-alias]
    surfaces:
      agent.anthropic: alpha-anthropic-1
      agent.openai: alpha-openai-1
  beta-fast:
    family: beta
    aliases: [beta]
    surfaces:
      agent.openai: beta-openai-1
  legacy-alpha:
    family: alpha
    status: Deprecated
    replacement: alpha-smart
    surfaces:
      agent.anthropic: legacy-anthropic-1
`),
		RequireExternal: true,
	})
	require.NoError(t, err)
	return catalog
}

func TestDefault_LoadsEmbeddedManifest(t *testing.T) {
	catalog, err := Default()
	require.NoError(t, err)

	resolved, err := catalog.Current("code-smart", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resolved.CanonicalID)
	assert.NotEmpty(t, resolved.ConcreteModel)
	assert.Equal(t, "embedded", resolved.ManifestSource)
}

func TestResolveAliasFromFixture(t *testing.T) {
	catalog := loadFixtureCatalog(t)
	resolved, err := catalog.Resolve("alpha", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha-smart", resolved.CanonicalID)
	assert.Equal(t, "alpha-anthropic-1", resolved.ConcreteModel)
	assert.False(t, resolved.Deprecated)
	assert.Equal(t, 7, resolved.ManifestVersion)
}

func TestCurrent_ResolveProfile(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	resolved, err := catalog.Current("code-fast", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "code-fast", resolved.Profile)
	assert.Equal(t, "beta-fast", resolved.CanonicalID)
	assert.Equal(t, "beta-openai-1", resolved.ConcreteModel)
}

func TestResolveCanonicalTarget(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	resolved, err := catalog.Resolve("alpha-smart", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha-openai-1", resolved.ConcreteModel)
	assert.Equal(t, "alpha", resolved.Family)
}

func TestResolveDeprecatedStrict(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	_, err := catalog.Resolve("legacy-alpha", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.Error(t, err)

	var deprecatedErr *DeprecatedTargetError
	require.True(t, errors.As(err, &deprecatedErr))
	assert.Equal(t, "legacy-alpha", deprecatedErr.CanonicalID)
	assert.Equal(t, "alpha-smart", deprecatedErr.Replacement)
}

func TestResolveDeprecatedAllowed(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	resolved, err := catalog.Resolve("legacy-alpha", ResolveOptions{
		Surface:         SurfaceAgentAnthropic,
		AllowDeprecated: true,
	})
	require.NoError(t, err)
	assert.True(t, resolved.Deprecated)
	assert.Equal(t, "alpha-smart", resolved.Replacement)
	assert.Equal(t, "legacy-anthropic-1", resolved.ConcreteModel)
}

func TestLoad_ExternalOverride(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "models.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`
version: 2
generated_at: 2026-04-09T00:00:00Z
profiles:
  code-smart:
    target: gpt-4.1
targets:
  gpt-4.1:
    family: gpt-4.1
    aliases: [gpt-smart]
    surfaces:
      agent.openai: gpt-4.1
`), 0o644))

	catalog, err := Load(LoadOptions{ManifestPath: manifestPath})
	require.NoError(t, err)

	resolved, err := catalog.Resolve("gpt-smart", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4.1", resolved.CanonicalID)
	assert.Equal(t, "gpt-4.1", resolved.ConcreteModel)
	assert.Equal(t, manifestPath, resolved.ManifestSource)
	assert.Equal(t, 2, resolved.ManifestVersion)
}

func TestLoad_FallbackToEmbedded(t *testing.T) {
	catalog, err := Load(LoadOptions{
		ManifestPath: filepath.Join(t.TempDir(), "missing.yaml"),
	})
	require.NoError(t, err)

	resolved, err := catalog.Resolve("code-smart", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.NoError(t, err)
	assert.Equal(t, "embedded", resolved.ManifestSource)
	assert.NotEmpty(t, resolved.CanonicalID)
}

func TestLoad_RequireExternal(t *testing.T) {
	_, err := Load(LoadOptions{
		ManifestPath:    filepath.Join(t.TempDir(), "missing.yaml"),
		RequireExternal: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read manifest")
}

func TestLoad_InvalidManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "models.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`
version: 1
generated_at: 2026-04-09T00:00:00Z
profiles:
  code-smart:
    target: missing
targets:
  claude-sonnet-4:
    family: claude-sonnet
    aliases: [dup]
    surfaces:
      agent.anthropic: claude-sonnet-4-20250514
  qwen3-coder-next:
    family: qwen3-coder
    aliases: [dup]
    surfaces:
      agent.openai: qwen/qwen3-coder-next
`), 0o644))

	_, err := Load(LoadOptions{
		ManifestPath:    manifestPath,
		RequireExternal: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}

func TestResolveMissingSurface(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	_, err := catalog.Resolve("beta-fast", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.Error(t, err)

	var missingSurfaceErr *MissingSurfaceError
	require.True(t, errors.As(err, &missingSurfaceErr))
	assert.Equal(t, SurfaceAgentAnthropic, missingSurfaceErr.Surface)
}

func TestResolveUnknownReference(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	_, err := catalog.Resolve("does-not-exist", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.Error(t, err)

	var unknownErr *UnknownReferenceError
	require.True(t, errors.As(err, &unknownErr))
	assert.Equal(t, "does-not-exist", unknownErr.Ref)
}

func TestResolveUnknownTarget(t *testing.T) {
	catalog := loadFixtureCatalog(t)
	delete(catalog.manifest.Targets, "alpha-smart")

	_, err := catalog.Resolve("alpha", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.Error(t, err)

	var unknownTargetErr *UnknownTargetError
	require.True(t, errors.As(err, &unknownTargetErr))
	assert.Equal(t, "alpha-smart", unknownTargetErr.CanonicalID)
}

func TestLoad_InvalidManifest_ReplacementCycle(t *testing.T) {
	manifestPath := writeFixtureManifest(t, `
version: 1
generated_at: 2026-04-09T00:00:00Z
targets:
  a:
    family: alpha
    status: deprecated
    replacement: b
    surfaces:
      agent.openai: a
  b:
    family: beta
    status: deprecated
    replacement: a
    surfaces:
      agent.openai: b
`)

	_, err := Load(LoadOptions{
		ManifestPath:    manifestPath,
		RequireExternal: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestResolveEmptyReference(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	_, err := catalog.Resolve("", ResolveOptions{Surface: SurfaceAgentOpenAI})
	require.Error(t, err)

	var unknownErr *UnknownReferenceError
	require.True(t, errors.As(err, &unknownErr))
}

func TestCurrentEmptyProfile(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	_, err := catalog.Current("", ResolveOptions{Surface: SurfaceAgentOpenAI})
	require.Error(t, err)

	var unknownErr *UnknownReferenceError
	require.True(t, errors.As(err, &unknownErr))
}

func TestNormalizedStatusCaseInsensitive(t *testing.T) {
	assert.Equal(t, statusDeprecated, normalizedStatus(" Deprecated "))
}
