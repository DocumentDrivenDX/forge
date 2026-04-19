package modelcatalog

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	statusActive     = "active"
	statusDeprecated = "deprecated"
	statusStale      = "stale"
	maxSchemaVersion = 4
)

//go:embed catalog/models.yaml
var embeddedManifest []byte

// LoadOptions configures how a catalog manifest is loaded.
type LoadOptions struct {
	ManifestPath    string
	RequireExternal bool
}

// ModelEntry holds per-model metadata introduced in manifest v4.
type ModelEntry struct {
	ProviderSystem    string  `yaml:"provider_system,omitempty"`
	CostInputPerMTok  float64 `yaml:"cost_input_per_mtok,omitempty"`
	CostOutputPerMTok float64 `yaml:"cost_output_per_mtok,omitempty"`
	SWEBenchVerified  float64 `yaml:"swe_bench_verified,omitempty"`
	OpenRouterRefID   string  `yaml:"openrouter_ref_id,omitempty"`
	SpeedTokensPerSec float64 `yaml:"speed_tokens_per_sec,omitempty"`
	ContextWindow     int     `yaml:"context_window,omitempty"`
}

type manifest struct {
	Version        int                     `yaml:"version"`
	GeneratedAt    string                  `yaml:"generated_at"`
	CatalogVersion string                  `yaml:"catalog_version,omitempty"`
	Models         map[string]ModelEntry   `yaml:"models,omitempty"`
	Profiles       map[string]profileEntry `yaml:"profiles"`
	Targets        map[string]targetEntry  `yaml:"targets"`
}

type profileEntry struct {
	Target string `yaml:"target"`
}

// surfaceValue represents a surface entry that may be a plain model ID string
// or a struct with a candidates list (v4+).
type surfaceValue struct {
	// model is set when the YAML value is a plain string.
	model string
	// candidates is set when the YAML value is a struct with a candidates list.
	candidates []string
}

// UnmarshalYAML decodes either a scalar string or a mapping with candidates.
func (s *surfaceValue) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		s.model = value.Value
		return nil
	case yaml.MappingNode:
		var raw struct {
			Candidates []string `yaml:"candidates"`
		}
		if err := value.Decode(&raw); err != nil {
			return err
		}
		s.candidates = raw.Candidates
		return nil
	default:
		return fmt.Errorf("surfaceValue: unexpected YAML node kind %v", value.Kind)
	}
}

// primaryModel returns the first/primary concrete model ID for this surface.
func (s surfaceValue) primaryModel() string {
	if s.model != "" {
		return s.model
	}
	if len(s.candidates) > 0 {
		return s.candidates[0]
	}
	return ""
}

// allCandidates returns an ordered list of candidate model IDs.
func (s surfaceValue) allCandidates() []string {
	if s.model != "" {
		return []string{s.model}
	}
	out := make([]string, len(s.candidates))
	copy(out, s.candidates)
	return out
}

type targetEntry struct {
	Family        string                        `yaml:"family"`
	Aliases       []string                      `yaml:"aliases"`
	Status        string                        `yaml:"status"`
	Replacement   string                        `yaml:"replacement,omitempty"`
	DeprecatedAt  string                        `yaml:"deprecated_at,omitempty"`
	Surfaces      map[string]surfaceValue       `yaml:"surfaces"`
	SurfacePolicy map[string]surfacePolicyEntry `yaml:"surface_policy,omitempty"`
	// Pricing (USD per 1M tokens, 0 = unknown/free)
	CostInputPerM      float64 `yaml:"cost_input_per_m,omitempty"`
	CostOutputPerM     float64 `yaml:"cost_output_per_m,omitempty"`
	CostCacheReadPerM  float64 `yaml:"cost_cache_read_per_m,omitempty"`
	CostCacheWritePerM float64 `yaml:"cost_cache_write_per_m,omitempty"`
	// Context and hardware
	ContextWindow int `yaml:"context_window,omitempty"` // max tokens
	// Benchmarks
	SWEBenchVerified float64 `yaml:"swe_bench_verified,omitempty"` // percent
	LiveCodeBench    float64 `yaml:"live_code_bench,omitempty"`    // percent
	BenchmarkAsOf    string  `yaml:"benchmark_as_of,omitempty"`    // YYYY-MM-DD
	// OpenRouter
	OpenRouterRefID string `yaml:"openrouter_ref_id,omitempty"` // OR model ID when different from surface model
}

type surfacePolicyEntry struct {
	EffortDefault string `yaml:"effort_default,omitempty"`
}

func (s surfacePolicyEntry) toResolved() SurfacePolicy {
	return SurfacePolicy{EffortDefault: strings.TrimSpace(s.EffortDefault)}
}

// Default loads the embedded default catalog snapshot.
func Default() (*Catalog, error) {
	return Load(LoadOptions{})
}

// Load loads a catalog from an external manifest or falls back to the embedded snapshot.
func Load(opts LoadOptions) (*Catalog, error) {
	data := embeddedManifest
	source := "embedded"

	if opts.ManifestPath != "" {
		externalData, err := os.ReadFile(opts.ManifestPath)
		if err != nil {
			if opts.RequireExternal {
				return nil, fmt.Errorf("modelcatalog: read manifest %s: %w", opts.ManifestPath, err)
			}
		} else {
			data = externalData
			source = opts.ManifestPath
		}
	}

	return loadManifest(data, source)
}

