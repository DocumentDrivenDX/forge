package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ScoredModel is a discovered model with a selection preference score.
// Higher scores are preferred by the auto-selection logic.
type ScoredModel struct {
	// ID is the model identifier returned by the server's /v1/models endpoint.
	ID string
	// CatalogRef is the catalog target ID if this model is recognized in the
	// model catalog for the provider's surface. Empty for unrecognized models.
	CatalogRef string
	// PatternMatch is true when this model matched the configured model_pattern.
	PatternMatch bool
	// Score summarises the selection preference: 3 = catalog-recognized,
	// 2 = pattern-matched, 1 = uncategorized.
	Score int
}

// modelsResponse is the shape of GET /v1/models from any OpenAI-compatible server.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// DiscoverModels queries the /v1/models endpoint of an OpenAI-compatible server
// and returns the model IDs it reports. At most 5 seconds is spent on the
// network request; callers that need a custom deadline should pass a context
// with a deadline already set.
func DiscoverModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	base := strings.TrimRight(baseURL, "/")
	endpoint := base + "/models"

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("discovery: %s returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var mr modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("discovery: decode response from %s: %w", endpoint, err)
	}

	ids := make([]string, 0, len(mr.Data))
	for _, m := range mr.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// RankModels scores and sorts a list of discovered model IDs by selection
// preference:
//
//   - Score 3 — catalog-recognized: the model ID appears in knownModels (a map
//     from concrete model ID to catalog target ID, e.g. from
//     Catalog.AllConcreteModels). These are explicitly tracked models; prefer
//     them when auto-selecting.
//   - Score 2 — pattern-matched: the model ID matches the case-insensitive
//     pattern regex (pattern == "" means this tier is skipped).
//   - Score 1 — uncategorized: known to the server but not in the catalog or
//     pattern.
//
// Within each score tier, the original server-returned order is preserved.
// Returns an error only if pattern is non-empty and fails to compile.
func RankModels(candidates []string, knownModels map[string]string, pattern string) ([]ScoredModel, error) {
	var patternRe *regexp.Regexp
	if pattern != "" {
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return nil, fmt.Errorf("discovery: invalid model_pattern %q: %w", pattern, err)
		}
		patternRe = re
	}

	scored := make([]ScoredModel, 0, len(candidates))
	for _, id := range candidates {
		sm := ScoredModel{ID: id, Score: 1}
		if ref, ok := knownModels[id]; ok {
			sm.CatalogRef = ref
			sm.Score = 3
		} else if patternRe != nil && patternRe.MatchString(id) {
			sm.PatternMatch = true
			sm.Score = 2
		}
		scored = append(scored, sm)
	}

	// Stable sort: higher score first, original order preserved within tier.
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
	return scored, nil
}

// SelectModel picks the preferred model ID from a ranked list. Returns ""
// if the list is empty.
func SelectModel(ranked []ScoredModel) string {
	if len(ranked) == 0 {
		return ""
	}
	return ranked[0].ID
}

// ModelLimits holds the context and output limits discovered from a provider API.
// Zero values mean the limit could not be determined.
type ModelLimits struct {
	// ContextLength is the model's context window in tokens.
	ContextLength int
	// MaxCompletionTokens is the maximum number of output tokens per turn.
	MaxCompletionTokens int
}

// LookupModelLimits queries the provider API to discover context and output
// limits for the given model. Returns zero values on any error — callers should
// apply their own defaults when the returned values are zero.
//
// flavor overrides automatic detection when non-empty; supported values are
// "lmstudio", "omlx", "openrouter", "ollama". When flavor is empty the
// function probes the server to detect its type, falling back to port-based
// heuristics as a last resort.
//
// Supported providers:
//   - LM Studio: queries /api/v0/models/{model} for loaded_context_length
//   - oMLX: queries /v1/models/status and finds the matching entry
//   - OpenRouter: queries /api/v1/models and finds the matching entry
func LookupModelLimits(ctx context.Context, baseURL, apiKey, flavor string, headers map[string]string, model string) ModelLimits {
	switch resolveProviderFlavor(ctx, baseURL, flavor) {
	case "lmstudio":
		return lmstudioLimits(ctx, baseURL, model)
	case "omlx":
		return omlxLimits(ctx, baseURL, model)
	case "openrouter":
		return openrouterLimits(ctx, apiKey, headers, model)
	default:
		return ModelLimits{}
	}
}

