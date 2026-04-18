package agent

// This file implements ListProviders and HealthCheck for the DdxAgent service.
// It lives in the root package to avoid import cycles; provider config data is
// injected via the ServiceConfig interface defined in service.go.
//
// Note: We cannot import agent/config or provider/openai here because both
// packages import the root agent package, creating a cycle. Provider probing
// is done inline using net/http.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ListProviders returns providers known to the native-agent harness with live
// status, configured-default markers, and cooldown state.
func (s *service) ListProviders(ctx context.Context) ([]ProviderInfo, error) {
	sc := s.opts.ServiceConfig
	if sc == nil {
		return nil, fmt.Errorf("service: no ServiceConfig provided; pass ServiceOptions.ServiceConfig")
	}

	names := sc.ProviderNames()
	defaultName := sc.DefaultProviderName()
	cooldown := sc.HealthCooldown()
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}

	type indexedInfo struct {
		idx  int
		info ProviderInfo
	}
	results := make([]indexedInfo, len(names))
	var wg sync.WaitGroup

	for i, name := range names {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()

			entry, ok := sc.Provider(name)
			if !ok {
				results[idx] = indexedInfo{idx: idx, info: ProviderInfo{
					Name:   name,
					Status: "error: provider not found in config",
				}}
				return
			}

			info := ProviderInfo{
				Name:         name,
				Type:         normalizeServiceProviderType(entry.Type),
				BaseURL:      entry.BaseURL,
				IsDefault:    name == defaultName,
				DefaultModel: entry.Model,
			}

			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			info.Status, info.ModelCount, info.Capabilities = probeServiceProvider(probeCtx, entry)
			info.CooldownState = serviceProviderCooldown(sc, name, cooldown)

			results[idx] = indexedInfo{idx: idx, info: info}
		}(i, name)
	}
	wg.Wait()

	out := make([]ProviderInfo, len(names))
	for _, r := range results {
		out[r.idx] = r.info
	}
	return out, nil
}

// HealthCheck triggers a fresh probe for the named target and updates internal state.
// target.Type is "harness" or "provider".
func (s *service) HealthCheck(ctx context.Context, target HealthTarget) error {
	switch target.Type {
	case "provider":
		sc := s.opts.ServiceConfig
		if sc == nil {
			return fmt.Errorf("service: no ServiceConfig provided; pass ServiceOptions.ServiceConfig")
		}
		entry, ok := sc.Provider(target.Name)
		if !ok {
			return fmt.Errorf("service: provider %q not found", target.Name)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		status, _, _ := probeServiceProvider(probeCtx, entry)
		if status == "connected" {
			return nil
		}
		return fmt.Errorf("service: provider %q: %s", target.Name, status)

	case "harness":
		statuses := s.registry.Discover()
		for _, st := range statuses {
			if st.Name != target.Name {
				continue
			}
			if !st.Available {
				return fmt.Errorf("service: harness %q unavailable: %s", target.Name, st.Error)
			}
			return nil
		}
		return fmt.Errorf("service: harness %q not registered", target.Name)

	default:
		return fmt.Errorf("service: unknown HealthTarget.Type %q (want \"harness\" or \"provider\")", target.Type)
	}
}

// probeServiceProvider pings a provider and returns (status, modelCount, capabilities).
func probeServiceProvider(ctx context.Context, entry ServiceProviderEntry) (status string, modelCount int, caps []string) {
	switch entry.Type {
	case "anthropic":
		if entry.APIKey == "" {
			return "error: api_key not configured", 0, nil
		}
		// Anthropic does not expose an unauthenticated /v1/models list endpoint.
		// Treat key presence as the connectivity signal.
		return "connected", 0, []string{"tool_use", "vision", "streaming"}

	case "openai-compat", "openai", "":
		if entry.BaseURL == "" {
			return "error: base_url not configured", 0, nil
		}
		n, err := discoverOpenAIModels(ctx, entry.BaseURL, entry.APIKey)
		if err != nil {
			msg := err.Error()
			if serviceIsUnreachable(msg) {
				return "unreachable", 0, nil
			}
			return "error: " + serviceTrimError(msg), 0, nil
		}
		return "connected", n, []string{"tool_use", "streaming", "json_mode"}

	default:
		return "error: unknown provider type " + entry.Type, 0, nil
	}
}

// discoverOpenAIModels queries the /v1/models endpoint and returns the model count.
// This is a minimal inline version of provider/openai.DiscoverModels that avoids
// the import cycle (provider/openai imports the root agent package).
func discoverOpenAIModels(ctx context.Context, baseURL, apiKey string) (int, error) {
	base := strings.TrimRight(baseURL, "/")
	endpoint := base + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("discovery: %s returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var mr struct {
		Data []struct{ ID string `json:"id"` } `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return 0, fmt.Errorf("discovery: decode response from %s: %w", endpoint, err)
	}
	return len(mr.Data), nil
}

// serviceProviderCooldown scans all model routes for any candidate matching
// providerName and returns the first active CooldownState, or nil.
func serviceProviderCooldown(sc ServiceConfig, providerName string, cooldown time.Duration) *CooldownState {
	workDir := sc.WorkDir()
	if workDir == "" {
		return nil
	}
	now := time.Now().UTC()
	for _, routeName := range sc.ModelRouteNames() {
		for _, candidate := range sc.ModelRouteCandidates(routeName) {
			if candidate != providerName {
				continue
			}
			failures := serviceLoadRouteFailures(workDir, routeName)
			failedAt, hasFail := failures[providerName]
			if !hasFail {
				continue
			}
			until := failedAt.Add(cooldown)
			if until.Before(now) {
				continue
			}
			return &CooldownState{
				Reason:    "consecutive_failures",
				Until:     until,
				FailCount: 1,
			}
		}
	}
	return nil
}

// serviceLoadRouteFailures reads the file-backed route health state and returns
// the Failures map (provider name → failure timestamp).
func serviceLoadRouteFailures(workDir, routeName string) map[string]time.Time {
	type routeHealthState struct {
		Failures map[string]time.Time `json:"failures,omitempty"`
	}
	key := serviceRouteStateKey(routeName)
	path := filepath.Join(workDir, ".agent", "route-health-"+key+".json")
	// #nosec G304 -- operator-managed path under workDir
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rs routeHealthState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil
	}
	return rs.Failures
}

func normalizeServiceProviderType(t string) string {
	switch t {
	case "openai-compat", "openai", "":
		return "openai-compat"
	case "anthropic":
		return "anthropic"
	default:
		return t
	}
}

func serviceIsUnreachable(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "dial tcp") ||
		strings.Contains(lower, "unreachable") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "i/o timeout")
}

func serviceTrimError(msg string) string {
	const maxLen = 120
	if len(msg) > maxLen {
		return msg[:maxLen] + "..."
	}
	return msg
}

func serviceRouteStateKey(routeName string) string {
	replacer := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return replacer.Replace(routeName)
}
