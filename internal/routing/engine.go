package routing

import (
	"fmt"
	"sort"
	"time"
)

// Request is the routing input. All fields are optional except at least
// one of {Profile, ModelRef, Model, Harness, Provider} should be set
// (otherwise the engine has nothing to disambiguate on).
//
// Provider is present from day one (fixes ddx-8610020e — no soft-preference
// dropping).
type Request struct {
	Profile     string // "cheap" | "standard" | "smart"
	ModelRef    string // catalog alias (e.g. "qwen/qwen3.6")
	Model       string // exact concrete model pin
	Provider    string // soft preference (hard when Harness also set)
	Harness     string // hard preference; constrains routing to one harness
	Effort      string // "low" | "medium" | "high"
	Permissions string // "safe" | "supervised" | "unrestricted"

	// EstimatedPromptTokens, when > 0, drives context-window gating.
	EstimatedPromptTokens int

	// RequiresTools, when true, requires the candidate to support tool calling.
	RequiresTools bool
}

// MinContextWindow returns the minimum context window the request requires,
// derived from EstimatedPromptTokens with a safety margin.
func (r Request) MinContextWindow() int {
	if r.EstimatedPromptTokens <= 0 {
		return 0
	}
	// 1.25x safety margin for response tokens + tool overhead.
	return r.EstimatedPromptTokens + r.EstimatedPromptTokens/4
}

// HarnessEntry is the harness-side input the caller (service) supplies.
// It is the routing engine's view of a registered harness; the engine does
// not import the harnesses package directly to keep the dependency narrow.
type HarnessEntry struct {
	Name             string
	Surface          string
	CostClass        string
	IsLocal          bool
	IsSubscription   bool
	IsHTTPProvider   bool
	TestOnly         bool
	ExactPinSupport  bool
	DefaultModel     string
	SupportedEfforts []string
	SupportedPerms   []string
	SupportsTools    bool

	// Available reflects the harness's discovered availability.
	Available bool

	// QuotaOK / QuotaPercentUsed reflect live quota state (when applicable).
	QuotaOK          bool
	QuotaPercentUsed int

	// Providers is the list of providers this harness can dispatch to.
	// For subprocess harnesses (claude/codex) this is typically a single
	// vendor entry. For the native "agent" harness it is the configured
	// list of HTTP providers.
	Providers []ProviderEntry
}

// ProviderEntry describes one provider available under a harness.
type ProviderEntry struct {
	Name           string
	BaseURL        string
	DefaultModel   string
	DiscoveredIDs  []string // models discovered via /v1/models or equivalent
	ContextWindows map[string]int
	SupportsTools  bool

	// InCooldown reflects whether this provider is in a failure-cooldown window.
	InCooldown bool
}

// Decision is the routing engine's output: the picked candidate plus the
// full ranked list (including rejected ones with rejection reasons).
type Decision struct {
	Harness    string
	Provider   string
	Model      string
	Reason     string
	Candidates []Candidate
}

// Candidate is one ranked routing option.
type Candidate struct {
	Harness  string
	Provider string
	Model    string
	Score    float64
	Eligible bool
	Reason   string
}

// Inputs bundles the engine's external data sources.
type Inputs struct {
	Harnesses         []HarnessEntry
	HistoricalSuccess map[string]float64   // by harness name; -1 = insufficient data
	ObservedSpeedTPS  map[string]float64   // by "provider:model"
	ProviderCooldowns map[string]time.Time // by provider name
	CooldownDuration  time.Duration        // 0 = no cooldown enforcement
	Now               time.Time            // injected for deterministic testing; default time.Now()
	CatalogResolver   func(ref, surface string) (concreteModel string, ok bool)
}

// candidateInternal carries the engine's intermediate state per (harness, provider, model).
type candidateInternal struct {
	Harness               string
	Provider              string
	Model                 string
	CostClass             string
	IsSubscription        bool
	QuotaOK               bool
	QuotaPercentUsed      int
	HistoricalSuccessRate float64
	ObservedTokensPerSec  float64
	InCooldown            bool
	ProviderAffinityMatch bool
}

