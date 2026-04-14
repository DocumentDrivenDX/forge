package modelcatalog

import (
	"fmt"
	"strings"
)

// Surface identifies the consumer-specific naming surface for a model target.
type Surface string

const (
	SurfaceAgentOpenAI    Surface = "agent.openai"
	SurfaceAgentAnthropic Surface = "agent.anthropic"
	SurfaceCodex          Surface = "codex"
	SurfaceClaudeCode     Surface = "claude-code"
)

// ResolveOptions configures how model references are resolved.
type ResolveOptions struct {
	Surface         Surface
	AllowDeprecated bool
}

// Catalog resolves logical model references into concrete consumer-specific model IDs.
type Catalog struct {
	manifest    manifest
	manifestSrc string
	aliasToID   map[string]string
	profileToID map[string]string
}

// SurfacePolicy captures optional routing metadata for a resolved surface.
type SurfacePolicy struct {
	EffortDefault string
}

// Metadata describes the loaded manifest.
type Metadata struct {
	ManifestSource  string
	ManifestVersion int
	CatalogVersion  string
}

// ResolvedTarget is the resolved output for a model reference.
type ResolvedTarget struct {
	Ref             string
	Profile         string
	Family          string
	CanonicalID     string
	ConcreteModel   string
	SurfacePolicy   SurfacePolicy
	Deprecated      bool
	Replacement     string
	CatalogVersion  string
	ManifestSource  string
	ManifestVersion int
	// Pricing (USD per 1M tokens, 0 = unknown/free)
	CostInputPerM      float64
	CostOutputPerM     float64
	CostCacheReadPerM  float64
	CostCacheWritePerM float64
	// Context
	ContextWindow int
	// Benchmarks
	SWEBenchVerified float64
	LiveCodeBench    float64
	BenchmarkAsOf    string
	// OpenRouter
	OpenRouterRefID string
}

// Metadata returns the loaded manifest metadata for inspection surfaces.
func (c *Catalog) Metadata() Metadata {
	return Metadata{
		ManifestSource:  c.manifestSrc,
		ManifestVersion: c.manifest.Version,
		CatalogVersion:  c.manifest.CatalogVersion,
	}
}

// UnknownReferenceError indicates that a reference is not known to the catalog.
type UnknownReferenceError struct {
	Ref string
}

func (e *UnknownReferenceError) Error() string {
	return fmt.Sprintf("modelcatalog: unknown reference %q", e.Ref)
}

// MissingSurfaceError indicates that a target cannot be projected to the requested surface.
type MissingSurfaceError struct {
	CanonicalID string
	Surface     Surface
}

func (e *MissingSurfaceError) Error() string {
	return fmt.Sprintf("modelcatalog: target %q has no mapping for surface %q", e.CanonicalID, e.Surface)
}

// DeprecatedTargetError indicates that a deprecated or stale target was resolved in strict mode.
type DeprecatedTargetError struct {
	CanonicalID string
	Status      string
	Replacement string
}

func (e *DeprecatedTargetError) Error() string {
	if e.Replacement == "" {
		return fmt.Sprintf("modelcatalog: target %q is %s", e.CanonicalID, e.Status)
	}
	return fmt.Sprintf("modelcatalog: target %q is %s; use %q", e.CanonicalID, e.Status, e.Replacement)
}

// UnknownTargetError indicates an internal invariant break where a referenced target is absent.
type UnknownTargetError struct {
	CanonicalID string
}

func (e *UnknownTargetError) Error() string {
	return fmt.Sprintf("modelcatalog: unknown target %q", e.CanonicalID)
}

// Current resolves a profile to its current target.
func (c *Catalog) Current(profile string, opts ResolveOptions) (ResolvedTarget, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return ResolvedTarget{}, &UnknownReferenceError{Ref: profile}
	}

	targetID, ok := c.profileToID[profile]
	if !ok {
		return ResolvedTarget{}, &UnknownReferenceError{Ref: profile}
	}

	return c.resolveTarget(profile, profile, targetID, opts)
}

// Resolve resolves a profile, canonical target, or alias to a concrete model ID.
func (c *Catalog) Resolve(ref string, opts ResolveOptions) (ResolvedTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ResolvedTarget{}, &UnknownReferenceError{Ref: ref}
	}

	if targetID, ok := c.profileToID[ref]; ok {
		return c.resolveTarget(ref, ref, targetID, opts)
	}
	if _, ok := c.manifest.Targets[ref]; ok {
		return c.resolveTarget(ref, "", ref, opts)
	}
	if targetID, ok := c.aliasToID[ref]; ok {
		return c.resolveTarget(ref, "", targetID, opts)
	}

	return ResolvedTarget{}, &UnknownReferenceError{Ref: ref}
}

