// Package config provides multi-provider configuration loading for agent.
// Supports named providers, environment variable expansion, and compatibility
// with the old flat config format.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/provider/anthropic"
	oaiProvider "github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
	"github.com/DocumentDrivenDX/agent/telemetry"
	"gopkg.in/yaml.v3"
)

// ProviderConfig describes a single named provider.
type ProviderConfig struct {
	Type    string `yaml:"type"`     // "openai-compat" or "anthropic"
	BaseURL string `yaml:"base_url"` // e.g., "http://localhost:1234/v1"
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	// ModelPattern is a case-insensitive regex applied to auto-discovered model
	// IDs when Model is empty. The first matching model returned by /v1/models
	// is used. If the pattern matches nothing, the first available model is
	// used as a fallback.
	ModelPattern string            `yaml:"model_pattern,omitempty"`
	Headers      map[string]string `yaml:"headers"` // extra HTTP headers (OpenRouter, Azure)
	// ThinkingBudget limits reasoning tokens for models that support extended
	// thinking (e.g. Qwen3, DeepSeek-R1). Zero means no budget is set.
	ThinkingBudget int `yaml:"thinking_budget,omitempty"`
	// ThinkingLevel is a named intensity level (off/low/medium/high).
	// Resolved to ThinkingBudget if ThinkingBudget is 0.
	ThinkingLevel string `yaml:"thinking_level,omitempty"`
	// MaxTokens is the maximum number of tokens the model may generate per turn.
	// Zero means use the provider's default.
	MaxTokens int `yaml:"max_tokens,omitempty"`
	// ContextWindow is the model's context window in tokens. Used to configure
	// automatic compaction: the compactor triggers when message history approaches
	// this limit. Zero means use the compaction package default (8192).
	ContextWindow int `yaml:"context_window,omitempty"`
	// Flavor identifies the server software when it cannot be reliably detected
	// from the base URL alone. Supported values: "lmstudio", "omlx", "openrouter",
	// "ollama". When set, limit discovery uses this directly instead of probing.
	Flavor string `yaml:"flavor,omitempty"`
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

// ModelRouteCandidateConfig describes one provider candidate inside a route.
type ModelRouteCandidateConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model,omitempty"`
	Priority int    `yaml:"priority,omitempty"`
}

