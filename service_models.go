package agent

// This file implements ListModels for the DdxAgent service.
// It lives in the root package to avoid import cycles; provider and catalog
// data is injected via ServiceConfig (defined in service.go).
//
// Provider-backed models are discovered through /v1/models. Codex and Claude
// expose a separate harness-native surface backed by PTY/CLI evidence.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	geminiharness "github.com/DocumentDrivenDX/agent/internal/harnesses/gemini"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
)

// ListModels returns models matching the filter, with full metadata.
// Empty filter returns all models from every reachable provider.
func (s *service) ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error) {
	if filter.Harness != "" && filter.Harness != "agent" {
		return s.listModelsForSubprocessHarness(filter), nil
	}

	sc := s.opts.ServiceConfig
	if sc == nil {
		return nil, fmt.Errorf("service: no ServiceConfig provided; pass ServiceOptions.ServiceConfig")
	}

	// Load the model catalog once for cross-referencing.
	cat, _ := modelcatalog.Default() // ignore error: catalog miss is non-fatal

	// Build lookup sets from config.
	defaultProviderName := sc.DefaultProviderName()
	configuredRouteNames := make(map[string]bool, len(sc.ModelRouteNames()))
	for _, rn := range sc.ModelRouteNames() {
		configuredRouteNames[rn] = true
	}

	names := sc.ProviderNames()

	type indexedModels struct {
		idx    int
		models []ModelInfo
	}
	results := make([]indexedModels, len(names))
	var wg sync.WaitGroup

	for i, name := range names {
		// Apply provider filter.
		if filter.Provider != "" && filter.Provider != name {
			results[i] = indexedModels{idx: i, models: nil}
			continue
		}
		// Apply harness filter: providers are served by the "agent" harness.
		if filter.Harness != "" && filter.Harness != "agent" {
			results[i] = indexedModels{idx: i, models: nil}
			continue
		}

		wg.Add(1)
		go func(idx int, providerName string) {
			defer wg.Done()

			entry, ok := sc.Provider(providerName)
			if !ok {
				results[idx] = indexedModels{idx: idx, models: nil}
				return
			}

			isDefaultProvider := providerName == defaultProviderName
			models := listModelsForProvider(ctx, providerName, entry, isDefaultProvider, sc, cat, configuredRouteNames)
			results[idx] = indexedModels{idx: idx, models: models}
		}(i, name)
	}
	wg.Wait()

	// Flatten in stable provider order.
	var out []ModelInfo
	for _, r := range results {
		out = append(out, r.models...)
	}
	return out, nil
}

func (s *service) listModelsForSubprocessHarness(filter ModelFilter) []ModelInfo {
	name := harnesses.ResolveHarnessAlias(filter.Harness)
	cfg, ok := s.registry.Get(name)
	modelIDs := subprocessHarnessModelIDs(name, cfg)
	if !ok || cfg.IsHTTPProvider || cfg.IsLocal || len(modelIDs) == 0 {
		return nil
	}
	if filter.Provider != "" && filter.Provider != name {
		return nil
	}
	cat, _ := modelcatalog.Default()
	catalogRefs := catalogRefsForHarness(cat, name)
	out := make([]ModelInfo, 0, len(modelIDs))
	for i, id := range modelIDs {
		info := ModelInfo{
			ID:           id,
			Provider:     name,
			Harness:      name,
			Capabilities: []string{"streaming", "tool_use"},
			Available:    true,
			IsDefault:    cfg.DefaultModel != "" && id == cfg.DefaultModel,
			CatalogRef:   catalogRefs[id],
			RankPosition: i,
		}
		if cat != nil {
			info.ContextLength = resolveContextLength(context.Background(), ServiceProviderEntry{}, id, cat)
			info.Cost, info.PerfSignal = catalogCostAndPerf(cat, id)
		}
		out = append(out, info)
	}
	return out
}