// Resolve runs the engine end-to-end and returns a Decision.
//
// The engine:
//  1. Enumerates (harness, provider, model) candidates from inputs.
//  2. Applies gating (capability, override, model-pin, surface).
//  3. Scores per profile policy with cooldown demotion + perf bias.
//  4. Sorts viable → score → locality → name.
//  5. Returns the top viable candidate with the full ranked list.
//
// Returns an error only when no viable candidate exists.
func Resolve(req Request, in Inputs) (*Decision, error) {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	canonicalHarness := req.Harness
	if canonicalHarness == "local" {
		canonicalHarness = "agent"
	}

	var ranked []rankedCandidate
	for _, h := range in.Harnesses {
		// TestOnly harnesses (script/virtual) only reachable via explicit override.
		if h.TestOnly && canonicalHarness != h.Name {
			continue
		}
		// Hard harness override: skip non-matching harnesses.
		if canonicalHarness != "" && canonicalHarness != h.Name {
			continue
		}
		entries := buildHarnessCandidates(h, req, in)
		ranked = append(ranked, entries...)
	}

	// Compute scores.
	for i := range ranked {
		ranked[i].out.Score = scorePolicy(req.Profile, ranked[i].internal)
	}

	// Sort: eligible first, then descending score, then locality, then alphabetical.
	sort.SliceStable(ranked, func(i, j int) bool {
		ei, ej := ranked[i].out.Eligible, ranked[j].out.Eligible
		if ei != ej {
			return ei
		}
		if !ei {
			return ranked[i].out.Harness < ranked[j].out.Harness
		}
		if ranked[i].out.Score != ranked[j].out.Score {
			return ranked[i].out.Score > ranked[j].out.Score
		}
		// Locality tiebreak: prefer local cost-class.
		li := costClassRank[ranked[i].internal.CostClass] == 0
		lj := costClassRank[ranked[j].internal.CostClass] == 0
		if li != lj {
			return li
		}
		if ranked[i].out.Harness != ranked[j].out.Harness {
			return ranked[i].out.Harness < ranked[j].out.Harness
		}
		return ranked[i].out.Provider < ranked[j].out.Provider
	})

	out := make([]Candidate, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].out
	}

	for i := range out {
		if out[i].Eligible {
			return &Decision{
				Harness:    out[i].Harness,
				Provider:   out[i].Provider,
				Model:      out[i].Model,
				Reason:     fmt.Sprintf("profile=%s; score=%.1f", req.Profile, out[i].Score),
				Candidates: out,
			}, nil
		}
	}
	return &Decision{Candidates: out}, fmt.Errorf("no viable routing candidate: %d candidates rejected", len(out))
}

type rankedCandidate struct {
	out      Candidate
	internal candidateInternal
}

// buildHarnessCandidates expands one HarnessEntry into 1..N candidates, one
// per (harness, provider, resolved-model) tuple.
func buildHarnessCandidates(h HarnessEntry, req Request, in Inputs) []rankedCandidate {
	caps := Capabilities{
		SupportsTools:    h.SupportsTools,
		SupportedEfforts: h.SupportedEfforts,
		SupportedPerms:   h.SupportedPerms,
		ExactPinSupport:  h.ExactPinSupport,
	}

	if !h.Available {
		return []rankedCandidate{{
			out: Candidate{
				Harness: h.Name,
				Reason:  "harness not available",
			},
			internal: candidateInternal{Harness: h.Name, CostClass: h.CostClass},
		}}
	}

	histRate, hasHist := in.HistoricalSuccess[h.Name]
	if !hasHist {
		histRate = -1
	}

	// Enumerate providers. For harnesses with no providers (subprocess harnesses
	// with vendor-managed billing), emit a single virtual provider entry.
	providers := h.Providers
	if len(providers) == 0 {
		providers = []ProviderEntry{{Name: ""}}
	}

	out := make([]rankedCandidate, 0, len(providers))
	for _, p := range providers {
		model, reason := resolveModel(h, p, req, in)
		ctxWin := 0
		if p.ContextWindows != nil {
			ctxWin = p.ContextWindows[model]
		}
		entryCaps := caps
		entryCaps.ContextWindow = ctxWin
		entryCaps.SupportsTools = caps.SupportsTools || p.SupportsTools

		eligible := true
		if reason == "" {
			if g := CheckGating(entryCaps, req); g != "" {
				eligible = false
				reason = g
			}
		} else {
			eligible = false
		}
		if eligible && req.Provider != "" && p.Name != "" && req.Provider != p.Name && req.Harness != "" {
			// Hard provider pin under explicit harness: reject other providers.
			eligible = false
			reason = fmt.Sprintf("provider override requires %s", req.Provider)
		}

		key := p.Name + ":" + model
		obs := in.ObservedSpeedTPS[key]

		inCooldown := false
		if p.Name != "" && p.InCooldown {
			inCooldown = true
		} else if p.Name != "" && in.CooldownDuration > 0 {
			if failedAt, ok := in.ProviderCooldowns[p.Name]; ok {
				if in.Now.Sub(failedAt) < in.CooldownDuration {
					inCooldown = true
				}
			}
		}

		ci := candidateInternal{
			Harness:               h.Name,
			Provider:              p.Name,
			Model:                 model,
			CostClass:             h.CostClass,
			IsSubscription:        h.IsSubscription,
			QuotaOK:               h.QuotaOK,
			QuotaPercentUsed:      h.QuotaPercentUsed,
			HistoricalSuccessRate: histRate,
			ObservedTokensPerSec:  obs,
			InCooldown:            inCooldown,
			ProviderAffinityMatch: req.Provider != "" && p.Name != "" && req.Provider == p.Name,
		}
		out = append(out, rankedCandidate{
			out: Candidate{
				Harness:  h.Name,
				Provider: p.Name,
				Model:    model,
				Eligible: eligible,
				Reason:   reason,
			},
			internal: ci,
		})
	}
	return out
}

