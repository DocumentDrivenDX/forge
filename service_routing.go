package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	geminiharness "github.com/DocumentDrivenDX/agent/internal/harnesses/gemini"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/routing"
)

var loadRoutingCatalog = modelcatalog.Default

// ResolveRoute resolves an under-specified RouteRequest to a concrete
// (Harness, Provider, Model) decision per CONTRACT-003.
//
// The implementation delegates to internal/routing.Resolve — the single
// routing engine that consolidates DDx-side harness-tier ranking and
// agent-side provider failover ordering.
func (s *service) ResolveRoute(ctx context.Context, req RouteRequest) (*RouteDecision, error) {
	s.ensurePrimaryQuotaRefresh(ctx, quotaRefreshAsync)
	cat := serviceRoutingCatalog()
	profile := req.Profile
	if profile == "" {
		profile = reqProfileFromModelRef(cat, req.ModelRef)
	}
	modelRef := reqModelRefStripProfile(cat, req.ModelRef)
	providerPreference, err := providerPreferenceForProfile(cat, profile)
	if err != nil {
		return &RouteDecision{}, err
	}
	in := s.buildRoutingInputsWithCatalog(ctx, cat)

	rReq := routing.Request{
		Profile:            profile,
		ModelRef:           modelRef,
		Model:              req.Model,
		Provider:           req.Provider,
		Harness:            req.Harness,
		Reasoning:          effectiveReasoningString(req.Reasoning),
		Permissions:        req.Permissions,
		ProviderPreference: providerPreference,
	}
	s.applyRouteAttemptCooldowns(&in)
	dec, err := routing.Resolve(rReq, in)
	result := routeDecisionFromInternal(dec)
	if err != nil {
		if result == nil {
			result = &RouteDecision{}
		}
		return result, publicRoutingError(err, result.Candidates)
	}
	// Cache the decision so RouteStatus can surface LastDecision.
	s.cacheRouteDecision(req.Model, result)
	return result, nil
}

func routeDecisionFromInternal(dec *routing.Decision) *RouteDecision {
	if dec == nil {
		return nil
	}
	return &RouteDecision{
		Harness:    dec.Harness,
		Provider:   dec.Provider,
		Endpoint:   dec.Endpoint,
		Model:      dec.Model,
		Reason:     dec.Reason,
		Candidates: routeCandidatesFromInternal(dec.Candidates),
	}
}

func routeCandidatesFromInternal(candidates []routing.Candidate) []RouteCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]RouteCandidate, len(candidates))
	for i, candidate := range candidates {
		out[i] = routeCandidateFromInternal(candidate)
	}
	return out
}

func routeCandidateFromInternal(candidate routing.Candidate) RouteCandidate {
	return RouteCandidate{
		Harness:            candidate.Harness,
		Provider:           candidate.Provider,
		Endpoint:           candidate.Endpoint,
		Model:              candidate.Model,
		Score:              candidate.Score,
		CostUSDPer1kTokens: candidate.CostUSDPer1kTokens,
		CostSource:         candidate.CostSource,
		Eligible:           candidate.Eligible,
		Reason:             candidate.Reason,
	}
}

func publicRoutingError(err error, candidates []RouteCandidate) error {
	var modelErr *routing.ErrHarnessModelIncompatible
	if errors.As(err, &modelErr) {
		return withRouteCandidates(&ErrHarnessModelIncompatible{
			Harness:         modelErr.Harness,
			Model:           modelErr.Model,
			SupportedModels: append([]string(nil), modelErr.SupportedModels...),
		}, candidates)
	}
	var profileErr *routing.ErrProfilePinConflict
	if errors.As(err, &profileErr) {
		return withRouteCandidates(&ErrProfilePinConflict{
			Profile:           profileErr.Profile,
			ConflictingPin:    profileErr.ConflictingPin,
			ProfileConstraint: profileErr.ProfileConstraint,
		}, candidates)
	}
	var noProfileErr *routing.ErrNoProfileCandidate
	if errors.As(err, &noProfileErr) {
		return withRouteCandidates(&ErrNoProfileCandidate{
			Profile:           noProfileErr.Profile,
			MissingCapability: noProfileErr.MissingCapability,
			Rejected:          noProfileErr.Rejected,
		}, candidates)
	}
	return withRouteCandidates(err, candidates)
}

