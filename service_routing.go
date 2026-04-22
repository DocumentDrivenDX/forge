package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	geminiharness "github.com/DocumentDrivenDX/agent/internal/harnesses/gemini"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/routing"
)

// ResolveRoute resolves an under-specified RouteRequest to a concrete
// (Harness, Provider, Model) decision per CONTRACT-003.
//
// The implementation delegates to internal/routing.Resolve — the single
// routing engine that consolidates DDx-side harness-tier ranking and
// agent-side provider failover ordering.
func (s *service) ResolveRoute(ctx context.Context, req RouteRequest) (*RouteDecision, error) {
	s.ensurePrimaryQuotaRefresh(ctx, quotaRefreshAsync)
	in := s.buildRoutingInputs(ctx)
	profile := req.Profile
	if profile == "" {
		profile = reqProfileFromModelRef(req.ModelRef)
	}

	rReq := routing.Request{
		Profile:            profile,
		ModelRef:           reqModelRefStripProfile(req.ModelRef),
		Model:              req.Model,
		Provider:           req.Provider,
		Harness:            req.Harness,
		Reasoning:          effectiveReasoningString(req.Reasoning),
		Permissions:        req.Permissions,
		ProviderPreference: providerPreferenceForProfile(profile),
	}
	s.applyRouteAttemptCooldowns(&in)
	dec, err := routing.Resolve(rReq, in)
	if err != nil {
		return nil, err
	}
	result := &RouteDecision{
		Harness:  dec.Harness,
		Provider: dec.Provider,
		Endpoint: dec.Endpoint,
		Model:    dec.Model,
		Reason:   dec.Reason,
	}
	// Cache the decision so RouteStatus can surface LastDecision.
	s.cacheRouteDecision(req.Model, result)
	return result, nil
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
func reqProfileFromModelRef(ref string) string {
	switch ref {
	case "cheap", "standard", "smart":
		return ref
	}
	return ""
}

// reqModelRefStripProfile returns "" when ref is a known profile alias,
// or ref otherwise.
func reqModelRefStripProfile(ref string) string {
	switch ref {
	case "cheap", "standard", "smart":
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
				entry.Providers = append(entry.Providers, s.liveProviderEntries(ctx, pname, pcfg)...)
			}
		}
		entries = append(entries, entry)
	}
	successRate, latencyMS := s.routeMetricSignals(time.Now(), s.routeAttemptTTL())
	return routing.Inputs{
		Harnesses:           entries,
		ProviderSuccessRate: successRate,
		ObservedLatencyMS:   latencyMS,
		CatalogResolver:     serviceRoutingCatalogResolver(),
	}
}

func serviceRoutingCatalogResolver() func(ref, surface string) (string, bool) {
	cat, err := modelcatalog.Default()
	if err != nil || cat == nil {
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

func (s *service) liveProviderEntries(ctx context.Context, providerName string, pcfg ServiceProviderEntry) []routing.ProviderEntry {
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
			out = append(out, routing.ProviderEntry{
				Name:               routeName,
				BaseURL:            endpoint.BaseURL,
				EndpointName:       endpoint.Name,
				EndpointBaseURL:    endpoint.BaseURL,
				DefaultModel:       pcfg.Model,
				DiscoveredIDs:      ids,
				DiscoveryAttempted: true,
				SupportsTools:      true,
			})
		}
		return out
	}
	return []routing.ProviderEntry{{
		Name:          providerName,
		BaseURL:       pcfg.BaseURL,
		DefaultModel:  pcfg.Model,
		SupportsTools: true,
	}}
}

func providerUsesLiveDiscovery(providerType string) bool {
	switch normalizeServiceProviderType(providerType) {
	case "openai", "openrouter", "lmstudio", "omlx", "ollama", "minimax", "qwen", "zai":
		return true
	default:
		return false
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
		return nil, fmt.Errorf("ResolveRoute: %w", err)
	}
	return dec, nil
}

func providerPreferenceForProfile(profile string) string {
	switch profile {
	case "offline", "air-gapped":
		return routing.ProviderPreferenceLocalOnly
	case "smart", "code-high":
		return routing.ProviderPreferenceSubscriptionFirst
	default:
		return routing.ProviderPreferenceLocalFirst
	}
}