// ModelEntry holds benchmark and metadata for a catalog target as looked up by
// concrete model ID.
type ModelEntry struct {
	CanonicalID      string
	Family           string
	SWEBenchVerified float64
	LiveCodeBench    float64
	BenchmarkAsOf    string
}

// LookupModel finds a catalog entry by concrete model ID (any surface).
// Returns the first matching active target. Returns (ModelEntry{}, false) if
// no target in the catalog maps to the given model ID.
func (c *Catalog) LookupModel(id string) (ModelEntry, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ModelEntry{}, false
	}
	for targetID, target := range c.manifest.Targets {
		for _, concreteID := range target.Surfaces {
			if concreteID == id {
				return ModelEntry{
					CanonicalID:      targetID,
					Family:           target.Family,
					SWEBenchVerified: target.SWEBenchVerified,
					LiveCodeBench:    target.LiveCodeBench,
					BenchmarkAsOf:    target.BenchmarkAsOf,
				}, true
			}
		}
	}
	return ModelEntry{}, false
}

// AllConcreteModels returns a map from concrete model ID to catalog target ID
// for every active target that has a mapping for the given surface. The map is
// safe to use as a membership set for ranking discovered models.
func (c *Catalog) AllConcreteModels(surface Surface) map[string]string {
	out := make(map[string]string)
	for targetID, entry := range c.manifest.Targets {
		if normalizedStatus(entry.Status) != statusActive {
			continue
		}
		if concreteID, ok := entry.Surfaces[string(surface)]; ok && concreteID != "" {
			out[concreteID] = targetID
		}
	}
	return out
}

// CatalogModelPricing holds per-million-token costs for a model as sourced from the catalog.
type CatalogModelPricing struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

// PricingFor returns pricing for all active concrete models across all surfaces.
// Only active targets with a positive CostInputPerM are included.
func (c *Catalog) PricingFor() map[string]CatalogModelPricing {
	result := make(map[string]CatalogModelPricing)
	for _, target := range c.manifest.Targets {
		if normalizedStatus(target.Status) != statusActive {
			continue
		}
		if target.CostInputPerM <= 0 {
			continue
		}
		pricing := CatalogModelPricing{
			InputPerMTok:  target.CostInputPerM,
			OutputPerMTok: target.CostOutputPerM,
		}
		for _, modelID := range target.Surfaces {
			result[modelID] = pricing
		}
	}
	return result
}

func (c *Catalog) resolveTarget(ref, profile, targetID string, opts ResolveOptions) (ResolvedTarget, error) {
	if opts.Surface == "" {
		return ResolvedTarget{}, &MissingSurfaceError{CanonicalID: targetID, Surface: opts.Surface}
	}

	target, ok := c.manifest.Targets[targetID]
	if !ok {
		return ResolvedTarget{}, &UnknownTargetError{CanonicalID: targetID}
	}
	status := normalizedStatus(target.Status)
	deprecated := status != statusActive
	if deprecated && !opts.AllowDeprecated {
		return ResolvedTarget{}, &DeprecatedTargetError{
			CanonicalID: targetID,
			Status:      status,
			Replacement: target.Replacement,
		}
	}

	concreteModel, ok := target.Surfaces[string(opts.Surface)]
	if !ok {
		return ResolvedTarget{}, &MissingSurfaceError{
			CanonicalID: targetID,
			Surface:     opts.Surface,
		}
	}
	policy := SurfacePolicy{}
	if target.SurfacePolicy != nil {
		if entry, ok := target.SurfacePolicy[string(opts.Surface)]; ok {
			policy = entry.toResolved()
		}
	}

	return ResolvedTarget{
		Ref:                ref,
		Profile:            profile,
		Family:             target.Family,
		CanonicalID:        targetID,
		ConcreteModel:      concreteModel,
		SurfacePolicy:      policy,
		Deprecated:         deprecated,
		Replacement:        target.Replacement,
		CatalogVersion:     c.manifest.CatalogVersion,
		ManifestSource:     c.manifestSrc,
		ManifestVersion:    c.manifest.Version,
		CostInputPerM:      target.CostInputPerM,
		CostOutputPerM:     target.CostOutputPerM,
		CostCacheReadPerM:  target.CostCacheReadPerM,
		CostCacheWritePerM: target.CostCacheWritePerM,
		ContextWindow:      target.ContextWindow,
		SWEBenchVerified:   target.SWEBenchVerified,
		LiveCodeBench:      target.LiveCodeBench,
		BenchmarkAsOf:      target.BenchmarkAsOf,
		OpenRouterRefID:    target.OpenRouterRefID,
	}, nil
}
