package modelcatalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/reasoning"
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
version: 1
generated_at: 2026-04-10T00:00:00Z
profiles:
  code-high:
    target: alpha-smart
  code-medium:
    target: beta-fast
  code-economy:
    target: gamma-economy
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
  gamma-economy:
    family: gamma
    aliases: [gamma]
    surfaces:
      agent.anthropic: gamma-anthropic-1
      agent.openai: gamma-openai-1
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

	resolved, err := catalog.Current("code-high", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.NoError(t, err)
	assert.Equal(t, "code-high", resolved.Profile)
	assert.Equal(t, "code-high", resolved.CanonicalID)
	assert.Equal(t, "opus-4.6", resolved.ConcreteModel)
	assert.Equal(t, reasoning.ReasoningHigh, resolved.SurfacePolicy.ReasoningDefault)
	assert.Equal(t, "embedded", resolved.ManifestSource)
	assert.Equal(t, "2026-04-12.3", resolved.CatalogVersion)
}

func TestDefault_ReasoningDefaultsByTier(t *testing.T) {
	catalog, err := Default()
	require.NoError(t, err)

	tests := []struct {
		ref  string
		want reasoning.Reasoning
	}{
		{ref: "cheap", want: reasoning.ReasoningOff},
		{ref: "code-economy", want: reasoning.ReasoningOff},
		{ref: "standard", want: reasoning.ReasoningOff},
		{ref: "code-medium", want: reasoning.ReasoningOff},
		{ref: "smart", want: reasoning.ReasoningHigh},
		{ref: "code-high", want: reasoning.ReasoningHigh},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			resolved, err := catalog.Resolve(tt.ref, ResolveOptions{
				Surface: SurfaceAgentOpenAI,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, resolved.SurfacePolicy.ReasoningDefault)
		})
	}
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
	assert.Equal(t, 1, resolved.ManifestVersion)
}

