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
	Profile            string // "cheap" | "standard" | "smart"
	ModelRef           string // catalog alias (e.g. "qwen/qwen3.6")
	Model              string // exact concrete model pin
	Provider           string // soft preference (hard when Harness also set)
	Harness            string // hard preference; constrains routing to one harness
	Reasoning          string // public reasoning scalar
	Permissions        string // "safe" | "supervised" | "unrestricted"
	ProviderPreference string // "local-first" | "subscription-first" | "local-only" | "subscription-only"

	// EstimatedPromptTokens, when > 0, drives context-window gating.
	EstimatedPromptTokens int

	// RequiresTools, when true, requires the candidate to support tool calling.
	RequiresTools bool
}

const (
	ProviderPreferenceLocalFirst        = "local-first"
	ProviderPreferenceSubscriptionFirst = "subscription-first"
	ProviderPreferenceLocalOnly         = "local-only"
	ProviderPreferenceSubscriptionOnly  = "subscription-only"
)

// MinContextWindow returns the minimum context window the request requires,
// derived from EstimatedPromptTokens with a safety margin.
func (r Request) MinContextWindow() int {
	if r.EstimatedPromptTokens <= 0 {
		return 0
	}
	// 1.25x safety margin for response tokens + tool overhead.
	return r.EstimatedPromptTokens + r.EstimatedPromptTokens/4
}

const (
	QuotaTrendUnknown    = "unknown"
	QuotaTrendHealthy    = "healthy"
	QuotaTrendBurning    = "burning"
	QuotaTrendExhausting = "exhausting"
)

// HarnessEntry is the harness-side input the caller (service) supplies.
// It is the routing engine's view of a registered harness; the engine does
// not import the harnesses package directly to keep the dependency narrow.
type HarnessEntry struct {
	Name                string
	Surface             string
	CostClass           string
	IsLocal             bool
	IsSubscription      bool
	IsHTTPProvider      bool
	AutoRoutingEligible bool
	TestOnly            bool
	ExactPinSupport     bool
	DefaultModel        string
	SupportedModels     []string
	SupportedReasoning  []string
	MaxReasoningTokens  int
	SupportedPerms      []string
	SupportsTools       bool

	// Available reflects the harness's discovered availability.
	Available bool

	// QuotaOK / QuotaPercentUsed reflect live quota state (when applicable).
	// SubscriptionOK gates subscription harnesses at the eligibility level:
	// when false, the candidate is ineligible regardless of score.
	QuotaOK          bool
	QuotaPercentUsed int
	QuotaStale       bool
	QuotaTrend       string // unknown|healthy|burning|exhausting
	SubscriptionOK   bool   // false = hard gate; true = score-based demotion

	// InCooldown marks the entire harness as being in a failure cooldown.
	// When true the harness is demoted in score (via candidateInternal.InCooldown)
	// but not hard-rejected, so it can still win when all other harnesses are
	// also unavailable.
	InCooldown bool

	// Providers is the list of providers this harness can dispatch to.
	// For subprocess harnesses (claude/codex) this is typically a single
	// vendor entry. For the native "agent" harness it is the configured
	// list of HTTP providers.
	Providers []ProviderEntry
}

// ProviderEntry describes one provider available under a harness.
type ProviderEntry struct {
	Name               string
	BaseURL            string
	EndpointName       string
	EndpointBaseURL    string
	DefaultModel       string
	DiscoveredIDs      []string // models discovered via /v1/models or equivalent
	DiscoveryAttempted bool
	ContextWindows     map[string]int
	SupportsTools      bool

	// CostUSDPer1kTokens is the estimated blended USD cost per 1,000 tokens.
	// A zero value with CostSourceUnknown means the provider cost is unknown.
	CostUSDPer1kTokens float64
	// CostSource describes where CostUSDPer1kTokens came from: catalog,
	// subscription, user-config, or unknown.
	CostSource string

	// InCooldown reflects whether this provider is in a failure-cooldown window.
	InCooldown bool
}

