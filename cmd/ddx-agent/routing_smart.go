package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent"
	agentConfig "github.com/DocumentDrivenDX/agent/config"
	"github.com/DocumentDrivenDX/agent/modelcatalog"
	"github.com/DocumentDrivenDX/agent/observations"
	oaiProvider "github.com/DocumentDrivenDX/agent/provider/openai"
	"github.com/DocumentDrivenDX/agent/session"
)

const (
	defaultRoutingHistoryWindow = 24 * time.Hour
	defaultRoutingProbeTimeout  = 3 * time.Second
	defaultRoutingLoadWindow    = 15 * time.Minute
)

type smartRouteHistory struct {
	Samples              int
	Successes            int
	Failures             int
	AvgDurationMs        float64
	AvgOutputTokensPerS  float64
	AvgKnownCostPer1KTok *float64
	RecentSelections     int
	LastSelectedAt       time.Time
}

func (h smartRouteHistory) ReliabilityScore() float64 {
	if h.Samples == 0 {
		return 0.5
	}
	return float64(h.Successes) / float64(h.Samples)
}

type smartRouteCandidate struct {
	Provider              string    `json:"provider"`
	Model                 string    `json:"model,omitempty"`
	Healthy               bool      `json:"healthy"`
	Reason                string    `json:"reason,omitempty"`
	Priority              int       `json:"priority,omitempty"`
	Reliability           float64   `json:"reliability"`
	AvgDurationMs         float64   `json:"avg_duration_ms,omitempty"`
	OutputTokensPerS      float64   `json:"output_tokens_per_second,omitempty"`
	RecentSelections      int       `json:"recent_selections,omitempty"`
	AvgCostPer1KTok       *float64  `json:"avg_cost_per_1k_tokens,omitempty"`
	Score                 float64   `json:"score,omitempty"`
	LastSelectedAt        time.Time `json:"last_selected_at,omitempty"`
	SWEBenchVerified      float64   `json:"swe_bench_verified,omitempty"`
	ObservedTokensPerSec  float64   `json:"observed_tokens_per_sec,omitempty"`
	CapabilityScore       float64   `json:"capability_score,omitempty"`
}

type smartRoutePlan struct {
	RouteKey          string                `json:"route_key"`
	RequestedModel    string                `json:"requested_model,omitempty"`
	RequestedModelRef string                `json:"requested_model_ref,omitempty"`
	Strategy          string                `json:"strategy"`
	Candidates        []smartRouteCandidate `json:"candidates"`
	Order             []int                 `json:"-"`
}

type providerModelProbe struct {
	models []string
	err    error
}

func (p providerModelProbe) available() bool {
	return p.err == nil
}

func routingHistoryWindow(cfg *agentConfig.Config) time.Duration {
	if cfg != nil && strings.TrimSpace(cfg.Routing.HistoryWindow) != "" {
		if d, err := time.ParseDuration(cfg.Routing.HistoryWindow); err == nil && d > 0 {
			return d
		}
	}
	return defaultRoutingHistoryWindow
}

func routingProbeTimeout(cfg *agentConfig.Config) time.Duration {
	if cfg != nil && strings.TrimSpace(cfg.Routing.ProbeTimeout) != "" {
		if d, err := time.ParseDuration(cfg.Routing.ProbeTimeout); err == nil && d > 0 {
			return d
		}
	}
	return defaultRoutingProbeTimeout
}