func subprocessHarnessModelIDs(name string, cfg harnesses.HarnessConfig) []string {
	models := append([]string(nil), cfg.Models...)
	switch name {
	case "claude":
		snapshot := claudeharness.DefaultClaudeModelDiscovery()
		models = appendUniqueModelIDs(models, snapshot.Models...)
		for _, family := range []string{"sonnet", "opus", "haiku"} {
			resolved := claudeharness.ResolveClaudeFamilyAlias(family, snapshot)
			if resolved != family {
				models = appendUniqueModelIDs(models, resolved)
			}
		}
	case "codex":
		snapshot := codexharness.DefaultCodexModelDiscovery()
		models = appendUniqueModelIDs(models, snapshot.Models...)
		for _, family := range []string{"gpt", "gpt-5"} {
			resolved := codexharness.ResolveCodexModelAlias(family, snapshot)
			if resolved != family {
				models = appendUniqueModelIDs(models, resolved)
			}
		}
	case "gemini":
		snapshot := geminiharness.DefaultGeminiModelDiscovery()
		models = appendUniqueModelIDs(models, snapshot.Models...)
		for _, family := range []string{"gemini", "gemini-2.5"} {
			resolved := geminiharness.ResolveGeminiModelAlias(family, snapshot)
			if resolved != family {
				models = appendUniqueModelIDs(models, resolved)
			}
		}
	}
	return models
}

func resolveSubprocessModelAlias(harness, model string) string {
	switch harness {
	case "claude":
		return claudeCLIExecutableModel(model)
	case "codex":
		return codexharness.ResolveCodexModelAlias(model, codexharness.DefaultCodexModelDiscovery())
	case "gemini":
		return geminiharness.ResolveGeminiModelAlias(model, geminiharness.DefaultGeminiModelDiscovery())
	default:
		return model
	}
}

func claudeCLIExecutableModel(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case normalized == "sonnet" || strings.HasPrefix(normalized, "sonnet-") || strings.HasPrefix(normalized, "claude-sonnet-"):
		return "sonnet"
	case normalized == "opus" || strings.HasPrefix(normalized, "opus-") || strings.HasPrefix(normalized, "claude-opus-"):
		return "opus"
	case normalized == "haiku" || strings.HasPrefix(normalized, "haiku-") || strings.HasPrefix(normalized, "claude-haiku-"):
		return "haiku"
	default:
		return model
	}
}

func appendUniqueModelIDs(values []string, additions ...string) []string {
	for _, value := range additions {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		found := false
		for _, existing := range values {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			values = append(values, value)
		}
	}
	return values
}

func catalogRefsForHarness(cat *modelcatalog.Catalog, harness string) map[string]string {
	if cat == nil {
		return nil
	}
	switch harness {
	case "codex":
		return cat.AllConcreteModels(modelcatalog.SurfaceCodex)
	case "claude":
		return cat.AllConcreteModels(modelcatalog.SurfaceClaudeCode)
	case "gemini":
		return cat.AllConcreteModels(modelcatalog.SurfaceGemini)
	default:
		return nil
	}
}