const (
	// CostSourceCatalog means cost came from the model catalog.
	CostSourceCatalog = "catalog"
	// CostSourceSubscription means cost came from subscription quota pricing.
	CostSourceSubscription = "subscription"
	// CostSourceUnknown means no reliable cost estimate is available.
	CostSourceUnknown = "unknown"
	// CostSourceUserConfig means cost came from explicit user configuration.
	CostSourceUserConfig = "user-config"
)

// Decision is the routing engine's output: the picked candidate plus the
// full ranked list (including rejected ones with rejection reasons).
type Decision struct {
	Harness    string
	Provider   string
	Endpoint   string
	Model      string
	Reason     string
	Candidates []Candidate
}

// Candidate is one ranked routing option.
type Candidate struct {
	Harness            string
	Provider           string
	Endpoint           string
	Model              string
	Score              float64
	CostUSDPer1kTokens float64
	CostSource         string
	Eligible           bool
	Reason             string

	// FilterReason is the typed disqualification category, set at the
	// rejection site that decided why this candidate is ineligible.
	// Empty for eligible candidates. Service-layer code maps this to the
	// public FilterReason* string constants without parsing free-form
	// Reason text.
	FilterReason FilterReason

	// LatencyMS, SuccessRate, and CostClass expose the score-component
	// inputs so callers can render per-axis explanations alongside the
	// final Score. Zero / negative values mean unknown (see Inputs docs).
	LatencyMS   float64
	SuccessRate float64
	CostClass   string
}

// FilterReason categorizes why a routing candidate was disqualified.
// The zero value (empty string) means the candidate is eligible.
type FilterReason string

const (
	// FilterReasonEligible is the zero value for an eligible candidate.
	FilterReasonEligible FilterReason = ""
	// FilterReasonContextTooSmall: candidate's context window is below the
	// request's MinContextWindow().
	FilterReasonContextTooSmall FilterReason = "context_too_small"
	// FilterReasonNoToolSupport: request needs tool calling but candidate
	// does not support it.
	FilterReasonNoToolSupport FilterReason = "no_tool_support"
	// FilterReasonReasoningUnsupported: candidate cannot satisfy the
	// requested reasoning policy.
	FilterReasonReasoningUnsupported FilterReason = "reasoning_unsupported"
	// FilterReasonUnhealthy: harness/provider is unavailable, in cooldown,
	// out of quota, or excluded by a hard provider-preference gate.
	FilterReasonUnhealthy FilterReason = "unhealthy"
	// FilterReasonScoredBelowTop: catch-all for ineligibility that does
	// not fit a more specific category (also used for capability
	// mismatches such as permissions/model-pin/exact-pin and for model
	// resolution failures).
	FilterReasonScoredBelowTop FilterReason = "scored_below_top"
)

// NoViableCandidateError reports that routing evaluated candidates but every
// one failed a gate.
type NoViableCandidateError struct {
	Rejected int
}

func (e *NoViableCandidateError) Error() string {
	return fmt.Sprintf("no viable routing candidate: %d candidates rejected", e.Rejected)
}

// ErrNoLiveProvider reports that profile-tier escalation walked the entire
// ladder (cheap → standard → smart) without finding a live provider that
// can serve the request. Callers translate this into a precise user-facing
// message naming the prompt size and tool requirement so operators know
// what capability is missing across all tiers.
type ErrNoLiveProvider struct {
	// PromptTokens is the request's EstimatedPromptTokens at the time
	// escalation began. Zero means no prompt-token gating was active.
	PromptTokens int
	// RequiresTools mirrors the request's RequiresTools flag.
	RequiresTools bool
	// StartingTier is the profile name that escalation began from
	// (the profile in the original request).
	StartingTier string
}

func (e *ErrNoLiveProvider) Error() string {
	return fmt.Sprintf("no live provider supports prompt of %d tokens with tools=%v at tier ≥ %s",
		e.PromptTokens, e.RequiresTools, e.StartingTier)
}

// ProfileEscalationLadder is the fixed cheap → standard → smart progression
// service.ResolveRoute walks when every candidate at the requested tier is
// filtered out (unhealthy or capability-rejected).
var ProfileEscalationLadder = []string{"cheap", "standard", "smart"}