func routingWeights(cfg *agentConfig.Config) (reliability, performance, load, cost, capability float64) {
	reliability = 0.35
	performance = 0.20
	load = 0.15
	cost = 0.20
	capability = 0.10
	if cfg == nil {
		return
	}
	if cfg.Routing.ReliabilityWeight > 0 {
		reliability = cfg.Routing.ReliabilityWeight
	}
	if cfg.Routing.PerformanceWeight > 0 {
		performance = cfg.Routing.PerformanceWeight
	}
	if cfg.Routing.LoadWeight > 0 {
		load = cfg.Routing.LoadWeight
	}
	if cfg.Routing.CostWeight > 0 {
		cost = cfg.Routing.CostWeight
	}
	if cfg.Routing.CapabilityWeight > 0 {
		capability = cfg.Routing.CapabilityWeight
	}
	total := reliability + performance + load + cost + capability
	if total <= 0 {
		return 0.35, 0.20, 0.15, 0.20, 0.10
	}
	return reliability / total, performance / total, load / total, cost / total, capability / total
}

func buildSmartRoutePlan(cfg *agentConfig.Config, workDir, routeKey, routeModelRef string, allowDeprecated bool, explicitRoute *agentConfig.ModelRouteConfig) (smartRoutePlan, error) {
	now := time.Now().UTC()
	plan := smartRoutePlan{
		RouteKey:          routeKey,
		RequestedModel:    routeKey,
		RequestedModelRef: routeModelRef,
		Strategy:          "smart",
	}

	var route agentConfig.ModelRouteConfig
	if explicitRoute != nil {
		route = *explicitRoute
		if strings.TrimSpace(route.Strategy) != "" {
			plan.Strategy = route.Strategy
		}
	} else {
		route = synthesizeIntentRoute(cfg, routeKey, routeModelRef)
	}

	history, err := readSmartRoutingHistory(sessionLogDir(workDir, cfg), routeKey, now, routingHistoryWindow(cfg))
	if err != nil {
		return plan, err
	}
	healthState, err := loadRouteHealthState(workDir, routeKey)
	if err != nil {
		healthState = routeHealthState{Failures: make(map[string]time.Time)}
	}
	counter, err := readAndIncrementRouteCounter(workDir, routeKey)
	if err != nil {
		counter = 0
	}

	cat, _ := modelcatalog.Load(modelcatalog.LoadOptions{})
	obs, _ := observations.LoadStore(observations.DefaultStorePath())

	modelProbeCache := make(map[string]providerModelProbe)
	plan.Candidates = make([]smartRouteCandidate, 0, len(route.Candidates))
	finalCandidates := make([]agentConfig.ModelRouteCandidateConfig, 0, len(route.Candidates))
	for _, candidate := range route.Candidates {
		inspected, resolvedCandidate := inspectSmartRouteCandidate(cfg, routeKey, routeModelRef, allowDeprecated, candidate, history[candidate.Provider], healthState, modelProbeCache, cat, obs)
		plan.Candidates = append(plan.Candidates, inspected)
		finalCandidates = append(finalCandidates, resolvedCandidate)
	}
	if len(finalCandidates) == 0 {
		return plan, fmt.Errorf("config: no routing candidates available for model %q", routeKey)
	}
	route.Candidates = finalCandidates

	if strings.TrimSpace(route.Strategy) == "smart" || explicitRoute == nil {
		order := scoreSmartRouteCandidates(&plan, counter, cfg)
		if len(order) == 0 {
			return plan, fmt.Errorf("config: no healthy providers available for model %q", routeKey)
		}
		plan.Order = order
		return plan, nil
	}

	order := routeAttemptOrder(route, counter, healthState, routeHealthCooldown(cfg))
	if len(order) == 0 {
		return plan, fmt.Errorf("config: no route candidates available for model %q", routeKey)
	}
	plan.Order = order
	return plan, nil
}

func synthesizeIntentRoute(cfg *agentConfig.Config, requestedModel, requestedModelRef string) agentConfig.ModelRouteConfig {
	candidates := make([]agentConfig.ModelRouteCandidateConfig, 0, len(cfg.ProviderNames()))
	for _, provider := range cfg.ProviderNames() {
		model := requestedModel
		if requestedModelRef != "" {
			model = ""
		}
		candidates = append(candidates, agentConfig.ModelRouteCandidateConfig{
			Provider: provider,
			Model:    model,
		})
	}
	return agentConfig.ModelRouteConfig{
		Strategy:   "smart",
		Candidates: candidates,
	}
}