func withRouteCandidates(err error, candidates []RouteCandidate) error {
	if err == nil || len(candidates) == 0 {
		return err
	}
	return &routeDecisionError{
		err:        err,
		candidates: append([]RouteCandidate(nil), candidates...),
	}
}

func (s *service) applyRouteAttemptCooldowns(in *routing.Inputs) {
	if in == nil {
		return
	}
	ttl := s.routeAttemptTTL()
	records := s.activeRouteAttempts(time.Now(), ttl)
	if len(records) == 0 {
		return
	}
	if in.ProviderCooldowns == nil {
		in.ProviderCooldowns = make(map[string]time.Time)
	}
	if in.CooldownDuration <= 0 {
		in.CooldownDuration = ttl
	}
	for _, record := range records {
		if record.key.Provider != "" {
			existing, ok := in.ProviderCooldowns[record.key.Provider]
			if !ok || record.recordedAt.After(existing) {
				in.ProviderCooldowns[record.key.Provider] = record.recordedAt
			}
		}
		if record.key.Provider == "" && record.key.Harness != "" {
			for i := range in.Harnesses {
				if in.Harnesses[i].Name == record.key.Harness {
					in.Harnesses[i].InCooldown = true
				}
			}
		}
	}
}

func (s *service) routeAttemptTTL() time.Duration {
	if s.opts.ServiceConfig == nil {
		return defaultRouteAttemptCooldown
	}
	ttl := s.opts.ServiceConfig.HealthCooldown()
	if ttl <= 0 {
		return defaultRouteAttemptCooldown
	}
	return ttl
}

// reqProfileFromModelRef returns ref when ref is a known profile alias,
// or "" otherwise. The contract puts ModelRef and Profile in the same field.
func reqProfileFromModelRef(cat *modelcatalog.Catalog, ref string) string {
	if cat == nil {
		return ""
	}
	if _, ok := cat.Profile(ref); ok {
		return ref
	}
	return ""
}

// reqModelRefStripProfile returns "" when ref is a known profile alias,
// or ref otherwise.
func reqModelRefStripProfile(cat *modelcatalog.Catalog, ref string) string {
	if cat == nil {
		return ref
	}
	if _, ok := cat.Profile(ref); ok {
		return ""
	}
	return ref
}

// buildRoutingInputs assembles routing.Inputs from the service's registry
// and ServiceConfig. When the service has a catalog cache attached (v0.9.2+),
// each configured provider's ProviderEntry is populated with DiscoveredIDs
// from the cache's live /v1/models probe, so routing.FuzzyMatch matches the
// request against IDs the server actually serves rather than the configured
// default-model string.
//
// ctx is used for cache probes with a short deadline; the cache's
// stale-while-revalidate flow makes most calls non-blocking.
func (s *service) buildRoutingInputs(ctx context.Context) routing.Inputs {
	return s.buildRoutingInputsWithCatalog(ctx, serviceRoutingCatalog())
}