// Inputs bundles the engine's external data sources.
type Inputs struct {
	Harnesses           []HarnessEntry
	HistoricalSuccess   map[string]float64   // by harness name; -1 = insufficient data
	ObservedSpeedTPS    map[string]float64   // by "provider:model"
	ProviderSuccessRate map[string]float64   // by ProviderModelKey(provider, endpoint, model)
	ObservedLatencyMS   map[string]float64   // by ProviderModelKey(provider, endpoint, model)
	ProviderCooldowns   map[string]time.Time // by provider name
	CooldownDuration    time.Duration        // 0 = no cooldown enforcement
	Now                 time.Time            // injected for deterministic testing; default time.Now()
	CatalogResolver     func(ref, surface string) (concreteModel string, ok bool)
}

// candidateInternal carries the engine's intermediate state per (harness, provider, model).
type candidateInternal struct {
	Harness               string
	Provider              string
	EndpointName          string
	Model                 string
	CostClass             string
	CostUSDPer1kTokens    float64
	CostSource            string
	IsSubscription        bool
	QuotaOK               bool
	QuotaPercentUsed      int
	QuotaStale            bool
	QuotaTrend            string
	SubscriptionOK        bool
	HistoricalSuccessRate float64
	ProviderSuccessRate   float64
	ObservedTokensPerSec  float64
	ObservedLatencyMS     float64
	InCooldown            bool
	ProviderAffinityMatch bool
	ProviderPreference    string
}

// ProviderModelKey is the metrics key used by routing callers for provider
// performance signals. Endpoint is optional; when empty the key remains
// compatible with older provider:model metrics.
func ProviderModelKey(provider, endpoint, model string) string {
	if endpoint == "" {
		return provider + ":" + model
	}
	return provider + "@" + endpoint + ":" + model
}

// Resolve runs the engine end-to-end and returns a Decision.
//
// The engine:
//  1. Enumerates (harness, provider, model) candidates from inputs.
//  2. Applies gating (capability, override, model-pin, surface).
//  3. Scores per profile policy with cooldown demotion + perf bias.
//  4. Sorts viable → score → cost → locality → name.
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
		// Automatic profile/tier routing is restricted to harnesses with full
		// coverage. Explicit Harness pins can still use experimental/ad-hoc
		// harnesses.
		if canonicalHarness == "" && !h.AutoRoutingEligible {
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
		if ranked[i].out.Eligible {
			ranked[i].out.Reason = fmt.Sprintf("profile=%s; score=%.1f", req.Profile, ranked[i].out.Score)
		}
	}
	neutralCost, hasKnownCost := neutralKnownCost(ranked)

	// Sort: eligible first, then descending score, then cost, then locality,
	// then alphabetical.
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
		if hasKnownCost {
			ci := candidateCostTieValue(ranked[i], neutralCost)
			cj := candidateCostTieValue(ranked[j], neutralCost)
			if ci != cj {
				return ci < cj
			}
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

	if err := explicitPinError(req, in); err != nil {
		return &Decision{Candidates: out}, err
	}

	for i := range out {
		if out[i].Eligible {
			return &Decision{
				Harness:    out[i].Harness,
				Provider:   out[i].Provider,
				Endpoint:   out[i].Endpoint,
				Model:      out[i].Model,
				Reason:     fmt.Sprintf("profile=%s; score=%.1f", req.Profile, out[i].Score),
				Candidates: out,
			}, nil
		}
	}
	if requested := requestedModelIntent(req); requested != "" && hasLiveDiscoveryCandidates(ranked) {
		return &Decision{Candidates: out}, fmt.Errorf("no live endpoint offers a match for %s", requested)
	}
	if missingCapability := missingProfileCapability(req); missingCapability != "" {
		return &Decision{Candidates: out}, &ErrNoProfileCandidate{
			Profile:           req.Profile,
			MissingCapability: missingCapability,
			Rejected:          len(out),
		}
	}
	return &Decision{Candidates: out}, &NoViableCandidateError{Rejected: len(out)}
}