func inspectSmartRouteCandidate(cfg *agentConfig.Config, routeKey, routeModelRef string, allowDeprecated bool, candidate agentConfig.ModelRouteCandidateConfig, history smartRouteHistory, healthState routeHealthState, modelProbeCache map[string]providerModelProbe, cat *modelcatalog.Catalog, obs *observations.Store) (smartRouteCandidate, agentConfig.ModelRouteCandidateConfig) {
	report := smartRouteCandidate{
		Provider:         candidate.Provider,
		Model:            candidate.Model,
		Priority:         candidate.Priority,
		Reliability:      history.ReliabilityScore(),
		AvgDurationMs:    history.AvgDurationMs,
		OutputTokensPerS: history.AvgOutputTokensPerS,
		RecentSelections: history.RecentSelections,
		AvgCostPer1KTok:  history.AvgKnownCostPer1KTok,
		LastSelectedAt:   history.LastSelectedAt,
	}

	overrides := agentConfig.ProviderOverrides{}
	if candidate.Model != "" {
		overrides.Model = candidate.Model
	} else if routeModelRef != "" {
		overrides.ModelRef = routeModelRef
	} else {
		overrides.Model = routeKey
	}

	overrides.AllowDeprecated = allowDeprecated
	pc, _, err := cfg.ResolveProviderConfig(candidate.Provider, overrides)
	if err != nil {
		report.Healthy = false
		report.Reason = err.Error()
		return report, candidate
	}
	if pc.Model != "" {
		report.Model = pc.Model
		candidate.Model = pc.Model
	}

	if failedAt, ok := healthState.Failures[candidate.Provider]; ok {
		if time.Since(failedAt) < routeHealthCooldown(cfg) {
			report.Healthy = false
			report.Reason = fmt.Sprintf("cooldown until %s", failedAt.Add(routeHealthCooldown(cfg)).Format(time.RFC3339))
			return report, candidate
		}
	}

	probe, ok := modelProbeCache[candidate.Provider]
	if !ok {
		probe = probeProviderModels(pc, routingProbeTimeout(cfg))
		modelProbeCache[candidate.Provider] = probe
	}
	healthy, matchedModel, reason := evaluateProviderCandidate(pc, routeKey, candidate.Model, probe)
	report.Healthy = healthy
	if matchedModel != "" {
		report.Model = matchedModel
		candidate.Model = matchedModel
	}
	report.Reason = reason

	// Populate catalog-sourced benchmark data.
	resolvedModel := report.Model
	if cat != nil && resolvedModel != "" {
		if entry, ok := cat.LookupModel(resolvedModel); ok {
			report.SWEBenchVerified = entry.SWEBenchVerified
		}
	}

	// Populate observed speed from the observations store.
	if obs != nil && resolvedModel != "" {
		if speed, ok := obs.MeanSpeed(observations.Key{ProviderSystem: candidate.Provider, Model: resolvedModel}); ok {
			report.ObservedTokensPerSec = speed
		}
	}

	return report, candidate
}

