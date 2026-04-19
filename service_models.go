package agent

// This file implements ListModels for the DdxAgent service.
// It lives in the root package to avoid import cycles; provider and catalog
// data is injected via ServiceConfig (defined in service.go).
//
// For subprocess harnesses (claude, codex, etc.) this implementation returns
// empty — their model lists are not discoverable through /v1/models.
// TODO(future): plumb vendor-specific model lists from internal/harnesses/<name>/.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
)

// ListModels returns models matching the filter, with full metadata.
// Empty filter returns all models from every reachable provider.
func (s *service) ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error) {
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
	ids, ranked := discoverAndRankModels(ctx, entry, cat)
	if len(ids) == 0 {
		return nil
	}

	// Build a position map from the ranked list.
	rankPos := make(map[string]int, len(ranked))
	for pos, sm := range ranked {
		rankPos[sm.ID] = pos
	}

	// Build CatalogRef map from ranked list.
	catalogRefMap := make(map[string]string, len(ranked))
	for _, sm := range ranked {
		if sm.CatalogRef != "" {
			catalogRefMap[sm.ID] = sm.CatalogRef
		}
	}

	configuredDefaultModel := entry.Model

	out := make([]ModelInfo, 0, len(ids))
	for _, id := range ids {
		info := ModelInfo{
			ID:       id,
			Provider: providerName,
			Harness:  "agent",
			Available: true,
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
	return out
}

// discoverAndRankModels fetches the model list from a provider and ranks them
// against the catalog. Returns (ids, ranked) where ids preserves discovery order
// and ranked is the scored list.
func discoverAndRankModels(ctx context.Context, entry ServiceProviderEntry, cat *modelcatalog.Catalog) ([]string, []scoredModel) {
	switch normalizeServiceProviderType(entry.Type) {
	case "openai-compat":
		if entry.BaseURL == "" {
			return nil, nil
		}
		ids, err := discoverModelsInline(ctx, entry.BaseURL, entry.APIKey)
		if err != nil {
			return nil, nil
		}
		ranked := rankModelsInline(ids, cat)
		return ids, ranked

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
			return []string{entry.Model}, []scoredModel{sm}
		}
		return nil, nil

	default:
		return nil, nil
	}
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
