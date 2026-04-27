// Package config provides multi-provider configuration loading for agent.
// Supports named providers, environment variable expansion, and compatibility
// with the old flat config format.
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/provider/anthropic"
	"github.com/DocumentDrivenDX/agent/internal/provider/limits"
	"github.com/DocumentDrivenDX/agent/internal/provider/lmstudio"
	"github.com/DocumentDrivenDX/agent/internal/provider/luce"
	"github.com/DocumentDrivenDX/agent/internal/provider/ollama"
	"github.com/DocumentDrivenDX/agent/internal/provider/omlx"
	oaiProvider "github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/provider/openrouter"
	"github.com/DocumentDrivenDX/agent/internal/provider/vllm"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
	"github.com/DocumentDrivenDX/agent/internal/sampling"
	"github.com/DocumentDrivenDX/agent/telemetry"
	"gopkg.in/yaml.v3"
)

// ProviderConfig describes a single named provider.
type ProviderConfig struct {
	Type      string             `yaml:"type"`               // "openai", "openrouter", "lmstudio", "omlx", "luce", "vllm", "ollama", or "anthropic"
	BaseURL   string             `yaml:"base_url,omitempty"` // shorthand for one endpoint
	Endpoints []ProviderEndpoint `yaml:"endpoints,omitempty"`
	APIKey    string             `yaml:"api_key,omitempty"`
	Model     string             `yaml:"model,omitempty"`
	// ModelPattern is a case-insensitive regex applied to auto-discovered model
	// IDs when Model is empty. The first matching model returned by /v1/models
	// is used. If the pattern matches nothing, the first available model is
	// used as a fallback.
	ModelPattern string            `yaml:"model_pattern,omitempty"`
	Headers      map[string]string `yaml:"headers"` // extra HTTP headers (OpenRouter, Azure)
	// Reasoning controls model-side reasoning with one scalar value.
	Reasoning reasoning.Reasoning `yaml:"reasoning,omitempty"`
	// MaxTokens is the maximum number of tokens the model may generate per turn.
	// Zero means use the provider's default.
	MaxTokens int `yaml:"max_tokens,omitempty"`
	// ContextWindow is the model's context window in tokens. Used to configure
	// automatic compaction: the compactor triggers when message history approaches
	// this limit. Zero means use the compaction package default (8192).
	ContextWindow int `yaml:"context_window,omitempty"`
	// Sampling overrides the harness defaults for sampling parameters.
	// Any nil/unset field falls through to harness/server defaults.
	Sampling *SamplingProfile `yaml:"sampling,omitempty"`
}

// SamplingProfile is the canonical sampling-overrides bundle. The type
// itself lives in internal/sampling so model-catalog entries and provider
// config can share it without a circular import; this alias preserves the
// existing config.SamplingProfile spelling for callers.
type SamplingProfile = sampling.Profile

// ProviderEndpoint describes one serving endpoint for providers that can run
// across multiple host:port locations.
type ProviderEndpoint struct {
	Name    string `yaml:"name,omitempty"`
	BaseURL string `yaml:"base_url"`
}

// EndpointConfig describes one endpoint-first serving target. Unlike
// ProviderConfig, endpoint blocks do not require a user-facing provider name
// or a configured model; routing discovers live model IDs from /v1/models.
type EndpointConfig struct {
	Name      string              `yaml:"name,omitempty"`
	Type      string              `yaml:"type"`
	Host      string              `yaml:"host,omitempty"`
	Port      int                 `yaml:"port,omitempty"`
	BaseURL   string              `yaml:"base_url,omitempty"`
	APIKey    string              `yaml:"api_key,omitempty"`
	Headers   map[string]string   `yaml:"headers,omitempty"`
	Reasoning reasoning.Reasoning `yaml:"reasoning,omitempty"`
}

// ImportMetadata records the last import source for drift detection.
type ImportMetadata struct {
	// Source is the import source ("pi" or "opencode").
	Source string `yaml:"source"`
	// Timestamp is when the import occurred (RFC3339).
	Timestamp string `yaml:"timestamp"`
	// SourceHash is the truncated SHA-256 of the source files.
	SourceHash string `yaml:"source_hash"`
}

// ModelCatalogConfig configures how the shared model catalog is loaded.
type ModelCatalogConfig struct {
	Manifest string `yaml:"manifest,omitempty"`
}

type ToolsConfig struct {
	Bash BashToolConfig `yaml:"bash,omitempty"`
}

type BashToolConfig struct {
	OutputFilter BashOutputFilterConfig `yaml:"output_filter,omitempty"`
}

type BashOutputFilterConfig struct {
	Mode         string `yaml:"mode,omitempty"`
	RTKBinary    string `yaml:"rtk_binary,omitempty"`
	MaxBytes     int    `yaml:"max_bytes,omitempty"`
	RawOutputDir string `yaml:"raw_output_dir,omitempty"`
}