func scoreSmartRouteCandidates(plan *smartRoutePlan, counter int, cfg *agentConfig.Config) []int {
	healthy := make([]int, 0, len(plan.Candidates))
	for i, candidate := range plan.Candidates {
		if candidate.Healthy {
			healthy = append(healthy, i)
		}
	}
	if len(healthy) == 0 {
		return nil
	}

	reliabilityWeight, performanceWeight, loadWeight, costWeight, capabilityWeight := routingWeights(cfg)

	minDuration, maxDuration := 0.0, 0.0
	minCost, maxCost := 0.0, 0.0
	minObsSpeed, maxObsSpeed := 0.0, 0.0
	minBench, maxBench := 0.0, 0.0
	maxSelections := 0
	firstDuration := true
	firstCost := true
	firstObsSpeed := true
	firstBench := true
	for _, idx := range healthy {
		candidate := plan.Candidates[idx]
		if candidate.AvgDurationMs > 0 {
			if firstDuration {
				minDuration, maxDuration = candidate.AvgDurationMs, candidate.AvgDurationMs
				firstDuration = false
			} else {
				if candidate.AvgDurationMs < minDuration {
					minDuration = candidate.AvgDurationMs
				}
				if candidate.AvgDurationMs > maxDuration {
					maxDuration = candidate.AvgDurationMs
				}
			}
		}
		if candidate.AvgCostPer1KTok != nil {
			cost := *candidate.AvgCostPer1KTok
			if firstCost {
				minCost, maxCost = cost, cost
				firstCost = false
			} else {
				if cost < minCost {
					minCost = cost
				}
				if cost > maxCost {
					maxCost = cost
				}
			}
		}
		if candidate.ObservedTokensPerSec > 0 {
			if firstObsSpeed {
				minObsSpeed, maxObsSpeed = candidate.ObservedTokensPerSec, candidate.ObservedTokensPerSec
				firstObsSpeed = false
			} else {
				if candidate.ObservedTokensPerSec < minObsSpeed {
					minObsSpeed = candidate.ObservedTokensPerSec
				}
				if candidate.ObservedTokensPerSec > maxObsSpeed {
					maxObsSpeed = candidate.ObservedTokensPerSec
				}
			}
		}
		if candidate.SWEBenchVerified > 0 {
			if firstBench {
				minBench, maxBench = candidate.SWEBenchVerified, candidate.SWEBenchVerified
				firstBench = false
			} else {
				if candidate.SWEBenchVerified < minBench {
					minBench = candidate.SWEBenchVerified
				}
				if candidate.SWEBenchVerified > maxBench {
					maxBench = candidate.SWEBenchVerified
				}
			}
		}
		if candidate.RecentSelections > maxSelections {
			maxSelections = candidate.RecentSelections
		}
	}

	for _, idx := range healthy {
		candidate := &plan.Candidates[idx]

		// Performance: prefer observed speed when available, fall back to session history latency.
		performanceScore := 0.5
		if !firstObsSpeed && candidate.ObservedTokensPerSec > 0 {
			if maxObsSpeed == minObsSpeed {
				performanceScore = 1.0
			} else {
				performanceScore = (candidate.ObservedTokensPerSec - minObsSpeed) / (maxObsSpeed - minObsSpeed)
			}
		} else if !firstDuration && candidate.AvgDurationMs > 0 {
			if maxDuration == minDuration {
				performanceScore = 1.0
			} else {
				performanceScore = 1 - ((candidate.AvgDurationMs - minDuration) / (maxDuration - minDuration))
			}
		}

		loadScore := 1.0
		if maxSelections > 0 {
			loadScore = 1 - (float64(candidate.RecentSelections) / float64(maxSelections))
		}

		costScore := 0.5
		if !firstCost && candidate.AvgCostPer1KTok != nil {
			if maxCost == minCost {
				costScore = 1.0
			} else {
				costScore = 1 - ((*candidate.AvgCostPer1KTok - minCost) / (maxCost - minCost))
			}
		}

		// Capability: normalize swe_bench_verified across healthy candidates.
		capScore := 0.5 // neutral when no data
		if !firstBench && candidate.SWEBenchVerified > 0 {
			if maxBench == minBench {
				capScore = 1.0
			} else {
				capScore = (candidate.SWEBenchVerified - minBench) / (maxBench - minBench)
			}
		}
		candidate.CapabilityScore = capScore

		priorityScore := 0.0
		if candidate.Priority > 0 {
			priorityScore = float64(candidate.Priority) / 1000
		}

		candidate.Score = candidate.Reliability*reliabilityWeight +
			performanceScore*performanceWeight +
			loadScore*loadWeight +
			costScore*costWeight +
			capScore*capabilityWeight +
			priorityScore
	}

	sort.SliceStable(healthy, func(i, j int) bool {
		left := plan.Candidates[healthy[i]]
		right := plan.Candidates[healthy[j]]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.RecentSelections != right.RecentSelections {
			return left.RecentSelections < right.RecentSelections
		}
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		return left.Provider < right.Provider
	})

	bestScore := plan.Candidates[healthy[0]].Score
	bestTier := make([]int, 0, len(healthy))
	rest := make([]int, 0, len(healthy))
	for _, idx := range healthy {
		if abs(plan.Candidates[idx].Score-bestScore) < 0.01 {
			bestTier = append(bestTier, idx)
			continue
		}
		rest = append(rest, idx)
	}
	if len(bestTier) > 1 {
		offset := counter % len(bestTier)
		bestTier = append(bestTier[offset:], bestTier[:offset]...)
	}
	return append(bestTier, rest...)
}

