package modelcatalog

import (
	"fmt"
	"strings"
)

// Surface identifies the consumer-specific naming surface for a model target.
type Surface string

const (
	SurfaceForgeOpenAI    Surface = "forge.openai"
	SurfaceForgeAnthropic Surface = "forge.anthropic"
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

// ResolvedTarget is the resolved output for a model reference.
type ResolvedTarget struct {
	Ref             string
	Profile         string
	Family          string
	CanonicalID     string
	ConcreteModel   string
	Deprecated      bool
	Replacement     string
	ManifestSource  string
	ManifestVersion int
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

	return ResolvedTarget{
		Ref:             ref,
		Profile:         profile,
		Family:          target.Family,
		CanonicalID:     targetID,
		ConcreteModel:   concreteModel,
		Deprecated:      deprecated,
		Replacement:     target.Replacement,
		ManifestSource:  c.manifestSrc,
		ManifestVersion: c.manifest.Version,
	}, nil
}