func explicitPinError(req Request, in Inputs) error {
	canonicalHarness := canonicalHarnessPin(req.Harness)
	if req.Profile != "" && (canonicalHarness != "" || req.Model != "") {
		if constraint, ok := explicitProfileConstraint(req.Profile, req.ProviderPreference); ok {
			if canonicalHarness != "" {
				if h, ok := findHarness(in.Harnesses, canonicalHarness); ok && harnessViolatesProfileConstraint(h, constraint) {
					return &ErrProfilePinConflict{
						Profile:           req.Profile,
						ConflictingPin:    "Harness=" + canonicalHarness,
						ProfileConstraint: constraint,
					}
				}
			}
			if req.Model != "" && modelPinViolatesProfileConstraint(req.Model, in, constraint) {
				return &ErrProfilePinConflict{
					Profile:           req.Profile,
					ConflictingPin:    "Model=" + req.Model,
					ProfileConstraint: constraint,
				}
			}
		}
	}

	if canonicalHarness == "" || req.Model == "" {
		return nil
	}
	// Pi can route any provider-pinned model (lmstudio, omlx, openrouter,
	// etc.); the pi CLI owns concrete model validation in that case.
	// Mirrors the bypass in service_execute.go validateExplicitHarnessModel.
	if canonicalHarness == "pi" && req.Provider != "" {
		return nil
	}
	h, ok := findHarness(in.Harnesses, canonicalHarness)
	if !ok || h.SupportedModels == nil || harnessSupportsModel(h.SupportedModels, req.Model) {
		return nil
	}
	return &ErrHarnessModelIncompatible{
		Harness:         canonicalHarness,
		Model:           req.Model,
		SupportedModels: append([]string(nil), h.SupportedModels...),
	}
}

func canonicalHarnessPin(harness string) string {
	if harness == "local" {
		return "agent"
	}
	return harness
}

func findHarness(harnesses []HarnessEntry, name string) (HarnessEntry, bool) {
	for _, h := range harnesses {
		if h.Name == name {
			return h, true
		}
	}
	return HarnessEntry{}, false
}

func harnessSupportsModel(supported []string, model string) bool {
	for _, candidate := range supported {
		if candidate == model {
			return true
		}
	}
	return false
}

func explicitProfileConstraint(profile, providerPreference string) (string, bool) {
	switch providerPreference {
	case ProviderPreferenceLocalOnly:
		return ProviderPreferenceLocalOnly, true
	case ProviderPreferenceSubscriptionOnly:
		return ProviderPreferenceSubscriptionOnly, true
	}
	switch profile {
	case "local", "offline", "air-gapped":
		return ProviderPreferenceLocalOnly, true
	case "smart", "code-smart", "code-high":
		return ProviderPreferenceSubscriptionOnly, true
	default:
		return "", false
	}
}

func harnessViolatesProfileConstraint(h HarnessEntry, constraint string) bool {
	switch constraint {
	case ProviderPreferenceLocalOnly:
		return !h.IsLocal
	case ProviderPreferenceSubscriptionOnly:
		return !h.IsSubscription
	default:
		return false
	}
}

func modelPinViolatesProfileConstraint(model string, in Inputs, constraint string) bool {
	var constrainedCanServe bool
	var outsideCanServe bool
	for _, h := range in.Harnesses {
		if h.TestOnly || !h.AutoRoutingEligible {
			continue
		}
		if !harnessCanServeExactModel(h, model) {
			continue
		}
		if harnessViolatesProfileConstraint(h, constraint) {
			outsideCanServe = true
		} else {
			constrainedCanServe = true
		}
	}
	return !constrainedCanServe && outsideCanServe
}

func harnessCanServeExactModel(h HarnessEntry, model string) bool {
	if len(h.SupportedModels) > 0 && harnessSupportsModel(h.SupportedModels, model) {
		return true
	}
	if h.DefaultModel == model {
		return true
	}
	providers := h.Providers
	if len(providers) == 0 {
		providers = []ProviderEntry{{Name: ""}}
	}
	for _, p := range providers {
		if p.DefaultModel == model {
			return true
		}
		if len(p.DiscoveredIDs) > 0 && FuzzyMatch(model, p.DiscoveredIDs) != "" {
			return true
		}
	}
	return false
}