func (s *service) buildRoutingInputsWithCatalog(ctx context.Context, cat *modelcatalog.Catalog) routing.Inputs {
	statuses := s.registry.Discover()
	statusByName := make(map[string]harnesses.HarnessStatus, len(statuses))
	for _, st := range statuses {
		statusByName[st.Name] = st
	}

	var entries []routing.HarnessEntry
	for _, name := range s.registry.Names() {
		cfg, ok := s.registry.Get(name)
		if !ok {
			continue
		}
		st := statusByName[name]
		entry := routing.HarnessEntry{
			Name:                name,
			Surface:             cfg.Surface,
			CostClass:           cfg.CostClass,
			IsLocal:             cfg.IsLocal,
			IsSubscription:      cfg.IsSubscription,
			IsHTTPProvider:      cfg.IsHTTPProvider,
			AutoRoutingEligible: cfg.AutoRoutingEligible,
			TestOnly:            cfg.TestOnly,
			ExactPinSupport:     cfg.ExactPinSupport,
			DefaultModel:        cfg.DefaultModel,
			SupportedModels:     append([]string(nil), cfg.Models...),
			SupportedReasoning:  supportedReasoning(cfg),
			MaxReasoningTokens:  cfg.MaxReasoningTokens,
			SupportedPerms:      supportedPermissions(cfg),
			SupportsTools:       true, // all builtin harnesses support tools today
			Available:           st.Available,
			QuotaOK:             true,
			QuotaTrend:          routing.QuotaTrendUnknown,
			// SubscriptionOK defaults to true and is refined by subscription
			// harness quota caches below.
			SubscriptionOK: true,
		}
		if name == "agent" && s.opts.ServiceConfig == nil {
			entry.AutoRoutingEligible = false
		}

		if name == "claude" {
			dec := claudeharness.ReadClaudeQuotaRoutingDecision(time.Now(), 0)
			entry.QuotaOK = dec.PreferClaude
			entry.QuotaStale = !dec.Fresh && dec.SnapshotPresent
			entry.SubscriptionOK = dec.PreferClaude
			if dec.Snapshot != nil {
				maxUsed := 0.0
				if dec.Snapshot.FiveHourLimit > 0 {
					maxUsed = float64(dec.Snapshot.FiveHourLimit-dec.Snapshot.FiveHourRemaining) / float64(dec.Snapshot.FiveHourLimit) * 100
				}
				if dec.Snapshot.WeeklyLimit > 0 {
					weeklyUsed := float64(dec.Snapshot.WeeklyLimit-dec.Snapshot.WeeklyRemaining) / float64(dec.Snapshot.WeeklyLimit) * 100
					if weeklyUsed > maxUsed {
						maxUsed = weeklyUsed
					}
				}
				entry.QuotaPercentUsed = int(maxUsed)
				if maxUsed >= 90 {
					entry.QuotaTrend = routing.QuotaTrendExhausting
				} else if maxUsed >= 70 {
					entry.QuotaTrend = routing.QuotaTrendBurning
				} else if dec.Fresh {
					entry.QuotaTrend = routing.QuotaTrendHealthy
				}
			}
		}

		if name == "codex" {
			dec := codexharness.ReadCodexQuotaRoutingDecision(time.Now(), 0)
			entry.QuotaOK = dec.PreferCodex
			entry.QuotaStale = !dec.Fresh && dec.SnapshotPresent
			entry.SubscriptionOK = dec.PreferCodex
			if dec.Snapshot != nil {
				maxUsed := 0.0
				for _, window := range dec.Snapshot.Windows {
					if window.UsedPercent > maxUsed {
						maxUsed = window.UsedPercent
					}
				}
				entry.QuotaPercentUsed = int(maxUsed)
				if maxUsed >= 90 {
					entry.QuotaTrend = routing.QuotaTrendExhausting
				} else if maxUsed >= 70 {
					entry.QuotaTrend = routing.QuotaTrendBurning
				} else if dec.Fresh {
					entry.QuotaTrend = routing.QuotaTrendHealthy
				}
			}
		}

		if name == "gemini" {
			auth := geminiharness.ReadAuthEvidence(time.Now())
			entry.QuotaOK = auth.Authenticated && auth.Fresh
			entry.QuotaStale = auth.Authenticated && !auth.Fresh
			entry.SubscriptionOK = auth.Authenticated && auth.Fresh
			if auth.Authenticated && auth.Fresh {
				entry.QuotaTrend = routing.QuotaTrendUnknown
			}
		}

		// Native "agent" harness: enumerate live configured provider endpoints.
		if name == "agent" && s.opts.ServiceConfig != nil {
			for _, pname := range s.opts.ServiceConfig.ProviderNames() {
				pcfg, ok := s.opts.ServiceConfig.Provider(pname)
				if !ok {
					continue
				}
				entry.Providers = append(entry.Providers, s.liveProviderEntries(ctx, pname, pcfg, cat)...)
			}
		}
		s.applySubscriptionRoutingCost(&entry, cat)
		entries = append(entries, entry)
	}
	successRate, latencyMS := s.routeMetricSignals(time.Now(), s.routeAttemptTTL())
	return routing.Inputs{
		Harnesses:           entries,
		ProviderSuccessRate: successRate,
		ObservedLatencyMS:   latencyMS,
		CatalogResolver:     serviceRoutingCatalogResolver(cat),
	}
}

