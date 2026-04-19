package modelcatalog

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type openrouterPricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read"`
	InputCacheWrite string `json:"input_cache_write"`
}

type openrouterModelEntry struct {
	ID            string            `json:"id"`
	ContextLength int               `json:"context_length"`
	Pricing       openrouterPricing `json:"pricing"`
}

// FetchOpenRouterPricing fetches current pricing from OpenRouter /models.
// Returns a map of OpenRouter model ID → entry.
func FetchOpenRouterPricing(timeout time.Duration) (map[string]openrouterModelEntry, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	baseURL := "https://openrouter.ai/api/v1"

	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch openrouter models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("openrouter /models HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Data []openrouterModelEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse openrouter models: %w", err)
	}

	out := make(map[string]openrouterModelEntry, len(parsed.Data))
	for _, m := range parsed.Data {
		out[m.ID] = m
	}
	return out, nil
}

// UpdateManifestPricing fetches OpenRouter pricing and updates cost fields
// in the manifest at manifestPath. Creates the file from the embedded default
// if it doesn't exist. Returns (updated count, not-found IDs, error).
func UpdateManifestPricing(manifestPath string, timeout time.Duration) (int, []string, error) {
	// Load or seed the manifest
	var data []byte
	existing, err := os.ReadFile(manifestPath) // #nosec G304 -- manifestPath is user-supplied config path, not attacker-controlled
	if err == nil {
		data = existing
	} else if os.IsNotExist(err) {
		data = embeddedManifest
	} else {
		return 0, nil, err
	}

	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return 0, nil, fmt.Errorf("parse manifest: %w", err)
	}

	prices, err := FetchOpenRouterPricing(timeout)
	if err != nil {
		return 0, nil, err
	}

	updated := 0
	var notFound []string

	for id, target := range m.Targets {
		// Try to find a matching OpenRouter entry:
		// 1. Try openrouter_ref_id if set
		// 2. Try surface model IDs
		var orEntry openrouterModelEntry
		var found bool

		if target.OpenRouterRefID != "" {
			orEntry, found = prices[target.OpenRouterRefID]
		}
		if !found {
			// Sort surface keys for deterministic matching order.
			surfaceKeys := make([]string, 0, len(target.Surfaces))
			for k := range target.Surfaces {
				surfaceKeys = append(surfaceKeys, k)
			}
			sort.Strings(surfaceKeys)
			for _, k := range surfaceKeys {
				primary := target.Surfaces[k].primaryModel()
				if e, ok := prices[primary]; ok {
					orEntry = e
					found = true
					break
				}
			}
		}

		if !found {
			notFound = append(notFound, id)
			continue
		}

		if p := parseORFloat(orEntry.Pricing.Prompt); p > 0 {
			target.CostInputPerM = p * 1_000_000
		}
		if p := parseORFloat(orEntry.Pricing.Completion); p > 0 {
			target.CostOutputPerM = p * 1_000_000
		}
		if p := parseORFloat(orEntry.Pricing.InputCacheRead); p > 0 {
			target.CostCacheReadPerM = p * 1_000_000
		}
		if p := parseORFloat(orEntry.Pricing.InputCacheWrite); p > 0 {
			target.CostCacheWritePerM = p * 1_000_000
		}
		if orEntry.ContextLength > 0 {
			target.ContextWindow = orEntry.ContextLength
		}
		m.Targets[id] = target
		updated++
	}

	out, err := yaml.Marshal(m)
	if err != nil {
		return 0, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o750); err != nil { // #nosec G301
		return 0, nil, err
	}
	if err := os.WriteFile(manifestPath, out, 0o600); err != nil { // #nosec G306
		return 0, nil, err
	}
	return updated, notFound, nil
}

func parseORFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