// listModelsForProvider discovers and annotates models for a single provider.
func listModelsForProvider(
	ctx context.Context,
	providerName string,
	entry ServiceProviderEntry,
	isDefaultProvider bool,
	sc ServiceConfig,
	cat *modelcatalog.Catalog,
	configuredRouteNames map[string]bool,
) []ModelInfo {
	// Discover model IDs from the provider.
	discoveries := discoverAndRankModels(ctx, entry, cat)
	if len(discoveries) == 0 {
		return nil
	}

	configuredDefaultModel := entry.Model
	providerType := normalizeServiceProviderType(entry.Type)

	outLen := 0
	for _, discovery := range discoveries {
		outLen += len(discovery.IDs)
	}
	out := make([]ModelInfo, 0, outLen)
	for _, discovery := range discoveries {
		// Build a position map from the ranked list.
		rankPos := make(map[string]int, len(discovery.Ranked))
		for pos, sm := range discovery.Ranked {
			rankPos[sm.ID] = pos
		}

		// Build CatalogRef map from ranked list.
		catalogRefMap := make(map[string]string, len(discovery.Ranked))
		for _, sm := range discovery.Ranked {
			if sm.CatalogRef != "" {
				catalogRefMap[sm.ID] = sm.CatalogRef
			}
		}

		for _, id := range discovery.IDs {
			info := ModelInfo{
				ID:              id,
				Provider:        providerName,
				ProviderType:    providerType,
				Harness:         "agent",
				EndpointName:    discovery.EndpointName,
				EndpointBaseURL: discovery.EndpointBaseURL,
				Available:       true,
			}

			// Resolve context length: provider API > catalog > 0.
			info.ContextLength = resolveContextLength(ctx, entry, id, cat)

			// Capabilities from provider type.
			info.Capabilities = providerCapabilities(entry)

			// CatalogRef from ranked discovery.
			info.CatalogRef = catalogRefMap[id]

			// Cost and PerfSignal from catalog.
			if cat != nil && info.CatalogRef != "" {
				info.Cost, info.PerfSignal = catalogCostAndPerf(cat, id)
			} else if cat != nil {
				// Try direct model lookup by ID even without a catalog ref.
				info.Cost, info.PerfSignal = catalogCostAndPerf(cat, id)
			}

			// IsDefault: provider is default AND this model is the configured default model.
			info.IsDefault = isDefaultProvider && configuredDefaultModel != "" && id == configuredDefaultModel

			// IsConfigured: model ID matches an explicit model_routes entry.
			info.IsConfigured = configuredRouteNames[id]

			// RankPosition from discovery ranking.
			if pos, ok := rankPos[id]; ok {
				info.RankPosition = pos
			} else {
				info.RankPosition = -1
			}

			out = append(out, info)
		}
	}
	return out
}

type discoveredModelSet struct {
	EndpointName    string
	EndpointBaseURL string
	IDs             []string
	Ranked          []scoredModel
}

// discoverAndRankModels fetches the model list from each provider endpoint and
// ranks results against the catalog. IDs preserve discovery order per endpoint.
func discoverAndRankModels(ctx context.Context, entry ServiceProviderEntry, cat *modelcatalog.Catalog) []discoveredModelSet {
	switch normalizeServiceProviderType(entry.Type) {
	case "openai", "openrouter", "lmstudio", "omlx", "ollama", "minimax", "qwen", "zai":
		endpoints := modelDiscoveryEndpoints(entry)
		if len(endpoints) == 0 {
			return nil
		}
		out := make([]discoveredModelSet, 0, len(endpoints))
		for _, endpoint := range endpoints {
			ids, err := discoverModelsInline(ctx, endpoint.BaseURL, entry.APIKey)
			if err != nil || len(ids) == 0 {
				continue
			}
			out = append(out, discoveredModelSet{
				EndpointName:    endpoint.Name,
				EndpointBaseURL: endpoint.BaseURL,
				IDs:             ids,
				Ranked:          rankModelsInline(ids, cat),
			})
		}
		return out

	case "anthropic":
		// Anthropic does not expose /v1/models for discovery.
		// If a default model is configured, surface it.
		if entry.Model != "" {
			sm := scoredModel{ID: entry.Model, CatalogRef: "", RankPosition: 0}
			if cat != nil {
				if ref, ok := catalogRefForModel(cat, entry.Model); ok {
					sm.CatalogRef = ref
				}
			}
			return []discoveredModelSet{{
				EndpointName:    "default",
				EndpointBaseURL: entry.BaseURL,
				IDs:             []string{entry.Model},
				Ranked:          []scoredModel{sm},
			}}
		}
		return nil

	default:
		return nil
	}
}

type modelDiscoveryEndpoint struct {
	Name    string
	BaseURL string
}