func serviceRoutingCatalog() *modelcatalog.Catalog {
	cat, err := loadRoutingCatalog()
	if err != nil || cat == nil {
		return nil
	}
	return cat
}

func serviceRoutingCatalogResolver(cat *modelcatalog.Catalog) func(ref, surface string) (string, bool) {
	if cat == nil {
		return nil
	}
	return func(ref, surface string) (string, bool) {
		catalogSurface, ok := serviceRoutingCatalogSurface(surface)
		if !ok {
			return "", false
		}
		resolved, err := cat.Resolve(ref, modelcatalog.ResolveOptions{
			Surface:         catalogSurface,
			AllowDeprecated: true,
		})
		if err != nil || resolved.ConcreteModel == "" {
			return "", false
		}
		return resolved.ConcreteModel, true
	}
}

func serviceRoutingCatalogSurface(surface string) (modelcatalog.Surface, bool) {
	switch surface {
	case "embedded-openai":
		return modelcatalog.SurfaceAgentOpenAI, true
	case "embedded-anthropic":
		return modelcatalog.SurfaceAgentAnthropic, true
	case "codex":
		return modelcatalog.SurfaceCodex, true
	case "claude":
		return modelcatalog.SurfaceClaudeCode, true
	case "gemini":
		return modelcatalog.SurfaceGemini, true
	default:
		return "", false
	}
}

func (s *service) liveProviderEntries(ctx context.Context, providerName string, pcfg ServiceProviderEntry, cat *modelcatalog.Catalog) []routing.ProviderEntry {
	if providerUsesLiveDiscovery(pcfg.Type) && s.catalog != nil {
		endpoints := modelDiscoveryEndpoints(pcfg)
		out := make([]routing.ProviderEntry, 0, len(endpoints))
		for _, endpoint := range endpoints {
			ids, ok := s.probeEndpointDiscoveredIDs(ctx, pcfg, endpoint.BaseURL)
			if !ok || len(ids) == 0 {
				continue
			}
			routeName := providerName
			if len(endpoints) > 1 {
				routeName = endpointProviderRef(providerName, endpoint.Name)
			}
			entry := routing.ProviderEntry{
				Name:               routeName,
				BaseURL:            endpoint.BaseURL,
				EndpointName:       endpoint.Name,
				EndpointBaseURL:    endpoint.BaseURL,
				DefaultModel:       pcfg.Model,
				DiscoveredIDs:      ids,
				DiscoveryAttempted: true,
				SupportsTools:      true,
			}
			s.applyEndpointRoutingCost(&entry, pcfg, cat)
			out = append(out, entry)
		}
		return out
	}
	entry := routing.ProviderEntry{
		Name:          providerName,
		BaseURL:       pcfg.BaseURL,
		DefaultModel:  pcfg.Model,
		SupportsTools: true,
	}
	s.applyEndpointRoutingCost(&entry, pcfg, cat)
	return []routing.ProviderEntry{entry}
}

func providerUsesLiveDiscovery(providerType string) bool {
	switch normalizeServiceProviderType(providerType) {
	case "openai", "openrouter", "lmstudio", "omlx", "ollama", "minimax", "qwen", "zai":
		return true
	default:
		return false
	}
}