func missingProfileCapability(req Request) string {
	if req.Profile == "" {
		return ""
	}
	switch req.ProviderPreference {
	case ProviderPreferenceLocalOnly:
		return "local endpoint"
	case ProviderPreferenceSubscriptionOnly:
		return "subscription harness"
	default:
		return ""
	}
}

func requestedModelIntent(req Request) string {
	switch {
	case req.Model != "":
		return req.Model
	case req.ModelRef != "":
		return req.ModelRef
	case req.Profile != "":
		return req.Profile
	default:
		return ""
	}
}

func hasLiveDiscoveryCandidates(candidates []rankedCandidate) bool {
	for _, c := range candidates {
		if c.out.Provider != "" && c.internal.Model == "" {
			return true
		}
		if c.out.Provider != "" && c.out.Reason != "" {
			return true
		}
	}
	return false
}

type rankedCandidate struct {
	out      Candidate
	internal candidateInternal
}

// buildHarnessCandidates expands one HarnessEntry into 1..N candidates, one
// per (harness, provider, resolved-model) tuple.
func buildHarnessCandidates(h HarnessEntry, req Request, in Inputs) []rankedCandidate {
	caps := Capabilities{
		SupportsTools:      h.SupportsTools,
		SupportedReasoning: h.SupportedReasoning,
		MaxReasoningTokens: h.MaxReasoningTokens,
		SupportedPerms:     h.SupportedPerms,
		ExactPinSupport:    h.ExactPinSupport,
		SupportedModels:    h.SupportedModels,
	}

	if !h.Available {
		return []rankedCandidate{{
			out: Candidate{
				Harness:      h.Name,
				CostSource:   CostSourceUnknown,
				Reason:       "harness not available",
				FilterReason: FilterReasonUnhealthy,
			},
			internal: candidateInternal{Harness: h.Name, CostClass: h.CostClass, CostSource: CostSourceUnknown},
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
		if len(p.DiscoveredIDs) > 0 {
			entryCaps.SupportedModels = nil
		}

		key := ProviderModelKey(p.Name, p.EndpointName, model)
		obs := in.ObservedSpeedTPS[key]
		if obs == 0 && p.EndpointName != "" {
			obs = in.ObservedSpeedTPS[ProviderModelKey(p.Name, "", model)]
		}
		providerSuccessRate := -1.0
		if rate, ok := in.ProviderSuccessRate[key]; ok {
			providerSuccessRate = rate
		} else if p.EndpointName != "" {
			if rate, ok := in.ProviderSuccessRate[ProviderModelKey(p.Name, "", model)]; ok {
				providerSuccessRate = rate
			}
		}
		latencyMS := in.ObservedLatencyMS[key]
		if latencyMS == 0 && p.EndpointName != "" {
			latencyMS = in.ObservedLatencyMS[ProviderModelKey(p.Name, "", model)]
		}

		eligible := true
		var filterReason FilterReason
		if reason == "" {
			if g, fr := CheckGating(entryCaps, req); g != "" {
				eligible = false
				reason = g
				filterReason = fr
			}
		} else {
			eligible = false
			// resolveModel rejection — model resolution is a capability
			// mismatch with no specific public category, so fall through
			// to the catch-all.
			filterReason = FilterReasonScoredBelowTop
		}

		// Hard preference filtering.
		if eligible {
			switch req.ProviderPreference {
			case ProviderPreferenceLocalOnly:
				if !h.IsLocal {
					eligible = false
					reason = "preference is local-only"
					filterReason = FilterReasonUnhealthy
				}
			case ProviderPreferenceSubscriptionOnly:
				if !h.IsSubscription {
					eligible = false
					reason = "preference is subscription-only"
					filterReason = FilterReasonUnhealthy
				}
			}
		}

		// SubscriptionOK hard gate: a subscription harness with SubscriptionOK=false
		// (no durable cache, quota cache missing, or routing decision says no)
		// is ineligible regardless of score.
		if eligible && h.IsSubscription && !h.SubscriptionOK {
			eligible = false
			reason = "subscription quota exhausted"
			filterReason = FilterReasonUnhealthy
		}

		if eligible && req.Provider != "" && p.Name != "" && req.Provider != p.Name && req.Harness != "" {
			// Hard provider pin under explicit harness: reject other providers.
			eligible = false
			reason = fmt.Sprintf("provider override requires %s", req.Provider)
			filterReason = FilterReasonScoredBelowTop
		}

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
			EndpointName:          p.EndpointName,
			Model:                 model,
			CostClass:             h.CostClass,
			CostUSDPer1kTokens:    p.CostUSDPer1kTokens,
			CostSource:            normalizeCostSource(p.CostSource),
			IsSubscription:        h.IsSubscription,
			QuotaOK:               h.QuotaOK,
			QuotaPercentUsed:      h.QuotaPercentUsed,
			QuotaStale:            h.QuotaStale,
			QuotaTrend:            h.QuotaTrend,
			SubscriptionOK:        h.SubscriptionOK,
			HistoricalSuccessRate: histRate,
			ProviderSuccessRate:   providerSuccessRate,
			ObservedTokensPerSec:  obs,
			ObservedLatencyMS:     latencyMS,
			InCooldown:            inCooldown || h.InCooldown,
			ProviderAffinityMatch: req.Provider != "" && p.Name != "" && req.Provider == p.Name,
			ProviderPreference:    req.ProviderPreference,
		}
		out = append(out, rankedCandidate{
			out: Candidate{
				Harness:            h.Name,
				Provider:           p.Name,
				Endpoint:           p.EndpointName,
				Model:              model,
				CostUSDPer1kTokens: p.CostUSDPer1kTokens,
				CostSource:         normalizeCostSource(p.CostSource),
				Eligible:           eligible,
				Reason:             reason,
				FilterReason:       filterReason,
				LatencyMS:          latencyMS,
				SuccessRate:        providerSuccessRate,
				CostClass:          h.CostClass,
			},
			internal: ci,
		})
	}
	return out
}

func normalizeCostSource(source string) string {
	switch source {
	case CostSourceCatalog, CostSourceSubscription, CostSourceUserConfig:
		return source
	default:
		return CostSourceUnknown
	}
}

func neutralKnownCost(candidates []rankedCandidate) (float64, bool) {
	var total float64
	var count int
	for _, candidate := range candidates {
		if !candidate.out.Eligible {
			continue
		}
		if normalizeCostSource(candidate.out.CostSource) == CostSourceUnknown {
			continue
		}
		total += candidate.out.CostUSDPer1kTokens
		count++
	}
	if count == 0 {
		return 0, false
	}
	return total / float64(count), true
}

func candidateCostTieValue(candidate rankedCandidate, neutralCost float64) float64 {
	if normalizeCostSource(candidate.out.CostSource) == CostSourceUnknown {
		return neutralCost
	}
	return candidate.out.CostUSDPer1kTokens
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
			return "", fmt.Sprintf("model %q not on provider %q", req.Model, p.Name)
		}
		if p.DiscoveryAttempted {
			return "", fmt.Sprintf("provider %q has no live discovered models", p.Name)
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
					return "", fmt.Sprintf("model ref %q not on provider %q", req.ModelRef, p.Name)
				}
				return concrete, ""
			}
			return "", fmt.Sprintf("model ref %q not available on surface %q", req.ModelRef, h.Surface)
		}
		if p.DiscoveryAttempted {
			return "", fmt.Sprintf("provider %q has no live discovered models", p.Name)
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
		} else if p.DefaultModel == "" && h.DefaultModel == "" {
			return "", fmt.Sprintf("profile %q not available on surface %q", req.Profile, h.Surface)
		}
		// No catalog hit; fall through to default.
	}

	// 4. Provider default → harness default. Empty default is acceptable
	// when no request fields constrained model selection — orphan validation
	// happens at dispatch time.
	if p.DefaultModel != "" {
		if len(p.DiscoveredIDs) > 0 {
			if matched := FuzzyMatch(p.DefaultModel, p.DiscoveredIDs); matched != "" {
				return matched, ""
			}
			return "", fmt.Sprintf("provider default %q not on provider %q", p.DefaultModel, p.Name)
		}
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