// RoutingConfig configures model-first route selection defaults.
type RoutingConfig struct {
	// DefaultModel is the default requested model-route key to use when the
	// caller does not set --model, --model-ref, or --provider.
	DefaultModel string `yaml:"default_model,omitempty"`

	// DefaultModelRef resolves through the model catalog to a canonical route
	// key when no explicit provider or model input is supplied.
	DefaultModelRef string `yaml:"default_model_ref,omitempty"`

	// HealthCooldown controls how long a failed candidate remains deprioritized
	// before it is retried.
	HealthCooldown string `yaml:"health_cooldown,omitempty"`

	// HistoryWindow controls how far back observed routing history is sampled
	// when scoring healthy candidates.
	HistoryWindow string `yaml:"history_window,omitempty"`

	// ProbeTimeout controls provider availability/model probes.
	ProbeTimeout string `yaml:"probe_timeout,omitempty"`

	// ReliabilityWeight weights recent success history when scoring healthy
	// candidates.
	ReliabilityWeight float64 `yaml:"reliability_weight,omitempty"`

	// PerformanceWeight weights observed latency/throughput when scoring healthy
	// candidates.
	PerformanceWeight float64 `yaml:"performance_weight,omitempty"`

	// LoadWeight weights recent selection volume when balancing healthy
	// candidates.
	LoadWeight float64 `yaml:"load_weight,omitempty"`

	// CostWeight weights observed known cost when balancing healthy candidates.
	CostWeight float64 `yaml:"cost_weight,omitempty"`

	// CapabilityWeight weights model benchmark capability (swe_bench_verified)
	// when scoring healthy candidates.
	CapabilityWeight float64 `yaml:"capability_weight"`
}

// ModelRouteConfig and ModelRouteCandidateConfig were moved to
// legacy_model_routes.go as part of ADR-005. The structural boundary
// test TestConfigSchemaHasNoModelRoutes asserts those types do not
// re-enter this file.

// BackendPoolConfig describes a named routing target that selects one provider
// before a run using a specified strategy.
type BackendPoolConfig struct {
	// ModelRef is the model catalog reference (alias, profile, or canonical
	// target) attached to this backend pool. Optional.
	ModelRef string `yaml:"model_ref,omitempty"`

	// Providers is the ordered list of named provider references.
	Providers []string `yaml:"providers"`

	// Strategy is the provider selection algorithm: "round-robin" or
	// "first-available". Defaults to "first-available" if empty.
	Strategy string `yaml:"strategy,omitempty"`
}

// ProviderOverrides are per-run overrides applied before building a provider.
type ProviderOverrides struct {
	Model           string
	ModelRef        string
	AllowDeprecated bool
}

const (
	projectConfigDir    = ".agent"
	globalConfigDirName = "agent"
)

// Config is the top-level agent configuration.
type Config struct {
	// Providers is a map of named provider configurations.
	Providers map[string]ProviderConfig `yaml:"providers"`

	// Endpoints is the endpoint-first provider schema. Each entry is expanded
	// into an internal generated provider during finalization.
	Endpoints []EndpointConfig `yaml:"endpoints,omitempty"`

	// Routing configures default model-first resolution behavior.
	Routing RoutingConfig `yaml:"routing,omitempty"`

	// ModelRoutes is the deprecated (ADR-005) model-first routing table.
	// Populated by a second-pass YAML decoder in legacy_model_routes.go
	// (no parsing tag is declared here), so config.go itself owns no
	// part of the deprecated surface.
	ModelRoutes map[string]ModelRouteConfig `yaml:"-"`

	// Backends is a map of named backend pool configurations.
	Backends map[string]BackendPoolConfig `yaml:"backends,omitempty"`

	// DefaultBackend is the name of the default backend pool. When set, it
	// takes precedence over Default for runs that do not name a provider
	// or backend explicitly.
	DefaultBackend string `yaml:"default_backend,omitempty"`

	// ModelCatalog configures the optional external manifest path.
	ModelCatalog ModelCatalogConfig `yaml:"model_catalog,omitempty"`

	Tools ToolsConfig `yaml:"tools,omitempty"`

	// Telemetry configures OTel export enablement and runtime-specific
	// pricing keyed by provider system and resolved model.
	Telemetry telemetry.Config `yaml:"telemetry,omitempty"`

	// Default is the name of the default provider. If empty, uses the first.
	Default string `yaml:"default"`

	// MaxIterations limits agent loop iterations.
	MaxIterations int `yaml:"max_iterations"`

	// ReasoningByteLimit is the maximum bytes of pure reasoning_content
	// allowed before the stream is aborted. Default is 256KB.
	// Set to 0 for unlimited (same pattern as max_iterations).
	ReasoningByteLimit int `yaml:"reasoning_byte_limit"`

	// ReasoningStallTimeout is the maximum duration (e.g. "5m", "300s")
	// that only reasoning tokens may arrive before the stream is aborted.
	// Default is 300s. Set to "0s" for unlimited.
	ReasoningStallTimeout string `yaml:"reasoning_stall_timeout"`

	// CompactionPercent scales the effective context window used to trigger
	// automatic compaction. Range 1-100. 0 or absent = use default (95%).
	// The actual trigger threshold = context_window × percent/100 - reserve_tokens.
	CompactionPercent int `yaml:"compaction_percent,omitempty"`

	// SessionLogDir is where session logs are written.
	SessionLogDir string `yaml:"session_log_dir"`

	// Preset is the system prompt preset name.
	Preset string `yaml:"preset"`

	// ImportedFrom records the last import source for drift detection.
	ImportedFrom *ImportMetadata `yaml:"imported_from,omitempty"`

	// Legacy flat fields for backwards compatibility.
	LegacyProvider string `yaml:"provider,omitempty"`
	LegacyBaseURL  string `yaml:"base_url,omitempty"`
	LegacyAPIKey   string `yaml:"api_key,omitempty"`
	LegacyModel    string `yaml:"model,omitempty"`

	warnings               []string                    `yaml:"-"`
	legacyModelRoutes      map[string]ModelRouteConfig `yaml:"-"`
	legacyModelRoutesPaths []string                    `yaml:"-"`
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	return Config{
		MaxIterations:         100,
		SessionLogDir:         filepath.Join(projectConfigDir, "sessions"),
		Preset:                "default",
		ReasoningByteLimit:    agent.DefaultReasoningByteLimit,
		ReasoningStallTimeout: agent.DefaultReasoningStallTimeout.String(),
	}
}