func TestCurrent_ResolveProfile(t *testing.T) {
	catalog := loadFixtureCatalog(t)

	resolved, err := catalog.Current("code-medium", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "code-medium", resolved.Profile)
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
catalog_version: 2026-04-10.1
profiles:
  code-smart:
    target: gpt-4.1
targets:
  gpt-4.1:
    family: gpt-4.1
    aliases: [gpt-smart]
    surfaces:
      agent.openai: gpt-4.1
    surface_policy:
      agent.openai:
        reasoning_default: medium
`), 0o644))

	catalog, err := Load(LoadOptions{ManifestPath: manifestPath})
	require.NoError(t, err)

	resolved, err := catalog.Resolve("gpt-smart", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4.1", resolved.CanonicalID)
	assert.Equal(t, "gpt-4.1", resolved.ConcreteModel)
	assert.Equal(t, reasoning.ReasoningMedium, resolved.SurfacePolicy.ReasoningDefault)
	assert.Equal(t, "2026-04-10.1", resolved.CatalogVersion)
	assert.Equal(t, manifestPath, resolved.ManifestSource)
	assert.Equal(t, 2, resolved.ManifestVersion)
}

func TestLoad_SurfacePolicyReasoningDefaultParsing(t *testing.T) {
	manifestPath := writeFixtureManifest(t, `
version: 4
generated_at: 2026-04-10T00:00:00Z
targets:
  named-high:
    family: named
    aliases: [named]
    surfaces:
      agent.openai: named-model
    surface_policy:
      agent.openai:
        reasoning_default: high
  string-zero:
    family: zero
    aliases: [zero-string]
    surfaces:
      agent.openai: zero-string-model
    surface_policy:
      agent.openai:
        reasoning_default: "0"
  numeric-zero:
    family: zero
    aliases: [zero-number]
    surfaces:
      agent.openai: zero-number-model
    surface_policy:
      agent.openai:
        reasoning_default: 0
  numeric-budget:
    family: budget
    aliases: [budget]
    surfaces:
      agent.openai: budget-model
    surface_policy:
      agent.openai:
        reasoning_default: 4096
`)

	catalog, err := Load(LoadOptions{
		ManifestPath:    manifestPath,
		RequireExternal: true,
	})
	require.NoError(t, err)

	tests := []struct {
		ref  string
		want reasoning.Reasoning
	}{
		{ref: "named", want: reasoning.ReasoningHigh},
		{ref: "zero-string", want: reasoning.ReasoningOff},
		{ref: "zero-number", want: reasoning.ReasoningOff},
		{ref: "budget", want: reasoning.ReasoningTokens(4096)},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			resolved, err := catalog.Resolve(tt.ref, ResolveOptions{
				Surface: SurfaceAgentOpenAI,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, resolved.SurfacePolicy.ReasoningDefault)
		})
	}
}

func TestLoad_SurfacePolicyRequiresReasoningDefault(t *testing.T) {
	manifestPath := writeFixtureManifest(t, `
version: 4
generated_at: 2026-04-10T00:00:00Z
targets:
  missing:
    family: missing
    surfaces:
      agent.openai: missing-model
    surface_policy:
      agent.openai:
        {}
`)

	_, err := Load(LoadOptions{
		ManifestPath:    manifestPath,
		RequireExternal: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reasoning_default")
}

func TestLoad_FallbackToEmbedded(t *testing.T) {
	catalog, err := Load(LoadOptions{
		ManifestPath: filepath.Join(t.TempDir(), "missing.yaml"),
	})
	require.NoError(t, err)

	resolved, err := catalog.Resolve("smart", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.NoError(t, err)
	assert.Equal(t, "embedded", resolved.ManifestSource)
	assert.Equal(t, "code-high", resolved.CanonicalID)
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

func TestLoad_InvalidManifest_SurfacePolicyRequiresMatchingSurface(t *testing.T) {
	manifestPath := writeFixtureManifest(t, `
version: 2
generated_at: 2026-04-10T00:00:00Z
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
    surface_policy:
      codex:
        reasoning_default: high
`)

	_, err := Load(LoadOptions{
		ManifestPath:    manifestPath,
		RequireExternal: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "surface_policy")
	assert.Contains(t, err.Error(), "matching surface")
}

func TestLoad_AllowsProfileWithSameNameAsTarget(t *testing.T) {
	manifestPath := writeFixtureManifest(t, `
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
`)

	catalog, err := Load(LoadOptions{
		ManifestPath:    manifestPath,
		RequireExternal: true,
	})
	require.NoError(t, err)

	resolved, err := catalog.Current("code-high", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "code-high", resolved.Profile)
	assert.Equal(t, "code-high", resolved.CanonicalID)
	assert.Equal(t, "gpt-5.4", resolved.ConcreteModel)
	assert.Equal(t, reasoning.ReasoningHigh, resolved.SurfacePolicy.ReasoningDefault)
}

func TestLoad_UnsupportedSchemaVersion(t *testing.T) {
	manifestPath := writeFixtureManifest(t, `
version: 5
generated_at: 2026-04-10T00:00:00Z
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
`)

	_, err := Load(LoadOptions{
		ManifestPath:    manifestPath,
		RequireExternal: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported schema version 5")
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

// loadV4FixtureCatalog loads a v4 manifest with models: map and candidates: lists.
func loadV4FixtureCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := Load(LoadOptions{
		ManifestPath: writeFixtureManifest(t, `
version: 4
generated_at: 2026-04-13T00:00:00Z
catalog_version: 2026-04-13.1
models:
  alpha-model-1:
    family: alpha
    tier: alpha-smart
    status: active
    provider_system: anthropic
    cost_input_per_m: 3.00
    cost_output_per_m: 15.00
    swe_bench_verified: 72.7
    surfaces:
      agent.anthropic: alpha-model-1
  alpha-model-2:
    family: alpha
    tier: alpha-smart
    status: active
    provider_system: anthropic
    cost_input_per_m: 0.80
    cost_output_per_m: 4.00
    swe_bench_verified: 65.0
    surfaces:
      agent.anthropic: alpha-model-2
  beta-model-1:
    family: beta
    tier: beta-fast
    status: active
    provider_system: openai
    cost_input_per_m: 0.10
    cost_output_per_m: 0.30
    swe_bench_verified: 59.0
    context_window: 262144
    reasoning_max_tokens: 32768
    reasoning_budgets:
      low: 2048
      medium: 8192
      high: 32768
    surfaces:
      agent.openai: beta-model-1
  beta-model-2:
    family: beta
    tier: beta-fast
    status: active
    provider_system: openai
    cost_input_per_m: 0.07
    cost_output_per_m: 0.20
    surfaces:
      agent.openai: beta-model-2
profiles:
  code-alpha:
    target: alpha-smart
  code-beta:
    target: beta-fast
targets:
  alpha-smart:
    family: alpha
    aliases: [alpha]
    candidates: [alpha-model-1, alpha-model-2, beta-model-1]
  beta-fast:
    family: beta
    aliases: [beta]
    candidates: [beta-model-1, beta-model-2]
`),
		RequireExternal: true,
	})
	require.NoError(t, err)
	return catalog
}

func TestLookupModel_KnownModel(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	entry, ok := catalog.LookupModel("alpha-model-1")
	require.True(t, ok)
	assert.Equal(t, "alpha", entry.Family)
	assert.Equal(t, "alpha-smart", entry.Tier)
	assert.Equal(t, "anthropic", entry.ProviderSystem)
	assert.Equal(t, 3.00, entry.CostInputPerM)
	assert.Equal(t, 15.00, entry.CostOutputPerM)
	assert.Equal(t, 72.7, entry.SWEBenchVerified)
}

func TestLookupModel_ReasoningBudgetMetadata(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	entry, ok := catalog.LookupModel("beta-model-1")
	require.True(t, ok)
	assert.Equal(t, 32768, entry.ReasoningMaxTokens)
	assert.Equal(t, 2048, entry.ReasoningBudgets[reasoning.ReasoningLow])
	assert.Equal(t, 8192, entry.ReasoningBudgets[reasoning.ReasoningMedium])
	assert.Equal(t, 32768, entry.ReasoningBudgets[reasoning.ReasoningHigh])
}

func TestLookupModel_UnknownModel(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	_, ok := catalog.LookupModel("does-not-exist")
	assert.False(t, ok)
}

func TestContextWindowForModel_KnownModel(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	// beta-model-1 has context_window: 262144 in the fixture.
	assert.Equal(t, 262144, catalog.ContextWindowForModel("beta-model-1"))
}

func TestContextWindowForModel_ModelWithoutContextWindow(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	// alpha-model-1 exists but has no context_window declared.
	assert.Equal(t, 0, catalog.ContextWindowForModel("alpha-model-1"))
}

func TestContextWindowForModel_UnknownModel(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	assert.Equal(t, 0, catalog.ContextWindowForModel("does-not-exist"))
}

func TestContextWindowForModel_CaseInsensitive(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	// Live servers sometimes present model IDs in mixed case
	// (e.g. "Qwen3.5-27B-4bit") while the catalog uses lowercase.
	assert.Equal(t, 262144, catalog.ContextWindowForModel("Beta-Model-1"))
}

func TestContextWindowForModel_EmbeddedCatalogHasQwenWindow(t *testing.T) {
	// Regression test for the CLI fallback: the embedded v4 catalog ships with
	// context_window: 262144 on qwen3.5-27b. If this ever stops resolving,
	// LM Studio sessions that omit context_length from /v1/models will fall
	// through to the package default (131072) and compact too aggressively.
	catalog, err := Default()
	require.NoError(t, err)
	assert.Equal(t, 262144, catalog.ContextWindowForModel("qwen3.5-27b"))
}

func TestCandidatesFor_CandidatesList(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	// Surface with candidates list
	candidates := catalog.CandidatesFor(SurfaceAgentAnthropic, "alpha-smart")
	assert.Equal(t, []string{"alpha-model-1", "alpha-model-2"}, candidates)
}

func TestCandidatesFor_SingleStringFormat(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	candidates := catalog.CandidatesFor(SurfaceAgentOpenAI, "alpha-smart")
	assert.Equal(t, []string{"beta-model-1"}, candidates)
}

func TestCandidatesFor_MissingTarget(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	candidates := catalog.CandidatesFor(SurfaceAgentAnthropic, "no-such-target")
	assert.Nil(t, candidates)
}

func TestCandidatesFor_MissingSurface(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	candidates := catalog.CandidatesFor(SurfaceClaudeCode, "alpha-smart")
	assert.Nil(t, candidates)
}

func TestPricingFor_IncludesModelsWithCost(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	pricing := catalog.PricingFor()

	// alpha-model-1 appears in surfaces and models map
	p, ok := pricing["alpha-model-1"]
	require.True(t, ok, "expected alpha-model-1 in pricing")
	assert.Equal(t, 3.00, p.InputPerMTok)
	assert.Equal(t, 15.00, p.OutputPerMTok)

	// beta-model-2 appears only in candidates list (not as primary surface model in any target)
	// but is in models map — per-model entry takes precedence
	_, hasB2 := pricing["beta-model-2"]
	assert.True(t, hasB2, "expected beta-model-2 in pricing via models map")
}

func TestAllModelsInTier_ReturnsOrderedModelEntries(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	models := catalog.AllModelsInTier("alpha-smart")

	require.Len(t, models, 3)
	assert.Equal(t, "alpha-model-1", models[0].ID)
	assert.Equal(t, "alpha-model-2", models[1].ID)
	assert.Equal(t, "beta-model-1", models[2].ID)
	assert.Equal(t, "alpha-smart", models[0].Entry.Tier)
}

func TestLoad_V3ManifestSynthesizesModelEntries(t *testing.T) {
	manifestPath := writeFixtureManifest(t, `
version: 3
generated_at: 2026-04-12T00:00:00Z
targets:
  code-medium:
    family: coding-tier
    status: active
    cost_input_per_m: 3
    cost_output_per_m: 15
    cost_cache_read_per_m: 0.30
    cost_cache_write_per_m: 3.75
    context_window: 200000
    swe_bench_verified: 75.5
    benchmark_as_of: "2026-04-12"
    openrouter_ref_id: anthropic/claude-sonnet-4.6
    surfaces:
      agent.anthropic: sonnet-4.6
      agent.openai: gpt-5.4-mini
`)

	catalog, err := Load(LoadOptions{ManifestPath: manifestPath, RequireExternal: true})
	require.NoError(t, err)

	anthropicModel, ok := catalog.LookupModel("sonnet-4.6")
	require.True(t, ok)
	assert.Equal(t, "coding-tier", anthropicModel.Family)
	assert.Equal(t, "code-medium", anthropicModel.Tier)
	assert.Equal(t, 3.0, anthropicModel.CostInputPerM)
	assert.Equal(t, 15.0, anthropicModel.CostOutputPerM)
	assert.Equal(t, 200000, anthropicModel.ContextWindow)
	assert.Equal(t, "anthropic/claude-sonnet-4.6", anthropicModel.OpenRouterRefID)
	assert.Equal(t, "sonnet-4.6", anthropicModel.Surfaces["agent.anthropic"])

	openAIModel, ok := catalog.LookupModel("gpt-5.4-mini")
	require.True(t, ok)
	assert.Equal(t, "gpt-5.4-mini", openAIModel.Surfaces["agent.openai"])
}

func TestDefault_V4TargetsUseModelCandidates(t *testing.T) {
	catalog, err := Default()
	require.NoError(t, err)

	target := catalog.manifest.Targets["code-economy"]
	assert.Empty(t, target.Surfaces)
	assert.Zero(t, target.CostInputPerM)
	assert.Empty(t, target.OpenRouterRefID)
	assert.Equal(t, []string{"claude-haiku-5.5", "qwen3.5-27b", "qwen3.5-7b"}, target.Candidates)

	models := catalog.AllModelsInTier("code-economy")
	require.Len(t, models, 3)
	assert.Equal(t, "claude-haiku-5.5", models[0].ID)
	assert.Equal(t, "qwen3.5-27b", models[1].ID)
	assert.Equal(t, "qwen3.5-7b", models[2].ID)
}

func TestUpdateManifestPricing_UpdatesModelEntries(t *testing.T) {
	oldFetch := fetchOpenRouterPricing
	t.Cleanup(func() { fetchOpenRouterPricing = oldFetch })
	fetchOpenRouterPricing = func(time.Duration) (map[string]openrouterModelEntry, error) {
		return map[string]openrouterModelEntry{
			"provider/alpha": {
				ID:            "provider/alpha",
				ContextLength: 123456,
				Pricing: openrouterPricing{
					Prompt:          "0.000002",
					Completion:      "0.000006",
					InputCacheRead:  "0.0000005",
					InputCacheWrite: "0.0000025",
				},
			},
		}, nil
	}

	manifestPath := writeFixtureManifest(t, `
version: 4
generated_at: 2026-04-13T00:00:00Z
models:
  alpha-model:
    family: alpha
    tier: code-alpha
    status: active
    openrouter_id: provider/alpha
    cost_input_per_m: 1
    cost_output_per_m: 2
    surfaces:
      agent.openai: alpha-model
  missing-model:
    family: alpha
    tier: code-alpha
    status: active
    openrouter_id: provider/missing
    surfaces:
      agent.openai: missing-model
targets:
  code-alpha:
    family: alpha
    candidates: [alpha-model, missing-model]
`)

	updated, notFound, err := UpdateManifestPricing(manifestPath, time.Second)
	require.NoError(t, err)
	assert.Equal(t, 1, updated)
	assert.Equal(t, []string{"missing-model"}, notFound)

	catalog, err := Load(LoadOptions{ManifestPath: manifestPath, RequireExternal: true})
	require.NoError(t, err)
	model, ok := catalog.LookupModel("alpha-model")
	require.True(t, ok)
	assert.Equal(t, 2.0, model.CostInputPerM)
	assert.Equal(t, 6.0, model.CostOutputPerM)
	assert.Equal(t, 0.5, model.CostCacheReadPerM)
	assert.Equal(t, 2.5, model.CostCacheWritePerM)
	assert.Equal(t, 123456, model.ContextWindow)
}

func TestAllConcreteModels_IncludesCandidates(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	models := catalog.AllConcreteModels(SurfaceAgentAnthropic)

	// Both candidates for alpha-smart should be present
	assert.Equal(t, "alpha-smart", models["alpha-model-1"])
	assert.Equal(t, "alpha-smart", models["alpha-model-2"])
}

func TestAllConcreteModels_SingleStringFormat(t *testing.T) {
	catalog := loadV4FixtureCatalog(t)

	models := catalog.AllConcreteModels(SurfaceAgentOpenAI)

	// Single-string surface entry
	assert.Equal(t, "alpha-smart", models["beta-model-1"])
	// Candidates list on beta-fast
	assert.Equal(t, "beta-fast", models["beta-model-2"])
}

func TestV4Manifest_BackwardsCompatibleLoad(t *testing.T) {
	// Verify that a v4 manifest with mixed old-style (string) and new-style
	// (candidates) surface values still resolves correctly.
	catalog := loadV4FixtureCatalog(t)

	resolved, err := catalog.Current("code-alpha", ResolveOptions{
		Surface: SurfaceAgentAnthropic,
	})
	require.NoError(t, err)
	// Primary candidate is the first in the list
	assert.Equal(t, "alpha-model-1", resolved.ConcreteModel)
	assert.Equal(t, "alpha-smart", resolved.CanonicalID)

	// Old-style string surface still resolves
	resolved2, err := catalog.Resolve("alpha-smart", ResolveOptions{
		Surface: SurfaceAgentOpenAI,
	})
	require.NoError(t, err)
	assert.Equal(t, "beta-model-1", resolved2.ConcreteModel)
}