func (s *service) applyEndpointRoutingCost(entry *routing.ProviderEntry, pcfg ServiceProviderEntry, cat *modelcatalog.Catalog) {
	if entry == nil {
		return
	}
	if providerTypeIsLocalEndpoint(pcfg.Type) {
		if s.opts.LocalCostUSDPer1kTokens > 0 {
			entry.CostUSDPer1kTokens = s.opts.LocalCostUSDPer1kTokens
			entry.CostSource = routing.CostSourceUserConfig
		} else {
			entry.CostUSDPer1kTokens = 0
			entry.CostSource = routing.CostSourceUnknown
		}
		return
	}
	if cost, ok := catalogCostUSDPer1kTokens(cat, entry.DefaultModel); ok {
		entry.CostUSDPer1kTokens = cost
		entry.CostSource = routing.CostSourceCatalog
		return
	}
	entry.CostUSDPer1kTokens = 0
	entry.CostSource = routing.CostSourceUnknown
}

func (s *service) applySubscriptionRoutingCost(entry *routing.HarnessEntry, cat *modelcatalog.Catalog) {
	if entry == nil || !entry.IsSubscription {
		return
	}
	baseCost, ok := catalogCostUSDPer1kTokens(cat, entry.DefaultModel)
	if !ok {
		baseCost, ok = catalogCostUSDPer1kTokens(cat, subscriptionFallbackProfile(entry.Name))
		if !ok {
			baseCost = 0
		}
	}
	cost := subscriptionEffectiveCostUSDPer1kTokens(baseCost, entry.QuotaPercentUsed, s.subscriptionCostCurve())
	entry.Providers = []routing.ProviderEntry{{
		CostUSDPer1kTokens: cost,
		CostSource:         routing.CostSourceSubscription,
	}}
}

func providerTypeIsLocalEndpoint(providerType string) bool {
	switch normalizeServiceProviderType(providerType) {
	case "lmstudio", "omlx", "ollama":
		return true
	default:
		return false
	}
}

func subscriptionFallbackProfile(harnessName string) string {
	switch harnessName {
	case "claude", "codex", "gemini":
		return "standard"
	default:
		return ""
	}
}

func catalogCostUSDPer1kTokens(cat *modelcatalog.Catalog, modelID string) (float64, bool) {
	if cat == nil || strings.TrimSpace(modelID) == "" {
		return 0, false
	}
	entry, ok := cat.LookupModel(modelID)
	if !ok {
		resolved := resolveCatalogCostModel(cat, modelID)
		if resolved == "" {
			return 0, false
		}
		entry, ok = cat.LookupModel(resolved)
		if !ok {
			return 0, false
		}
	}
	input := entry.CostInputPerM
	if input == 0 {
		input = entry.CostInputPerMTok
	}
	output := entry.CostOutputPerM
	if output == 0 {
		output = entry.CostOutputPerMTok
	}
	switch {
	case input > 0 && output > 0:
		return ((input + output) / 2) / 1000, true
	case input > 0:
		return input / 1000, true
	case output > 0:
		return output / 1000, true
	default:
		return 0, false
	}
}

func resolveCatalogCostModel(cat *modelcatalog.Catalog, ref string) string {
	for _, surface := range []modelcatalog.Surface{
		modelcatalog.SurfaceAgentOpenAI,
		modelcatalog.SurfaceAgentAnthropic,
		modelcatalog.SurfaceCodex,
		modelcatalog.SurfaceClaudeCode,
		modelcatalog.SurfaceGemini,
	} {
		resolved, err := cat.Resolve(ref, modelcatalog.ResolveOptions{
			Surface:         surface,
			AllowDeprecated: true,
		})
		if err == nil && resolved.ConcreteModel != "" {
			return resolved.ConcreteModel
		}
	}
	return ""
}