// ParseReasoningStallTimeout parses the ReasoningStallTimeout string into a
// time.Duration. Returns 0 if the string is empty (caller should apply default).
func (c *Config) ParseReasoningStallTimeout() (time.Duration, error) {
	s := strings.TrimSpace(c.ReasoningStallTimeout)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("config: invalid reasoning_stall_timeout %q: %w", s, err)
	}
	return d, nil
}

// Load reads configuration from .agent/config.yaml (project) and
// ~/.config/agent/config.yaml (global), with env var expansion.
// Project config overrides global config. If no config files exist,
// returns defaults.
func Load(workDir string) (*Config, error) {
	cfg := Defaults()

	// Try global config first, then project config (project wins)
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", globalConfigDirName, "config.yaml"))
	}
	paths = append(paths, filepath.Join(workDir, projectConfigDir, "config.yaml"))

	for _, p := range paths {
		data, err := safefs.ReadFile(p)
		if err != nil {
			continue
		}
		expanded := expandEnvVars(string(data))
		if err := rejectLegacyProviderReasoningKeys([]byte(expanded)); err != nil {
			return nil, fmt.Errorf("config: parsing %s: %w", p, err)
		}
		if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
			return nil, fmt.Errorf("config: parsing %s: %w", p, err)
		}
		if err := noteLegacyModelRoutes(&cfg, []byte(expanded), p); err != nil {
			return nil, err
		}
	}

	// Migrate legacy flat format
	cfg.migrateLegacy()

	// Apply env var overrides to default provider
	cfg.applyEnvOverrides()

	if err := cfg.finalize(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// migrateLegacy converts old flat fields to a named provider.
func (c *Config) migrateLegacy() {
	if c.Providers != nil && len(c.Providers) > 0 {
		return // new format takes precedence
	}
	if c.LegacyProvider == "" && c.LegacyBaseURL == "" {
		return // nothing to migrate
	}

	provType := c.LegacyProvider
	if provType == "" {
		provType = inferProviderTypeFromBaseURL(c.LegacyBaseURL)
		if provType == "" {
			provType = "lmstudio"
		}
	}
	baseURL := c.LegacyBaseURL
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}

	c.Providers = map[string]ProviderConfig{
		"default": {
			Type:    provType,
			BaseURL: baseURL,
			APIKey:  c.LegacyAPIKey,
			Model:   c.LegacyModel,
		},
	}
	if c.Default == "" {
		c.Default = "default"
	}
	c.LegacyProvider = ""
	c.LegacyBaseURL = ""
	c.LegacyAPIKey = ""
	c.LegacyModel = ""
}

// applyEnvOverrides applies AGENT_* env vars to the default provider.
func (c *Config) applyEnvOverrides() {
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}

	// Get or create default provider
	defName := c.defaultNameForEnvOverride()
	p := c.Providers[defName]
	changed := false

	if v := os.Getenv("AGENT_PROVIDER"); v != "" {
		p.Type = v
		changed = true
	}
	if v := os.Getenv("AGENT_BASE_URL"); v != "" {
		p.BaseURL = v
		changed = true
	}
	if v := os.Getenv("AGENT_API_KEY"); v != "" {
		p.APIKey = v
		changed = true
	}
	if v := os.Getenv("AGENT_MODEL"); v != "" {
		p.Model = v
		changed = true
	}

	if changed {
		if p.Type == "" {
			p.Type = inferProviderTypeFromBaseURL(p.BaseURL)
			if p.Type == "" {
				p.Type = "lmstudio"
			}
		}
		c.Providers[defName] = p
		if c.Default == "" {
			c.Default = defName
		}
	}
}

// DefaultName returns the name of the default provider.
func (c *Config) DefaultName() string {
	if c.Default != "" {
		return c.Default
	}
	// Return first provider name
	for name := range c.Providers {
		return name
	}
	return "default"
}

func (c *Config) defaultNameForEnvOverride() string {
	if c.Default != "" {
		return c.Default
	}
	if len(c.Providers) == 1 {
		for name := range c.Providers {
			return name
		}
	}
	return "default"
}

// ProviderNames returns configured provider names in stable order.
func (c *Config) ProviderNames() []string {
	if c.Providers == nil {
		return nil
	}
	// Put default first, then alphabetical
	var names []string
	defName := c.DefaultName()
	if _, ok := c.Providers[defName]; ok {
		names = append(names, defName)
	}
	sorted := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		if name != defName {
			sorted = append(sorted, name)
		}
	}
	// Simple sort
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	names = append(names, sorted...)
	return names
}

// BuildProvider creates a agent.Provider from a named provider config.
func (c *Config) BuildProvider(name string) (agent.Provider, error) {
	pc, ok := c.Providers[name]
	if !ok {
		return nil, fmt.Errorf("config: unknown provider %q", name)
	}
	return buildProviderFromConfig(pc)
}