func modelDiscoveryEndpoints(entry ServiceProviderEntry) []modelDiscoveryEndpoint {
	if len(entry.Endpoints) > 0 {
		out := make([]modelDiscoveryEndpoint, 0, len(entry.Endpoints))
		for _, ep := range entry.Endpoints {
			if strings.TrimSpace(ep.BaseURL) == "" {
				continue
			}
			out = append(out, modelDiscoveryEndpoint{
				Name:    endpointDisplayName(ep.Name, ep.BaseURL),
				BaseURL: ep.BaseURL,
			})
		}
		return out
	}
	if strings.TrimSpace(entry.BaseURL) == "" {
		return nil
	}
	return []modelDiscoveryEndpoint{{
		Name:    endpointDisplayName("default", entry.BaseURL),
		BaseURL: entry.BaseURL,
	}}
}

func endpointDisplayName(name, baseURL string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	u, err := url.Parse(baseURL)
	if err == nil && u.Host != "" {
		return u.Host
	}
	return "default"
}

// discoverModelsInline queries /v1/models and returns model IDs.
// Mirrors the inline impl in service_providers.go to avoid import cycle.
func discoverModelsInline(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	base := strings.TrimRight(baseURL, "/")
	endpoint := base + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("discovery: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var mr struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("discovery: decode response: %w", err)
	}

	ids := make([]string, 0, len(mr.Data))
	for _, m := range mr.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// scoredModel mirrors provider/openai.ScoredModel to avoid the import cycle.
type scoredModel struct {
	ID           string
	CatalogRef   string
	RankPosition int
}

// rankModelsInline scores discovered model IDs against the catalog.
func rankModelsInline(ids []string, cat *modelcatalog.Catalog) []scoredModel {
	var knownModels map[string]string
	if cat != nil {
		knownModels = cat.AllConcreteModels(modelcatalog.SurfaceAgentOpenAI)
	}

	// Score: 3=catalog, 2=pattern(unused here), 1=uncategorized.
	scored := make([]scoredModel, 0, len(ids))
	for pos, id := range ids {
		sm := scoredModel{ID: id, RankPosition: pos}
		if ref, ok := knownModels[id]; ok {
			sm.CatalogRef = ref
		}
		scored = append(scored, sm)
	}
	return scored
}

// catalogRefForModel looks up a model ID in the catalog and returns (targetID, true) if found.
func catalogRefForModel(cat *modelcatalog.Catalog, modelID string) (string, bool) {
	knownModels := cat.AllConcreteModels(modelcatalog.SurfaceAgentOpenAI)
	ref, ok := knownModels[modelID]
	return ref, ok
}

// resolveContextLength resolves the context window for a model using the
// precedence chain: catalog > 0 (provider API lookup omitted to keep
// ListModels non-blocking in the common case).
func resolveContextLength(_ context.Context, _ ServiceProviderEntry, modelID string, cat *modelcatalog.Catalog) int {
	if cat != nil {
		if n := cat.ContextWindowForModel(modelID); n > 0 {
			return n
		}
	}
	return 0
}

// catalogCostAndPerf extracts CostInfo and PerfSignal for a model from the catalog.
func catalogCostAndPerf(cat *modelcatalog.Catalog, modelID string) (CostInfo, PerfSignal) {
	entry, ok := cat.LookupModel(modelID)
	if ok {
		return CostInfo{
				InputPerMTok:  entry.CostInputPerMTok,
				OutputPerMTok: entry.CostOutputPerMTok,
			}, PerfSignal{
				SpeedTokensPerSec: entry.SpeedTokensPerSec,
				SWEBenchVerified:  entry.SWEBenchVerified,
			}
	}

	// Fallback: try target-level pricing via PricingFor.
	pricing := cat.PricingFor()
	if p, ok := pricing[modelID]; ok {
		return CostInfo{
			InputPerMTok:  p.InputPerMTok,
			OutputPerMTok: p.OutputPerMTok,
		}, PerfSignal{}
	}

	return CostInfo{}, PerfSignal{}
}

// providerCapabilities returns the capability set for a provider entry.
func providerCapabilities(entry ServiceProviderEntry) []string {
	switch normalizeServiceProviderType(entry.Type) {
	case "anthropic":
		return []string{"tool_use", "vision", "streaming"}
	default:
		return []string{"tool_use", "streaming", "json_mode"}
	}
}