func evaluateProviderCandidate(pc agentConfig.ProviderConfig, requestedModel, configuredModel string, probe providerModelProbe) (bool, string, string) {
	switch pc.Type {
	case "anthropic":
		if pc.APIKey == "" {
			return false, "", "missing API key"
		}
		if requestedModel != "" && configuredModel != "" {
			requestedFamily := modelFamily(requestedModel)
			configuredFamily := modelFamily(configuredModel)
			if requestedFamily != "" && configuredFamily != "" && requestedFamily != configuredFamily {
				return false, "", "requested model not compatible with provider"
			}
		}
		if configuredModel != "" {
			return true, configuredModel, "configured"
		}
		return true, requestedModel, "configured"
	default:
		if probe.err != nil {
			return false, "", probe.err.Error()
		}
		match := bestModelMatch(requestedModel, configuredModel, probe.models)
		if requestedModel != "" && match == "" {
			if configuredModel != "" && !sameModelIntent(requestedModel, configuredModel) {
				return true, configuredModel, "configured (unlisted)"
			}
			return false, "", "requested model unavailable"
		}
		if match == "" {
			match = configuredModel
		}
		if match == "" && len(probe.models) > 0 {
			match = probe.models[0]
		}
		return true, match, fmt.Sprintf("healthy (%d models)", len(probe.models))
	}
}

func bestModelMatch(requestedModel, configuredModel string, listed []string) string {
	if requestedModel == "" {
		if configuredModel != "" {
			return configuredModel
		}
		if len(listed) > 0 {
			return listed[0]
		}
		return ""
	}
	if configuredModel != "" && sameModelIntent(requestedModel, configuredModel) {
		return configuredModel
	}
	for _, model := range listed {
		if sameModelIntent(requestedModel, model) {
			return model
		}
	}
	if configuredModel != "" {
		return requestedModel
	}
	return ""
}

func sameModelIntent(requestedModel, candidate string) bool {
	left := comparableModelName(requestedModel)
	right := comparableModelName(candidate)
	if left == "" || right == "" {
		return false
	}
	return left == right || strings.Contains(right, left) || strings.Contains(left, right)
}

func comparableModelName(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = model[slash+1:]
	}
	if idx := strings.LastIndex(model, "-20"); idx > 0 {
		model = model[:idx]
	}
	model = strings.TrimSuffix(model, "-latest")
	return model
}

func modelFamily(model string) string {
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "claude"):
		return "claude"
	case strings.Contains(model, "qwen"):
		return "qwen"
	case strings.Contains(model, "gpt"):
		return "gpt"
	case strings.Contains(model, "gemini"):
		return "gemini"
	case strings.Contains(model, "llama"):
		return "llama"
	default:
		return ""
	}
}