// BuildTelemetry constructs the telemetry runtime from config.
// Pricing entries from the model catalog are seeded into RuntimePricing as
// fallback defaults; user-configured entries in ddx-agent.yaml take precedence.
func (c *Config) BuildTelemetry() telemetry.Telemetry {
	pricing := c.buildRuntimePricing()
	return telemetry.New(telemetry.Config{
		Enabled:        c.Telemetry.Enabled,
		Pricing:        pricing,
		TracerProvider: c.Telemetry.TracerProvider,
		MeterProvider:  c.Telemetry.MeterProvider,
		Shutdown:       c.Telemetry.Shutdown,
	})
}

// surfaceToProviderSystem maps a model catalog surface name to the provider
// system string used in telemetry.RuntimePricing.
func surfaceToProviderSystem(surface string) (string, bool) {
	switch surface {
	case string(modelcatalog.SurfaceAgentAnthropic):
		return "anthropic", true
	case string(modelcatalog.SurfaceAgentOpenAI):
		return "openai", true
	default:
		return "", false
	}
}

// buildRuntimePricing constructs a RuntimePricing map seeded from catalog/static
// pricing as defaults, then overlaid with any user-configured entries.
func (c *Config) buildRuntimePricing() telemetry.RuntimePricing {
	pricing := make(telemetry.RuntimePricing)

	// Seed from the model catalog. Prefer the catalog loaded per user config;
	// fall back to DefaultPricing from the static table.
	cat, err := c.LoadModelCatalog()
	if err == nil {
		seedFromCatalog(pricing, cat)
	} else {
		seedFromDefaultPricing(pricing)
	}

	// Overlay user-configured pricing — user config always wins.
	for providerSystem, models := range c.Telemetry.Pricing {
		if _, exists := pricing[providerSystem]; !exists {
			pricing[providerSystem] = make(map[string]telemetry.Cost)
		}
		for model, cost := range models {
			pricing[providerSystem][model] = cost
		}
	}

	if len(pricing) == 0 {
		return nil
	}
	return pricing
}

// seedFromCatalog populates dst with per-model pricing from the catalog for the
// agent.anthropic and agent.openai surfaces. Only entries not already present
// in dst are written (caller overlays user config afterward).
func seedFromCatalog(dst telemetry.RuntimePricing, cat *modelcatalog.Catalog) {
	catalogPricing := cat.PricingFor()

	surfaces := []struct {
		surface        modelcatalog.Surface
		providerSystem string
	}{
		{modelcatalog.SurfaceAgentAnthropic, "anthropic"},
		{modelcatalog.SurfaceAgentOpenAI, "openai"},
	}

	for _, s := range surfaces {
		models := cat.AllConcreteModels(s.surface)
		for concreteModel := range models {
			p, ok := catalogPricing[concreteModel]
			if !ok {
				continue
			}
			if _, exists := dst[s.providerSystem]; !exists {
				dst[s.providerSystem] = make(map[string]telemetry.Cost)
			}
			if _, exists := dst[s.providerSystem][concreteModel]; !exists {
				dst[s.providerSystem][concreteModel] = telemetry.Cost{
					InputPerMTok:   p.InputPerMTok,
					OutputPerMTok:  p.OutputPerMTok,
					CacheReadPerM:  p.CacheReadPerM,
					CacheWritePerM: p.CacheWritePerM,
					Currency:       "USD",
					PricingRef:     s.providerSystem + "/" + concreteModel,
				}
			}
		}
	}
}

// seedFromDefaultPricing populates dst with entries from the static
// DefaultPricing table using well-known model ID prefixes to infer the
// provider system.
func seedFromDefaultPricing(dst telemetry.RuntimePricing) {
	for modelID, p := range agent.DefaultPricing {
		providerSystem := defaultPricingProviderSystem(modelID)
		if providerSystem == "" {
			continue
		}
		if _, exists := dst[providerSystem]; !exists {
			dst[providerSystem] = make(map[string]telemetry.Cost)
		}
		if _, exists := dst[providerSystem][modelID]; !exists {
			dst[providerSystem][modelID] = telemetry.Cost{
				InputPerMTok:  p.InputPerMTok,
				OutputPerMTok: p.OutputPerMTok,
				Currency:      "USD",
				PricingRef:    providerSystem + "/" + modelID,
			}
		}
	}
}

// defaultPricingProviderSystem infers the provider system for a model ID from
// the static DefaultPricing table using well-known model ID prefixes.
func defaultPricingProviderSystem(modelID string) string {
	switch {
	case strings.HasPrefix(modelID, "claude-"):
		return "anthropic"
	case strings.HasPrefix(modelID, "gpt-"), strings.HasPrefix(modelID, "o3"), strings.HasPrefix(modelID, "o1"):
		return "openai"
	default:
		return ""
	}
}

// ResolveProviderConfig applies per-run overrides to a named provider config.
func (c *Config) ResolveProviderConfig(name string, overrides ProviderOverrides) (ProviderConfig, *modelcatalog.ResolvedTarget, error) {
	pc, ok := c.Providers[name]
	if !ok {
		return ProviderConfig{}, nil, fmt.Errorf("config: unknown provider %q", name)
	}

	if overrides.Model != "" {
		pc.Model = overrides.Model
		return pc, nil, nil
	}

	if overrides.ModelRef == "" {
		return pc, nil, nil
	}

	catalog, err := c.LoadModelCatalog()
	if err != nil {
		return ProviderConfig{}, nil, err
	}

	surface, err := surfaceForProviderType(pc.Type)
	if err != nil {
		return ProviderConfig{}, nil, err
	}

	resolved, err := catalog.Resolve(overrides.ModelRef, modelcatalog.ResolveOptions{
		Surface:         surface,
		AllowDeprecated: overrides.AllowDeprecated,
	})
	if err != nil {
		return ProviderConfig{}, nil, err
	}

	pc.Model = resolved.ConcreteModel
	return pc, &resolved, nil
}