func (s *service) subscriptionCostCurve() SubscriptionCostCurve {
	if s.opts.SubscriptionCostCurve == nil {
		return defaultSubscriptionCostCurve()
	}
	curve := *s.opts.SubscriptionCostCurve
	def := defaultSubscriptionCostCurve()
	if curve.FreeUntilPercent == 0 {
		curve.FreeUntilPercent = def.FreeUntilPercent
	}
	if curve.LowUntilPercent == 0 {
		curve.LowUntilPercent = def.LowUntilPercent
	}
	if curve.MediumUntilPercent == 0 {
		curve.MediumUntilPercent = def.MediumUntilPercent
	}
	if curve.LowMultiplier == 0 {
		curve.LowMultiplier = def.LowMultiplier
	}
	if curve.MediumMultiplier == 0 {
		curve.MediumMultiplier = def.MediumMultiplier
	}
	if curve.HighMultiplier == 0 {
		curve.HighMultiplier = def.HighMultiplier
	}
	return curve
}

func defaultSubscriptionCostCurve() SubscriptionCostCurve {
	return SubscriptionCostCurve{
		FreeUntilPercent:   70,
		LowUntilPercent:    80,
		MediumUntilPercent: 90,
		LowMultiplier:      0.1,
		MediumMultiplier:   0.3,
		HighMultiplier:     1.2,
	}
}

func subscriptionEffectiveCostUSDPer1kTokens(baseCost float64, quotaPercentUsed int, curve SubscriptionCostCurve) float64 {
	switch {
	case quotaPercentUsed <= curve.FreeUntilPercent:
		return 0
	case quotaPercentUsed <= curve.LowUntilPercent:
		return baseCost * curve.LowMultiplier
	case quotaPercentUsed <= curve.MediumUntilPercent:
		return baseCost * curve.MediumMultiplier
	default:
		return baseCost * curve.HighMultiplier
	}
}

// probeEndpointDiscoveredIDs returns the foreground /v1/models catalog for one
// endpoint. Dispatch routing must not use stale cache hits: a recently dead
// endpoint has to be skipped before any chat request is attempted.
func (s *service) probeEndpointDiscoveredIDs(ctx context.Context, pcfg ServiceProviderEntry, baseURL string) ([]string, bool) {
	if baseURL == "" {
		return nil, false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ids, err := probeOpenAIModels(probeCtx, baseURL, pcfg.APIKey)
	if err != nil || len(ids) == 0 {
		return nil, false
	}
	return ids, true
}

// resolveExecuteRouteWithEngine is the post-engine variant of resolveExecuteRoute.
// It is invoked by Execute when the request is under-specified
// (no PreResolved, no fully-specified Harness). Returns nil when the request
// is already specific enough that the legacy resolveExecuteRoute path applies.
func (s *service) resolveExecuteRouteWithEngine(req ServiceExecuteRequest) (*RouteDecision, error) {
	rr := RouteRequest{
		Profile:     req.Profile,
		Model:       req.Model,
		Provider:    req.Provider,
		Harness:     req.Harness,
		ModelRef:    req.ModelRef,
		Reasoning:   req.Reasoning,
		Permissions: req.Permissions,
	}
	dec, err := s.ResolveRoute(context.Background(), rr)
	if err != nil {
		if isExplicitPinError(err) {
			return nil, err
		}
		return nil, fmt.Errorf("ResolveRoute: %w", err)
	}
	return dec, nil
}

func providerPreferenceForProfile(cat *modelcatalog.Catalog, profile string) (string, error) {
	if profile == "" {
		return routing.ProviderPreferenceLocalFirst, nil
	}
	if cat == nil {
		return "", &ErrUnknownProfile{Profile: profile}
	}
	info, ok := cat.Profile(profile)
	if !ok {
		return "", &ErrUnknownProfile{Profile: profile}
	}
	switch info.ProviderPreference {
	case routing.ProviderPreferenceLocalOnly, routing.ProviderPreferenceSubscriptionOnly,
		routing.ProviderPreferenceLocalFirst, routing.ProviderPreferenceSubscriptionFirst:
		return info.ProviderPreference, nil
	default:
		return "", fmt.Errorf("profile %q has unsupported provider preference %q", profile, info.ProviderPreference)
	}
}
