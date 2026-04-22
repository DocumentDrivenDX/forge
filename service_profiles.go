package agent

import (
	"context"
	"fmt"
	"sort"

	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
)

func (s *service) ListProfiles(_ context.Context) ([]ProfileInfo, error) {
	cat, err := modelcatalog.Default()
	if err != nil {
		return nil, err
	}
	meta := cat.Metadata()
	profiles := cat.Profiles()
	aliases := cat.Aliases()
	out := make([]ProfileInfo, 0, len(profiles)+len(aliases))
	seen := make(map[string]struct{}, len(profiles)+len(aliases))
	for _, profile := range profiles {
		info := ProfileInfo{
			Name:               profile.Name,
			Target:             profile.Target,
			ProviderPreference: profile.ProviderPreference,
			CatalogVersion:     meta.CatalogVersion,
			ManifestSource:     meta.ManifestSource,
			ManifestVersion:    meta.ManifestVersion,
		}
		if profile.Name != profile.Target {
			info.AliasOf = profile.Target
		}
		out = append(out, info)
		seen[profile.Name] = struct{}{}
	}
	for _, alias := range aliases {
		if _, ok := seen[alias.Name]; ok {
			continue
		}
		out = append(out, ProfileInfo{
			Name:            alias.Name,
			Target:          alias.Target,
			AliasOf:         alias.Target,
			Deprecated:      alias.Deprecated,
			Replacement:     alias.Replacement,
			CatalogVersion:  meta.CatalogVersion,
			ManifestSource:  meta.ManifestSource,
			ManifestVersion: meta.ManifestVersion,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *service) ProfileAliases(_ context.Context) (map[string]string, error) {
	cat, err := modelcatalog.Default()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, profile := range cat.Profiles() {
		if profile.Name != profile.Target {
			out[profile.Name] = profile.Target
		}
	}
	for _, alias := range cat.Aliases() {
		target := alias.Target
		if alias.Deprecated && alias.Replacement != "" {
			target = alias.Replacement
		}
		out[alias.Name] = target
	}
	return out, nil
}

func (s *service) ResolveProfile(_ context.Context, name string) (*ResolvedProfile, error) {
	cat, err := modelcatalog.Default()
	if err != nil {
		return nil, err
	}
	meta := cat.Metadata()

	var resolved *ResolvedProfile
	for _, surface := range serviceProfileSurfaces() {
		target, err := cat.Resolve(name, modelcatalog.ResolveOptions{
			Surface:         surface.catalogSurface,
			AllowDeprecated: true,
		})
		if err != nil {
			if _, ok := err.(*modelcatalog.MissingSurfaceError); ok {
				continue
			}
			return nil, err
		}
		if resolved == nil {
			resolved = &ResolvedProfile{
				Name:            name,
				Target:          target.CanonicalID,
				Deprecated:      target.Deprecated,
				Replacement:     target.Replacement,
				CatalogVersion:  meta.CatalogVersion,
				ManifestSource:  meta.ManifestSource,
				ManifestVersion: meta.ManifestVersion,
			}
		}
		candidates := cat.CandidatesFor(surface.catalogSurface, target.CanonicalID)
		if len(candidates) == 0 && target.ConcreteModel != "" {
			candidates = []string{target.ConcreteModel}
		}
		resolved.Surfaces = append(resolved.Surfaces, ProfileSurface{
			Name:                    surface.name,
			Harness:                 surface.harness,
			ProviderSystem:          surface.providerSystem,
			Model:                   target.ConcreteModel,
			Candidates:              append([]string(nil), candidates...),
			PlacementOrder:          append([]string(nil), target.SurfacePolicy.PlacementOrder...),
			CostCeilingInputPerMTok: cloneFloat64(target.SurfacePolicy.MaxInputCostPerMTokUSD),
			ReasoningDefault:        Reasoning(target.SurfacePolicy.ReasoningDefault),
			FailurePolicy:           target.SurfacePolicy.FailurePolicy,
		})
	}
	if resolved == nil {
		return nil, fmt.Errorf("profile %q has no service-supported surface", name)
	}
	sort.Slice(resolved.Surfaces, func(i, j int) bool {
		return resolved.Surfaces[i].Name < resolved.Surfaces[j].Name
	})
	return resolved, nil
}

type serviceProfileSurface struct {
	name           string
	harness        string
	providerSystem string
	catalogSurface modelcatalog.Surface
}

func serviceProfileSurfaces() []serviceProfileSurface {
	return []serviceProfileSurface{
		{name: "native-anthropic", harness: "agent", providerSystem: "anthropic", catalogSurface: modelcatalog.SurfaceAgentAnthropic},
		{name: "native-openai", harness: "agent", providerSystem: "openai-compatible", catalogSurface: modelcatalog.SurfaceAgentOpenAI},
		{name: "codex", harness: "codex", providerSystem: "openai", catalogSurface: modelcatalog.SurfaceCodex},
		{name: "claude", harness: "claude", providerSystem: "anthropic", catalogSurface: modelcatalog.SurfaceClaudeCode},
		{name: "gemini", harness: "gemini", providerSystem: "google", catalogSurface: modelcatalog.SurfaceGemini},
	}
}

func cloneFloat64(v *float64) *float64 {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}