// BuildProviderWithOverrides builds a provider after applying per-run overrides.
func (c *Config) BuildProviderWithOverrides(name string, overrides ProviderOverrides) (agent.Provider, ProviderConfig, *modelcatalog.ResolvedTarget, error) {
	pc, resolved, err := c.ResolveProviderConfig(name, overrides)
	if err != nil {
		return nil, ProviderConfig{}, nil, err
	}

	p, err := buildProviderFromConfig(pc)
	if err != nil {
		return nil, ProviderConfig{}, nil, err
	}

	return p, pc, resolved, nil
}

// DefaultProvider creates the default provider.
func (c *Config) DefaultProvider() (agent.Provider, error) {
	return c.BuildProvider(c.DefaultName())
}

// GetProvider returns the ProviderConfig for a named provider.
func (c *Config) GetProvider(name string) (ProviderConfig, bool) {
	pc, ok := c.Providers[name]
	return pc, ok
}

// GetBackend returns the BackendPoolConfig for a named backend pool.
func (c *Config) GetBackend(name string) (BackendPoolConfig, bool) {
	bc, ok := c.Backends[name]
	return bc, ok
}

// GetModelRoute / GetDeprecatedBackendRoute moved to legacy_model_routes.go.

// BackendNames returns configured backend pool names in stable alphabetical order.
func (c *Config) BackendNames() []string {
	if c.Backends == nil {
		return nil
	}
	names := make([]string, 0, len(c.Backends))
	for name := range c.Backends {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

// ModelRouteNames moved to legacy_model_routes.go.

// Warnings returns config-load warnings that the CLI may surface.
func (c *Config) Warnings() []string {
	if len(c.warnings) == 0 {
		return nil
	}
	out := make([]string, len(c.warnings))
	copy(out, c.warnings)
	return out
}

// selectProviderIndex returns the provider list index to use for a backend pool
// given the strategy and a rotation counter.
//
// Supported strategies:
//   - "first-available" (default): always index 0
//   - "round-robin": counter % len(providers)
func selectProviderIndex(strategy string, counter, numProviders int) int {
	if numProviders <= 0 {
		return 0
	}
	if strategy == "round-robin" {
		return counter % numProviders
	}
	// first-available and any unknown strategy
	return 0
}

// ResolveBackend resolves a named backend pool to a concrete provider and model.
//
// counter is the rotation index used for round-robin selection; it should be
// the number of prior requests against this backend pool. Callers that want
// stateless first-available behavior can always pass 0.
//
// overrides.Model, when set, bypasses the catalog and uses the given concrete
// model string. overrides.ModelRef, when set, overrides the backend's own
// model_ref. If neither the backend nor the overrides specify a model, the
// provider's configured default model is used.
func (c *Config) ResolveBackend(name string, counter int, overrides ProviderOverrides) (agent.Provider, ProviderConfig, *modelcatalog.ResolvedTarget, error) {
	bc, ok := c.Backends[name]
	if !ok {
		return nil, ProviderConfig{}, nil, fmt.Errorf("config: unknown backend pool %q", name)
	}
	if len(bc.Providers) == 0 {
		return nil, ProviderConfig{}, nil, fmt.Errorf("config: backend pool %q has no providers", name)
	}

	idx := selectProviderIndex(bc.Strategy, counter, len(bc.Providers))
	providerName := bc.Providers[idx]

	// Determine the effective model ref: explicit override takes priority over
	// the backend's own model_ref.
	effectiveOverrides := overrides
	if effectiveOverrides.ModelRef == "" && bc.ModelRef != "" {
		effectiveOverrides.ModelRef = bc.ModelRef
	}

	p, pc, resolved, err := c.BuildProviderWithOverrides(providerName, effectiveOverrides)
	if err != nil {
		return nil, ProviderConfig{}, nil, fmt.Errorf("config: backend pool %q: %w", name, err)
	}

	return p, pc, resolved, nil
}

func buildProviderFromConfig(pc ProviderConfig) (agent.Provider, error) {
	pc = normalizeProviderConfig(pc)
	knownModels := openAIKnownModels()
	modelWire := modelReasoningWireMap()
	switch pc.Type {
	case "openai":
		return oaiProvider.New(oaiProvider.Config{
			BaseURL:            pc.BaseURL,
			APIKey:             pc.APIKey,
			Model:              pc.Model,
			ProviderName:       "openai",
			ProviderSystem:     "openai",
			ModelPattern:       pc.ModelPattern,
			KnownModels:        knownModels,
			Headers:            pc.Headers,
			Reasoning:          pc.Reasoning,
			Capabilities:       &oaiProvider.OpenAIProtocolCapabilities,
			ModelReasoningWire: modelWire,
		}), nil
	case "openrouter":
		return openrouter.New(openrouter.Config{
			BaseURL:            pc.BaseURL,
			APIKey:             pc.APIKey,
			Model:              pc.Model,
			ModelPattern:       pc.ModelPattern,
			KnownModels:        knownModels,
			Headers:            pc.Headers,
			Reasoning:          pc.Reasoning,
			ModelReasoningWire: modelWire,
		}), nil
	case "lmstudio":
		return lmstudio.New(lmstudio.Config{
			BaseURL:      pc.BaseURL,
			APIKey:       pc.APIKey,
			Model:        pc.Model,
			ModelPattern: pc.ModelPattern,
			KnownModels:  knownModels,
			Headers:      pc.Headers,
			Reasoning:    pc.Reasoning,
		}), nil
	case "omlx":
		return omlx.New(omlx.Config{
			BaseURL:      pc.BaseURL,
			APIKey:       pc.APIKey,
			Model:        pc.Model,
			ModelPattern: pc.ModelPattern,
			KnownModels:  knownModels,
			Headers:      pc.Headers,
			Reasoning:    pc.Reasoning,
		}), nil
	case "luce":
		return luce.New(luce.Config{
			BaseURL:      pc.BaseURL,
			APIKey:       pc.APIKey,
			Model:        pc.Model,
			ModelPattern: pc.ModelPattern,
			KnownModels:  knownModels,
			Headers:      pc.Headers,
			Reasoning:    pc.Reasoning,
		}), nil
	case "vllm":
		return vllm.New(vllm.Config{
			BaseURL:      pc.BaseURL,
			APIKey:       pc.APIKey,
			Model:        pc.Model,
			ModelPattern: pc.ModelPattern,
			KnownModels:  knownModels,
			Headers:      pc.Headers,
			Reasoning:    pc.Reasoning,
		}), nil
	case "ollama":
		return ollama.New(ollama.Config{
			BaseURL:      pc.BaseURL,
			APIKey:       pc.APIKey,
			Model:        pc.Model,
			ModelPattern: pc.ModelPattern,
			KnownModels:  knownModels,
			Headers:      pc.Headers,
			Reasoning:    pc.Reasoning,
		}), nil
	case "minimax", "qwen", "zai":
		return oaiProvider.New(oaiProvider.Config{
			BaseURL:            pc.BaseURL,
			APIKey:             pc.APIKey,
			Model:              pc.Model,
			ProviderName:       pc.Type,
			ProviderSystem:     pc.Type,
			ModelPattern:       pc.ModelPattern,
			KnownModels:        knownModels,
			Headers:            pc.Headers,
			Reasoning:          pc.Reasoning,
			ModelReasoningWire: modelWire,
		}), nil
	case "anthropic":
		return anthropic.New(anthropic.Config{
			BaseURL: pc.BaseURL,
			APIKey:  pc.APIKey,
			Model:   pc.Model,
		}), nil
	default:
		return nil, fmt.Errorf("config: unknown provider type %q", pc.Type)
	}
}

// LookupModelLimits queries the concrete provider package for provider-owned
// model limits. Unknown or unsupported providers return zero values.
func LookupModelLimits(ctx context.Context, pc ProviderConfig, model string) limits.ModelLimits {
	pc = normalizeProviderConfig(pc)
	switch pc.Type {
	case "openrouter":
		return openrouter.LookupModelLimits(ctx, pc.BaseURL, pc.APIKey, pc.Headers, model)
	case "lmstudio":
		return lmstudio.LookupModelLimits(ctx, pc.BaseURL, model)
	case "omlx":
		return omlx.LookupModelLimits(ctx, pc.BaseURL, model)
	default:
		return limits.ModelLimits{}
	}
}

func openAIKnownModels() map[string]string {
	if cat, err := modelcatalog.Default(); err == nil {
		return cat.AllConcreteModels(modelcatalog.SurfaceAgentOpenAI)
	}
	return nil
}

// modelReasoningWireMap returns a model_id → reasoning_wire map sourced from
// the embedded catalog. Models without an explicit reasoning_wire field are
// omitted (the provider treats absence as the "provider" default).
func modelReasoningWireMap() map[string]string {
	cat, err := modelcatalog.Default()
	if err != nil {
		return nil
	}
	all := cat.AllModels()
	if len(all) == 0 {
		return nil
	}
	out := make(map[string]string, len(all))
	for id, entry := range all {
		if entry.ReasoningWire != "" {
			out[id] = entry.ReasoningWire
		}
	}
	return out
}

func rejectLegacyProviderReasoningKeys(data []byte) error {
	var raw struct {
		Providers map[string]map[string]any `yaml:"providers"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	for name, provider := range raw.Providers {
		if _, ok := provider["flavor"]; ok {
			return fmt.Errorf("provider %q: flavor is no longer supported; use a concrete provider type", name)
		}
		for _, key := range []string{"thinking" + "_level", "thinking" + "_budget"} {
			if _, ok := provider[key]; ok {
				return fmt.Errorf("provider %q: use reasoning instead of %s", name, key)
			}
		}
	}
	return nil
}

func (c *Config) finalize() error {
	c.warnings = nil
	c.legacyModelRoutes = make(map[string]ModelRouteConfig)
	c.emitLegacyModelRoutesWarnings()

	if err := c.expandEndpointProviders(); err != nil {
		return err
	}

	for name, pc := range c.Providers {
		normalized := normalizeProviderConfig(pc)
		c.Providers[name] = normalized
	}

	if err := c.validateProviders(); err != nil {
		return err
	}

	if c.ModelRoutes == nil {
		c.ModelRoutes = make(map[string]ModelRouteConfig)
	}

	if err := c.translateLegacyBackends(); err != nil {
		return err
	}
	if err := c.validateModelRoutes(); err != nil {
		return err
	}

	if c.CompactionPercent != 0 && (c.CompactionPercent < 1 || c.CompactionPercent > 100) {
		return fmt.Errorf("config: compaction_percent must be 1-100, got %d", c.CompactionPercent)
	}

	return nil
}

func (c *Config) expandEndpointProviders() error {
	if len(c.Endpoints) == 0 {
		return nil
	}
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig, len(c.Endpoints))
	}
	firstGenerated := ""
	used := make(map[string]bool, len(c.Providers)+len(c.Endpoints))
	for name := range c.Providers {
		used[name] = true
	}
	for i, endpoint := range c.Endpoints {
		pc, name, err := providerConfigFromEndpoint(endpoint, i+1)
		if err != nil {
			return err
		}
		name = uniqueEndpointProviderName(name, used)
		used[name] = true
		c.Providers[name] = pc
		if firstGenerated == "" {
			firstGenerated = name
		}
	}
	if c.Default == "" && firstGenerated != "" {
		c.Default = firstGenerated
	}
	return nil
}

func providerConfigFromEndpoint(endpoint EndpointConfig, ordinal int) (ProviderConfig, string, error) {
	providerType := strings.ToLower(strings.TrimSpace(endpoint.Type))
	if providerType == "" {
		providerType = inferProviderTypeFromBaseURL(endpoint.BaseURL)
	}
	if providerType == "" {
		return ProviderConfig{}, "", fmt.Errorf("config: endpoint %d: type is required when base_url does not identify a provider", ordinal)
	}
	baseURL, err := endpointBaseURL(endpoint, providerType)
	if err != nil {
		return ProviderConfig{}, "", fmt.Errorf("config: endpoint %d: %w", ordinal, err)
	}
	name := strings.TrimSpace(endpoint.Name)
	if name == "" {
		name = generatedEndpointProviderName(endpoint, providerType, baseURL, ordinal)
	}
	return ProviderConfig{
		Type:    providerType,
		BaseURL: baseURL,
		Endpoints: []ProviderEndpoint{{
			Name:    "default",
			BaseURL: baseURL,
		}},
		APIKey:    endpoint.APIKey,
		Headers:   endpoint.Headers,
		Reasoning: endpoint.Reasoning,
	}, name, nil
}

func endpointBaseURL(endpoint EndpointConfig, providerType string) (string, error) {
	if baseURL := strings.TrimSpace(endpoint.BaseURL); baseURL != "" {
		return baseURL, nil
	}
	if providerType == "openrouter" {
		return openrouter.DefaultBaseURL, nil
	}
	host := strings.TrimSpace(endpoint.Host)
	if host == "" {
		return "", fmt.Errorf("host or base_url is required")
	}
	port := endpoint.Port
	if port == 0 {
		port = defaultEndpointPort(providerType)
	}
	if port == 0 {
		return "", fmt.Errorf("port is required for provider type %q", providerType)
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(fmt.Sprintf("%s:%d", strings.TrimRight(host, "/"), port), "/") + "/v1", nil
	}
	return fmt.Sprintf("http://%s:%d/v1", host, port), nil
}

func defaultEndpointPort(providerType string) int {
	switch providerType {
	case "lmstudio":
		return 1234
	case "omlx":
		return 1235
	case "luce":
		return 1236
	case "vllm":
		return 8000
	case "ollama":
		return 11434
	default:
		return 0
	}
}

func generatedEndpointProviderName(endpoint EndpointConfig, providerType, baseURL string, ordinal int) string {
	host := strings.TrimSpace(endpoint.Host)
	if host == "" {
		host = strings.TrimSpace(baseURL)
	}
	host = strings.NewReplacer("http://", "", "https://", "", "/v1", "", "/", "-", ":", "-").Replace(host)
	if host == "" {
		return fmt.Sprintf("%s-%d", providerType, ordinal)
	}
	if endpoint.Port > 0 {
		return fmt.Sprintf("%s-%s-%d", providerType, host, endpoint.Port)
	}
	return fmt.Sprintf("%s-%s", providerType, host)
}

func uniqueEndpointProviderName(name string, used map[string]bool) string {
	if !used[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !used[candidate] {
			return candidate
		}
	}
}

func normalizeProviderConfig(pc ProviderConfig) ProviderConfig {
	pc.Type = strings.ToLower(strings.TrimSpace(pc.Type))
	pc.BaseURL = strings.TrimSpace(pc.BaseURL)
	for i := range pc.Endpoints {
		pc.Endpoints[i].Name = strings.TrimSpace(pc.Endpoints[i].Name)
		pc.Endpoints[i].BaseURL = strings.TrimSpace(pc.Endpoints[i].BaseURL)
	}
	if pc.BaseURL == "" && len(pc.Endpoints) > 0 {
		pc.BaseURL = pc.Endpoints[0].BaseURL
	}
	if pc.Type == "" {
		pc.Type = inferProviderTypeFromBaseURL(pc.BaseURL)
	}
	switch pc.Type {
	case "openrouter":
		if pc.BaseURL == "" {
			pc.BaseURL = openrouter.DefaultBaseURL
		}
	case "openai":
		if pc.BaseURL == "" {
			pc.BaseURL = "https://api.openai.com/v1"
		}
	case "lmstudio":
		if pc.BaseURL == "" {
			pc.BaseURL = lmstudio.DefaultBaseURL
		}
	case "omlx":
		if pc.BaseURL == "" {
			pc.BaseURL = omlx.DefaultBaseURL
		}
	case "luce":
		if pc.BaseURL == "" {
			pc.BaseURL = luce.DefaultBaseURL
		}
	case "vllm":
		if pc.BaseURL == "" {
			pc.BaseURL = vllm.DefaultBaseURL
		}
	case "ollama":
		if pc.BaseURL == "" {
			pc.BaseURL = ollama.DefaultBaseURL
		}
	}
	if pc.BaseURL != "" && len(pc.Endpoints) == 0 && providerUsesEndpoint(pc.Type) {
		pc.Endpoints = []ProviderEndpoint{{Name: "default", BaseURL: pc.BaseURL}}
	}
	return pc
}

func inferProviderTypeFromBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	low := strings.ToLower(baseURL)
	switch {
	case strings.Contains(low, "openrouter.ai"):
		return "openrouter"
	case strings.Contains(low, "openai.com"):
		return "openai"
	case strings.Contains(low, "minimaxi.chat"):
		return "minimax"
	case strings.Contains(low, "dashscope.aliyuncs.com"):
		return "qwen"
	case strings.Contains(low, "z.ai"):
		return "zai"
	case strings.Contains(low, ":11434"):
		return "ollama"
	case strings.Contains(low, ":1234"):
		return "lmstudio"
	case strings.Contains(low, ":1235"):
		return "omlx"
	case strings.Contains(low, ":1236"):
		return "luce"
	case strings.Contains(low, ":8000"):
		return "vllm"
	default:
		return ""
	}
}

func providerUsesEndpoint(providerType string) bool {
	switch providerType {
	case "openai", "openrouter", "lmstudio", "omlx", "luce", "vllm", "ollama", "minimax", "qwen", "zai":
		return true
	default:
		return false
	}
}

// translateLegacyBackends, translateLegacyBackend, validateModelRoutes,
// validateModelRoute, translateLegacyBackendStrategy moved to
// legacy_model_routes.go (ADR-005).

// LoadModelCatalog loads the shared model catalog using the configured manifest override path.
func (c *Config) LoadModelCatalog() (*modelcatalog.Catalog, error) {
	manifestPath := c.ModelCatalog.Manifest
	if strings.TrimSpace(manifestPath) == "" {
		manifestPath = defaultModelCatalogManifestPath()
	}
	return modelcatalog.Load(modelcatalog.LoadOptions{
		ManifestPath: manifestPath,
	})
}

func defaultModelCatalogManifestPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, globalConfigDirName, "models.yaml")
}

// ProviderImplicitGenerationConfig reports whether the inference server
// behind the given provider type auto-applies the model's HuggingFace
// generation_config.json when the request omits sampler fields. The CLI
// uses this to tone the ADR-007 §7 catalog-stale nudge — vLLM users with
// no catalog profile aren't decoding greedy, they're getting model-card
// defaults; everyone else is in the loop-bug regime.
//
// The single source of truth is each provider package's
// ProtocolCapabilities; this function exists because cmd/agent cannot
// import provider packages directly.
func ProviderImplicitGenerationConfig(providerType string) bool {
	switch providerType {
	case "vllm":
		return vllm.ProtocolCapabilities.ImplicitGenerationConfig
	default:
		return false
	}
}

func surfaceForProviderType(providerType string) (modelcatalog.Surface, error) {
	switch providerType {
	case "openai", "openrouter", "lmstudio", "omlx", "luce", "vllm", "ollama", "minimax", "qwen", "zai":
		return modelcatalog.SurfaceAgentOpenAI, nil
	case "anthropic":
		return modelcatalog.SurfaceAgentAnthropic, nil
	default:
		return "", fmt.Errorf("config: cannot resolve model reference for provider type %q", providerType)
	}
}

// envVarPattern matches ${VAR_NAME} patterns.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars replaces ${VAR} patterns with environment variable values.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match // leave unexpanded if not set
	})
}

// Save serializes the config to YAML bytes.
func (c *Config) Save() ([]byte, error) {
	// #nosec G117 -- config persistence intentionally serializes configured credentials.
	return yaml.Marshal(c)
}

// Save is a package-level alias for Config.Save.
func Save(cfg *Config) ([]byte, error) {
	return cfg.Save()
}

// SaveToFile writes the config to a YAML file.
func SaveToFile(path string, cfg *Config) error {
	data, err := cfg.Save()
	if err != nil {
		return fmt.Errorf("config: marshaling: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("config: writing %s: %w", path, err)
	}
	return nil
}

// validateProviders checks configured provider entries. A config with no
// providers is still loadable because several CLI surfaces do not need a
// runtime provider.
func (c *Config) validateProviders() error {
	if len(c.Providers) == 0 {
		return nil
	}
	for name, pc := range c.Providers {
		if pc.Type == "openai-compat" {
			return fmt.Errorf("config: provider %q: type openai-compat is no longer supported; use openai, openrouter, lmstudio, omlx, or ollama", name)
		}
		switch pc.Type {
		case "openai", "openrouter", "lmstudio", "omlx", "luce", "vllm", "ollama", "minimax", "qwen", "zai", "anthropic":
		default:
			return fmt.Errorf("config: provider %q has unknown type %q (use openai, openrouter, lmstudio, omlx, luce, vllm, ollama, or anthropic)", name, pc.Type)
		}
		if providerUsesEndpoint(pc.Type) {
			for i, endpoint := range pc.Endpoints {
				if endpoint.BaseURL == "" {
					return fmt.Errorf("config: provider %q endpoint %d: base_url is required", name, i+1)
				}
			}
		}
		if (pc.Type == "openai" || pc.Type == "openrouter" || pc.Type == "anthropic") && pc.APIKey == "" {
			return fmt.Errorf("config: provider %q (%s): api_key is required", name, pc.Type)
		}
	}
	return nil
}