// resolveModel picks the concrete model string for a (harness, provider) pair
// given the request. Returns the model and a non-empty rejection reason if
// resolution fails.
func resolveModel(h HarnessEntry, p ProviderEntry, req Request, in Inputs) (string, string) {
	// 1. Exact pin.
	if req.Model != "" {
		// If the provider has discovery data, try fuzzy matching to map the
		// canonical/short ref to the provider-native ID.
		if len(p.DiscoveredIDs) > 0 {
			if matched := FuzzyMatch(req.Model, p.DiscoveredIDs); matched != "" {
				return matched, ""
			}
			// Discovery present but no match — only reject if the model isn't
			// in the catalog either (orphan check happens at dispatch time).
			if in.CatalogResolver != nil {
				if _, ok := in.CatalogResolver(req.Model, h.Surface); !ok {
					return "", fmt.Sprintf("model %q not on provider %q", req.Model, p.Name)
				}
			}
		}
		return req.Model, ""
	}

	// 2. Catalog ref.
	if req.ModelRef != "" {
		if in.CatalogResolver != nil {
			if concrete, ok := in.CatalogResolver(req.ModelRef, h.Surface); ok {
				// If discovery is available, double-check the concrete ID
				// (or the original ref) appears on this provider.
				if len(p.DiscoveredIDs) > 0 {
					if matched := FuzzyMatch(concrete, p.DiscoveredIDs); matched != "" {
						return matched, ""
					}
					// Try the original ref against discovery.
					if matched := FuzzyMatch(req.ModelRef, p.DiscoveredIDs); matched != "" {
						return matched, ""
					}
				}
				return concrete, ""
			}
			return "", fmt.Sprintf("model ref %q not available on surface %q", req.ModelRef, h.Surface)
		}
		// No catalog: pass the ref through.
		return req.ModelRef, ""
	}

	// 3. Profile.
	if req.Profile != "" {
		if in.CatalogResolver != nil {
			if concrete, ok := in.CatalogResolver(req.Profile, h.Surface); ok {
				// If discovery is available, verify the resolved model exists.
				if len(p.DiscoveredIDs) > 0 {
					if matched := FuzzyMatch(concrete, p.DiscoveredIDs); matched != "" {
						return matched, ""
					}
					// Also try the original profile ref (handles bare-leaf refs
					// against discovery). Fixes ddx-2f5a2284.
					if matched := FuzzyMatch(req.Profile, p.DiscoveredIDs); matched != "" {
						return matched, ""
					}
					return "", fmt.Sprintf("profile %q resolves to %q but provider %q does not serve it", req.Profile, concrete, p.Name)
				}
				return concrete, ""
			}
			// No catalog mapping for this surface — fall through to defaults
			// only if defaults exist; otherwise reject so escalation skips
			// this candidate (fixes ddx-3c5ba7cc).
			if p.DefaultModel == "" && h.DefaultModel == "" {
				return "", fmt.Sprintf("profile %q not available on surface %q", req.Profile, h.Surface)
			}
		}
		// No catalog hit; fall through to default.
	}

	// 4. Provider default → harness default. Empty default is acceptable
	// when no request fields constrained model selection — orphan validation
	// happens at dispatch time.
	if p.DefaultModel != "" {
		return p.DefaultModel, ""
	}
	if h.DefaultModel != "" {
		return h.DefaultModel, ""
	}
	return "", ""
}

// EscalateProfileAware is a helper for tier escalation. Given a request that
// failed at one profile, return the next profile to try, restricted to those
// that have a viable candidate under the current Inputs (i.e., the profile's
// resolved concrete model exists on the request's pinned provider, if any).
//
// Fixes ddx-3c5ba7cc: tier escalation respects --provider affinity.
//
// Returns "" when no further profile is viable.
func EscalateProfileAware(current string, ladder []string, req Request, in Inputs) string {
	startIdx := -1
	for i, p := range ladder {
		if p == current {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return ""
	}
	for i := startIdx + 1; i < len(ladder); i++ {
		next := ladder[i]
		probe := req
		probe.Profile = next
		if dec, err := Resolve(probe, in); err == nil && dec != nil && dec.Harness != "" {
			return next
		}
	}
	return ""
}