// ModelRouteConfig describes a model-first routing target.
type ModelRouteConfig struct {
	Strategy   string                      `yaml:"strategy,omitempty"`
	Candidates []ModelRouteCandidateConfig `yaml:"candidates"`
}

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

	// Routing configures default model-first resolution behavior.
	Routing RoutingConfig `yaml:"routing,omitempty"`

	// ModelRoutes is the canonical model-first routing table keyed by requested
	// model or canonical target.
	ModelRoutes map[string]ModelRouteConfig `yaml:"model_routes,omitempty"`

	// Backends is a map of named backend pool configurations.
	Backends map[string]BackendPoolConfig `yaml:"backends,omitempty"`

	// DefaultBackend is the name of the default backend pool. When set, it
	// takes precedence over Default for runs that do not name a provider
	// or backend explicitly.
	DefaultBackend string `yaml:"default_backend,omitempty"`

	// ModelCatalog configures the optional external manifest path.
	ModelCatalog ModelCatalogConfig `yaml:"model_catalog,omitempty"`

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

	warnings          []string                    `yaml:"-"`
	legacyModelRoutes map[string]ModelRouteConfig `yaml:"-"`
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	return Config{
		MaxIterations:         20,
		SessionLogDir:         filepath.Join(projectConfigDir, "sessions"),
		Preset:                "agent",
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
		if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
			return nil, fmt.Errorf("config: parsing %s: %w", p, err)
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
		provType = "openai-compat"
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

	if v := os.Getenv("AGENT_PROVIDER"); v != "" {
		p.Type = v
	}
	if v := os.Getenv("AGENT_BASE_URL"); v != "" {
		p.BaseURL = v
	}
	if v := os.Getenv("AGENT_API_KEY"); v != "" {
		p.APIKey = v
	}
	if v := os.Getenv("AGENT_MODEL"); v != "" {
		p.Model = v
	}

	// Only write back if something was set
	if p.Type != "" || p.BaseURL != "" || p.APIKey != "" || p.Model != "" {
		if p.Type == "" {
			p.Type = "openai-compat"
		}
		if p.BaseURL == "" && p.Type == "openai-compat" {
			p.BaseURL = "http://localhost:1234/v1"
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
					InputPerMTok:  p.InputPerMTok,
					OutputPerMTok: p.OutputPerMTok,
					Currency:      "USD",
					PricingRef:    s.providerSystem + "/" + concreteModel,
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

// GetModelRoute returns the canonical model-route config for a route key.
func (c *Config) GetModelRoute(name string) (ModelRouteConfig, bool) {
	mr, ok := c.ModelRoutes[name]
	return mr, ok
}

// GetDeprecatedBackendRoute returns the translated model-route config for a
// deprecated backend compatibility input.
func (c *Config) GetDeprecatedBackendRoute(name string) (ModelRouteConfig, bool) {
	mr, ok := c.legacyModelRoutes[name]
	return mr, ok
}

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

// ModelRouteNames returns configured model-route names in stable alphabetical order.
func (c *Config) ModelRouteNames() []string {
	if c.ModelRoutes == nil {
		return nil
	}
	names := make([]string, 0, len(c.ModelRoutes))
	for name := range c.ModelRoutes {
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
	switch pc.Type {
	case "openai-compat", "openai":
		budget := pc.ThinkingBudget
		if budget == 0 && pc.ThinkingLevel != "" {
			budget = agent.ResolveThinkingBudget(agent.ThinkingLevel(pc.ThinkingLevel))
		}
		// Populate the known-models map from the embedded catalog so the provider
		// can rank discovered models by catalog recognition. Failure is non-fatal.
		var knownModels map[string]string
		if cat, err := modelcatalog.Default(); err == nil {
			knownModels = cat.AllConcreteModels(modelcatalog.SurfaceAgentOpenAI)
		}
		return oaiProvider.New(oaiProvider.Config{
			BaseURL:        pc.BaseURL,
			APIKey:         pc.APIKey,
			Model:          pc.Model,
			ModelPattern:   pc.ModelPattern,
			KnownModels:    knownModels,
			Headers:        pc.Headers,
			ThinkingBudget: budget,
			Flavor:         pc.Flavor,
		}), nil
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKey: pc.APIKey,
			Model:  pc.Model,
		}), nil
	default:
		return nil, fmt.Errorf("config: unknown provider type %q (use openai-compat or anthropic)", pc.Type)
	}
}

func (c *Config) finalize() error {
	c.warnings = nil
	c.legacyModelRoutes = make(map[string]ModelRouteConfig)

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

func (c *Config) translateLegacyBackends() error {
	for name, bc := range c.Backends {
		translated, err := c.translateLegacyBackend(name, bc)
		if err != nil {
			return err
		}
		c.legacyModelRoutes[name] = translated
		c.warnings = append(c.warnings, fmt.Sprintf("backend %q is deprecated; use model_routes plus --model/--model-ref instead", name))
	}
	if c.DefaultBackend != "" {
		if _, ok := c.legacyModelRoutes[c.DefaultBackend]; !ok {
			return fmt.Errorf("config: unknown default backend pool %q", c.DefaultBackend)
		}
		c.warnings = append(c.warnings, "default_backend is deprecated; use routing.default_model or routing.default_model_ref")
	}
	return nil
}

func (c *Config) translateLegacyBackend(name string, bc BackendPoolConfig) (ModelRouteConfig, error) {
	if strings.TrimSpace(name) == "" {
		return ModelRouteConfig{}, fmt.Errorf("config: backend pool name must not be empty")
	}
	if len(bc.Providers) == 0 {
		return ModelRouteConfig{}, fmt.Errorf("config: backend pool %q has no providers", name)
	}

	route := ModelRouteConfig{
		Strategy: translateLegacyBackendStrategy(bc.Strategy),
	}
	for _, providerName := range bc.Providers {
		providerName = strings.TrimSpace(providerName)
		if providerName == "" {
			return ModelRouteConfig{}, fmt.Errorf("config: backend pool %q references an empty provider name", name)
		}
		if _, ok := c.Providers[providerName]; !ok {
			return ModelRouteConfig{}, fmt.Errorf("config: backend pool %q references unknown provider %q", name, providerName)
		}
		route.Candidates = append(route.Candidates, ModelRouteCandidateConfig{
			Provider: providerName,
			Model:    "",
			Priority: 100,
		})
	}

	return route, nil
}

func translateLegacyBackendStrategy(strategy string) string {
	switch strings.TrimSpace(strategy) {
	case "", "first-available":
		return "ordered-failover"
	case "round-robin":
		return "priority-round-robin"
	default:
		return strategy
	}
}

func (c *Config) validateModelRoutes() error {
	for _, name := range c.ModelRouteNames() {
		route := c.ModelRoutes[name]
		if err := c.validateModelRoute(name, route); err != nil {
			return err
		}
	}
	for name, route := range c.legacyModelRoutes {
		if err := c.validateModelRoute(name, route); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateModelRoute(name string, route ModelRouteConfig) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("config: model route name must not be empty")
	}
	if len(route.Candidates) == 0 {
		return fmt.Errorf("config: model route %q has no candidates", name)
	}
	switch strings.TrimSpace(route.Strategy) {
	case "", "priority-round-robin", "ordered-failover", "smart":
	default:
		return fmt.Errorf("config: model route %q has unknown strategy %q", name, route.Strategy)
	}
	for i, candidate := range route.Candidates {
		if strings.TrimSpace(candidate.Provider) == "" {
			return fmt.Errorf("config: model route %q candidate %d is missing provider", name, i+1)
		}
		if _, ok := c.Providers[candidate.Provider]; !ok {
			return fmt.Errorf("config: model route %q candidate %d references unknown provider %q", name, i+1, candidate.Provider)
		}
	}
	return nil
}

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

func surfaceForProviderType(providerType string) (modelcatalog.Surface, error) {
	switch providerType {
	case "openai-compat", "openai":
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

// validateProviders checks that at least one usable provider configuration
// exists. Called during Load, it catches the common mistake of running with
// no config and no env-var overrides before a cryptic API error at runtime.
func (c *Config) validateProviders() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("config: no providers configured — set AGENT_PROVIDER/AGENT_BASE_URL or write a config file")
	}
	for name, pc := range c.Providers {
		if pc.Type != "openai-compat" && pc.Type != "openai" && pc.Type != "anthropic" {
			return fmt.Errorf("config: provider %q has unknown type %q (use openai-compat or anthropic)", name, pc.Type)
		}
		if pc.Type == "openai-compat" || pc.Type == "openai" {
			if pc.BaseURL == "" {
				return fmt.Errorf("config: provider %q (%s): base_url is required", name, pc.Type)
			}
		}
		if pc.Type == "anthropic" && pc.APIKey == "" {
			return fmt.Errorf("config: provider %q (anthropic): api_key is required", name)
		}
	}
	return nil
}