func loadManifest(data []byte, source string) (*Catalog, error) {
	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("modelcatalog: parse manifest %s: %w", source, err)
	}
	if err := validateManifest(m); err != nil {
		return nil, fmt.Errorf("modelcatalog: validate manifest %s: %w", source, err)
	}

	catalog := &Catalog{
		manifest:    m,
		manifestSrc: source,
		aliasToID:   make(map[string]string),
		profileToID: make(map[string]string),
	}

	for profile, entry := range m.Profiles {
		catalog.profileToID[profile] = entry.Target
	}
	for targetID, target := range m.Targets {
		for _, alias := range target.Aliases {
			catalog.aliasToID[alias] = targetID
		}
	}

	return catalog, nil
}

func validateManifest(m manifest) error {
	if m.Version <= 0 {
		return fmt.Errorf("version must be greater than zero")
	}
	if m.Version > maxSchemaVersion {
		return fmt.Errorf("unsupported schema version %d (max supported %d)", m.Version, maxSchemaVersion)
	}
	if len(m.Targets) == 0 {
		return fmt.Errorf("targets must not be empty")
	}

	reserved := make(map[string]string)
	targetIDs := make([]string, 0, len(m.Targets))
	for targetID := range m.Targets {
		targetIDs = append(targetIDs, targetID)
	}
	sort.Strings(targetIDs)

	for _, targetID := range targetIDs {
		target := m.Targets[targetID]
		if strings.TrimSpace(targetID) == "" {
			return fmt.Errorf("target ID must not be empty")
		}
		if strings.TrimSpace(target.Family) == "" {
			return fmt.Errorf("target %q must define family", targetID)
		}
		if len(target.Surfaces) == 0 {
			return fmt.Errorf("target %q must define at least one surface", targetID)
		}

		status := normalizedStatus(target.Status)
		switch status {
		case statusActive, statusDeprecated, statusStale:
		default:
			return fmt.Errorf("target %q has invalid status %q", targetID, target.Status)
		}

		if target.Replacement != "" {
			if _, ok := m.Targets[target.Replacement]; !ok {
				return fmt.Errorf("target %q replacement %q not found", targetID, target.Replacement)
			}
			if replacementCycle := findReplacementCycle(m, targetID); replacementCycle != "" {
				return fmt.Errorf("target %q replacement chain contains cycle via %q", targetID, replacementCycle)
			}
		}

		if owner, exists := reserved[targetID]; exists {
			return fmt.Errorf("reference %q collides with %s", targetID, owner)
		}
		reserved[targetID] = fmt.Sprintf("target %q", targetID)

		for surface, sv := range target.Surfaces {
			if strings.TrimSpace(surface) == "" {
				return fmt.Errorf("target %q has empty surface key", targetID)
			}
			if strings.TrimSpace(sv.primaryModel()) == "" {
				return fmt.Errorf("target %q has empty model for surface %q", targetID, surface)
			}
		}
		for surface, policy := range target.SurfacePolicy {
			if strings.TrimSpace(surface) == "" {
				return fmt.Errorf("target %q has empty surface_policy key", targetID)
			}
			if _, ok := target.Surfaces[surface]; !ok {
				return fmt.Errorf("target %q surface_policy %q has no matching surface mapping", targetID, surface)
			}
			if strings.TrimSpace(policy.EffortDefault) == "" {
				return fmt.Errorf("target %q surface_policy %q must define effort_default", targetID, surface)
			}
		}

		if target.CostInputPerM < 0 {
			return fmt.Errorf("target %q cost_input_per_m must be >= 0", targetID)
		}
		if target.CostOutputPerM < 0 {
			return fmt.Errorf("target %q cost_output_per_m must be >= 0", targetID)
		}
		if target.CostCacheReadPerM < 0 {
			return fmt.Errorf("target %q cost_cache_read_per_m must be >= 0", targetID)
		}
		if target.CostCacheWritePerM < 0 {
			return fmt.Errorf("target %q cost_cache_write_per_m must be >= 0", targetID)
		}
		if target.ContextWindow < 0 {
			return fmt.Errorf("target %q context_window must be >= 0", targetID)
		}

		for _, alias := range target.Aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				return fmt.Errorf("target %q has empty alias", targetID)
			}
			if owner, exists := reserved[alias]; exists {
				return fmt.Errorf("alias %q for target %q collides with %s", alias, targetID, owner)
			}
			reserved[alias] = fmt.Sprintf("alias for target %q", targetID)
		}
	}

	profiles := make([]string, 0, len(m.Profiles))
	for profile := range m.Profiles {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)

	for _, profile := range profiles {
		entry := m.Profiles[profile]
		if strings.TrimSpace(profile) == "" {
			return fmt.Errorf("profile name must not be empty")
		}
		if strings.TrimSpace(entry.Target) == "" {
			return fmt.Errorf("profile %q must define target", profile)
		}
		if _, ok := m.Targets[entry.Target]; !ok {
			return fmt.Errorf("profile %q references unknown target %q", profile, entry.Target)
		}
		if owner, exists := reserved[profile]; exists && profile != entry.Target {
			return fmt.Errorf("profile %q collides with %s", profile, owner)
		}
		reserved[profile] = fmt.Sprintf("profile %q", profile)
	}

	return nil
}

func normalizedStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return statusActive
	}
	return status
}

func findReplacementCycle(m manifest, start string) string {
	seen := map[string]bool{start: true}
	current := start
	for {
		next := strings.TrimSpace(m.Targets[current].Replacement)
		if next == "" {
			return ""
		}
		if seen[next] {
			return next
		}
		seen[next] = true
		current = next
	}
}