func probeProviderModels(pc agentConfig.ProviderConfig, timeout time.Duration) providerModelProbe {
	if pc.Type == "anthropic" {
		if pc.APIKey == "" {
			return providerModelProbe{err: fmt.Errorf("missing API key")}
		}
		return providerModelProbe{}
	}
	if strings.TrimSpace(pc.BaseURL) == "" {
		return providerModelProbe{err: fmt.Errorf("no URL configured")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	models, err := oaiProvider.DiscoverModels(ctx, pc.BaseURL, pc.APIKey)
	if err != nil {
		return providerModelProbe{err: err}
	}
	return providerModelProbe{models: models}
}

func readSmartRoutingHistory(logDir, routeKey string, now time.Time, historyWindow time.Duration) (map[string]smartRouteHistory, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]smartRouteHistory{}, nil
		}
		return nil, fmt.Errorf("routing history: reading log dir: %w", err)
	}
	cutoff := now.Add(-historyWindow)
	loadCutoff := now.Add(-minDuration(historyWindow, defaultRoutingLoadWindow))
	type accumulator struct {
		successes        int
		failures         int
		durationMs       int64
		outputTokPerSSum float64
		costPer1KSum     float64
		costSamples      int
		recentSelections int
		lastSelectedAt   time.Time
	}
	accumulators := make(map[string]*accumulator)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		events, err := session.ReadEvents(filepath.Join(logDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, event := range events {
			if event.Type != agent.EventSessionEnd {
				continue
			}
			var end session.SessionEndData
			if err := json.Unmarshal(event.Data, &end); err != nil {
				continue
			}
			if end.SelectedProvider == "" {
				continue
			}
			intent := end.RequestedModel
			if intent == "" {
				intent = end.ResolvedModel
			}
			if intent == "" {
				intent = end.Model
			}
			if routeKey != "" && intent != routeKey {
				continue
			}
			if event.Timestamp.Before(cutoff) {
				continue
			}
			acc := accumulators[end.SelectedProvider]
			if acc == nil {
				acc = &accumulator{}
				accumulators[end.SelectedProvider] = acc
			}
			if event.Timestamp.After(acc.lastSelectedAt) {
				acc.lastSelectedAt = event.Timestamp
			}
			if event.Timestamp.After(loadCutoff) {
				acc.recentSelections++
			}
			if end.Status == agent.StatusSuccess {
				acc.successes++
				acc.durationMs += end.DurationMs
				if end.DurationMs > 0 && end.Tokens.Output > 0 {
					acc.outputTokPerSSum += float64(end.Tokens.Output) / (float64(end.DurationMs) / 1000)
				}
				if end.CostUSD != nil && *end.CostUSD >= 0 && end.Tokens.Total > 0 {
					acc.costPer1KSum += (*end.CostUSD / float64(end.Tokens.Total)) * 1000
					acc.costSamples++
				}
			} else {
				acc.failures++
			}
		}
	}

	result := make(map[string]smartRouteHistory, len(accumulators))
	for provider, acc := range accumulators {
		stats := smartRouteHistory{
			Samples:          acc.successes + acc.failures,
			Successes:        acc.successes,
			Failures:         acc.failures,
			RecentSelections: acc.recentSelections,
			LastSelectedAt:   acc.lastSelectedAt,
		}
		if acc.successes > 0 {
			stats.AvgDurationMs = float64(acc.durationMs) / float64(acc.successes)
			stats.AvgOutputTokensPerS = acc.outputTokPerSSum / float64(acc.successes)
		}
		if acc.costSamples > 0 {
			value := acc.costPer1KSum / float64(acc.costSamples)
			stats.AvgKnownCostPer1KTok = &value
		}
		result[provider] = stats
	}
	return result, nil
}