// getAndDecode performs a GET request with optional Bearer auth and extra
// headers, decodes the JSON response into out, and returns any error.
func getAndDecode(ctx context.Context, timeout time.Duration, endpoint, apiKey string, headers map[string]string, out any) error {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// lmstudioLimits queries LM Studio's extended /api/v0/models/{model} endpoint.
func lmstudioLimits(ctx context.Context, baseURL, model string) ModelLimits {
	// Strip /v1 to get the LM Studio server root; path-escape the model ID to
	// handle namespaced IDs like "qwen/qwen3.5-27b".
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	endpoint := root + "/api/v0/models/" + url.PathEscape(model)

	var info struct {
		LoadedContextLength int `json:"loaded_context_length"`
		MaxContextLength    int `json:"max_context_length"`
	}
	if err := getAndDecode(ctx, 5*time.Second, endpoint, "", nil, &info); err != nil {
		return ModelLimits{}
	}

	// Prefer the actually-loaded context over the theoretical model maximum.
	contextLen := info.LoadedContextLength
	if contextLen == 0 {
		contextLen = info.MaxContextLength
	}
	return ModelLimits{ContextLength: contextLen}
}

// omlxLimits queries oMLX's extended /v1/models/status endpoint.
func omlxLimits(ctx context.Context, baseURL, model string) ModelLimits {
	base := strings.TrimRight(baseURL, "/")
	endpoint := base + "/models/status"

	var status struct {
		Models []struct {
			ID               string `json:"id"`
			MaxContextWindow int    `json:"max_context_window"`
			MaxTokens        int    `json:"max_tokens"`
		} `json:"models"`
	}
	if err := getAndDecode(ctx, 5*time.Second, endpoint, "", nil, &status); err != nil {
		return ModelLimits{}
	}

	for _, entry := range status.Models {
		if strings.EqualFold(entry.ID, model) {
			return ModelLimits{
				ContextLength:       entry.MaxContextWindow,
				MaxCompletionTokens: entry.MaxTokens,
			}
		}
	}
	return ModelLimits{}
}

// openrouterLimits queries the OpenRouter /api/v1/models list and finds the
// entry matching the configured model.
func openrouterLimits(ctx context.Context, apiKey string, headers map[string]string, model string) ModelLimits {
	var list struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			TopProvider   struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := getAndDecode(ctx, 10*time.Second, "https://openrouter.ai/api/v1/models", apiKey, headers, &list); err != nil {
		return ModelLimits{}
	}

	// Normalize ID by lowercasing and replacing hyphens with dots so that
	// version variants like "4-5" and "4.5" compare equal.
	normalizeID := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(s, "-", "."))
	}
	normModel := normalizeID(model)
	for _, m := range list.Data {
		if strings.EqualFold(m.ID, model) || normalizeID(m.ID) == normModel {
			return ModelLimits{
				ContextLength:       m.ContextLength,
				MaxCompletionTokens: m.TopProvider.MaxCompletionTokens,
			}
		}
	}
	return ModelLimits{}
}

func resolveProviderFlavor(ctx context.Context, baseURL, flavor string) string {
	if flavor != "" {
		return strings.ToLower(strings.TrimSpace(flavor))
	}

	// openAIIdentity gives a reliable answer for well-known URLs (openrouter.ai,
	// ollama on 11434, etc.) without a network round-trip. Only probe when it
	// returns an ambiguous result ("local" or "openai").
	detected, _, _ := openAIIdentity(baseURL)
	switch detected {
	case "local", "openai":
		if probed := probeProviderFlavor(ctx, baseURL); probed != "" {
			return probed
		}
		return detected
	default:
		return detected
	}
}

func probeProviderFlavor(ctx context.Context, baseURL string) string {
	if isOMLX(ctx, baseURL) {
		return "omlx"
	}
	if isLMStudio(ctx, baseURL) {
		return "lmstudio"
	}
	return ""
}

func isOMLX(ctx context.Context, baseURL string) bool {
	base := strings.TrimRight(baseURL, "/")
	endpoint := base + "/models/status"
	var status struct {
		Models []json.RawMessage `json:"models"`
	}
	return getAndDecode(ctx, 2*time.Second, endpoint, "", nil, &status) == nil && status.Models != nil
}

func isLMStudio(ctx context.Context, baseURL string) bool {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	endpoint := root + "/api/v0/models"
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	return getAndDecode(ctx, 2*time.Second, endpoint, "", nil, &list) == nil && list.Data != nil
}

// NormalizeModelID resolves a caller-supplied model name against the server's
// canonical model catalog (the IDs returned by GET /v1/models). If the name
// matches a catalog entry exactly (case-insensitive), that entry is returned.
// If the name matches exactly one catalog entry by suffix (the part after the
// last '/'), that entry's full ID is returned — this handles the common case
// where a user supplies a bare name like "qwen3-coder-next" but the server
// lists it as "qwen/qwen3-coder-next". Multiple suffix matches produce an
// ambiguity error listing the candidates. Zero matches return the original
// name unchanged.
func NormalizeModelID(requested string, catalog []string) (string, error) {
	reqLower := strings.ToLower(strings.TrimSpace(requested))
	if reqLower == "" {
		return requested, nil
	}

	// Exact match (case-insensitive).
	for _, id := range catalog {
		if strings.EqualFold(id, requested) {
			return id, nil
		}
	}

	// Suffix match: compare requested against the basename (after last '/')
	// of each catalog entry.
	var matches []string
	for _, id := range catalog {
		idLower := strings.ToLower(id)
		slash := strings.LastIndex(idLower, "/")
		if slash < 0 {
			continue // no prefix to strip — already checked via exact match
		}
		basename := idLower[slash+1:]
		if basename == reqLower {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return requested, nil
	default:
		return "", fmt.Errorf("ambiguous model %q: matches %v", requested, matches)
	}
}