func minDuration(left, right time.Duration) time.Duration {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func cmdRouteStatus(workDir string, args []string) int {
	fs := flag.NewFlagSet("route-status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	model := fs.String("model", "", "Requested model intent")
	modelRef := fs.String("model-ref", "", "Model catalog reference")
	jsonOut := fs.Bool("json", false, "Output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	routeKey, routeModelRef, legacyBackend, err := resolveRouteTarget(cfg, "", "", agentConfig.ProviderOverrides{
		Model:    *model,
		ModelRef: *modelRef,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}
	if legacyBackend != "" {
		fmt.Fprintf(os.Stderr, "error: route-status requires model intent, not deprecated backend selection\n")
		return 2
	}
	if routeKey == "" {
		fmt.Fprintf(os.Stderr, "error: no route target resolved (use --model or --model-ref, or configure routing defaults)\n")
		return 2
	}

	var explicitRoute *agentConfig.ModelRouteConfig
	if route, ok := cfg.GetModelRoute(routeKey); ok {
		explicitRoute = &route
	}
	plan, err := buildSmartRoutePlan(cfg, workDir, routeKey, routeModelRef, false, explicitRoute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	selectedProvider := ""
	selectedModel := ""
	if len(plan.Order) > 0 {
		selectedProvider = plan.Candidates[plan.Order[0]].Provider
		selectedModel = plan.Candidates[plan.Order[0]].Model
	}

	if *jsonOut {
		payload := struct {
			RouteKey          string                `json:"route_key"`
			RequestedModel    string                `json:"requested_model,omitempty"`
			RequestedModelRef string                `json:"requested_model_ref,omitempty"`
			Strategy          string                `json:"strategy"`
			SelectedProvider  string                `json:"selected_provider,omitempty"`
			SelectedModel     string                `json:"selected_model,omitempty"`
			Candidates        []smartRouteCandidate `json:"candidates"`
		}{
			RouteKey:          plan.RouteKey,
			RequestedModel:    plan.RequestedModel,
			RequestedModelRef: plan.RequestedModelRef,
			Strategy:          plan.Strategy,
			SelectedProvider:  selectedProvider,
			SelectedModel:     selectedModel,
			Candidates:        orderedCandidates(plan),
		}
		data, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(data))
		return 0
	}

	fmt.Printf("Route: %s\n", plan.RouteKey)
	if plan.RequestedModelRef != "" {
		fmt.Printf("Model Ref: %s\n", plan.RequestedModelRef)
	}
	fmt.Printf("Strategy: %s\n", plan.Strategy)
	if selectedProvider != "" {
		fmt.Printf("Selected: %s (%s)\n", selectedProvider, selectedModel)
	}
	fmt.Printf("%-12s %-32s %-8s %-10s %-12s %-12s %-8s %s\n", "PROVIDER", "MODEL", "HEALTH", "SCORE", "RELIABILITY", "LATENCY", "LOAD", "REASON")
	for _, candidate := range orderedCandidates(plan) {
		health := "down"
		if candidate.Healthy {
			health = "healthy"
		}
		fmt.Printf("%-12s %-32s %-8s %-10.3f %-12.2f %-12.0f %-8d %s\n",
			candidate.Provider,
			truncate(candidate.Model, 32),
			health,
			candidate.Score,
			candidate.Reliability,
			candidate.AvgDurationMs,
			candidate.RecentSelections,
			candidate.Reason,
		)
	}
	return 0
}

func orderedCandidates(plan smartRoutePlan) []smartRouteCandidate {
	if len(plan.Candidates) == 0 {
		return nil
	}
	ordered := make([]smartRouteCandidate, 0, len(plan.Candidates))
	seen := make(map[int]struct{}, len(plan.Candidates))
	for _, idx := range plan.Order {
		if idx < 0 || idx >= len(plan.Candidates) {
			continue
		}
		ordered = append(ordered, plan.Candidates[idx])
		seen[idx] = struct{}{}
	}
	for idx, candidate := range plan.Candidates {
		if _, ok := seen[idx]; ok {
			continue
		}
		ordered = append(ordered, candidate)
	}
	return ordered
}

func truncate(value string, n int) string {
	if n <= 0 || len(value) <= n {
		return value
	}
	if n <= 2 {
		return value[:n]
	}
	return value[:n-2] + ".."
}
