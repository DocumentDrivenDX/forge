package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/catalogdist"
	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/observations"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

const defaultCatalogBaseURL = "https://documentdrivendx.github.io/agent/catalog"

func cmdCatalog(workDir string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ddx-agent catalog <show|models|observations|check|update|update-pricing> [flags]")
		return 2
	}
	switch args[0] {
	case "show":
		return cmdCatalogShow(workDir, args[1:])
	case "models":
		return cmdCatalogModels(workDir, args[1:])
	case "observations":
		return cmdCatalogObservations(workDir, args[1:])
	case "check":
		return cmdCatalogCheck(workDir, args[1:])
	case "update":
		return cmdCatalogUpdate(workDir, args[1:])
	case "update-pricing":
		return cmdCatalogUpdatePricing(workDir, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "error: unknown catalog subcommand %q\n", args[0])
		return 2
	}
}

func cmdCatalogShow(workDir string, args []string) int {
	fs := flag.NewFlagSet("catalog show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, catalog, manifestPath, err := loadCatalogRuntime(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	_ = cfg

	meta := catalog.Metadata()
	fmt.Printf("source: %s\n", meta.ManifestSource)
	fmt.Printf("manifest_path: %s\n", manifestPath)
	fmt.Printf("schema_version: %d\n", meta.ManifestVersion)
	fmt.Printf("catalog_version: %s\n", blankIfEmpty(meta.CatalogVersion))

	for _, ref := range []string{"code-high", "code-medium", "code-economy"} {
		fmt.Printf("%s:\n", ref)
		printResolvedSurface(catalog, ref, modelcatalog.SurfaceAgentAnthropic)
		printResolvedSurface(catalog, ref, modelcatalog.SurfaceAgentOpenAI)
	}
	return 0
}

// catalogModelRow is the JSON representation of one v4 models: map entry.
type catalogModelRow struct {
	ID                string  `json:"id"`
	ProviderSystem    string  `json:"provider_system,omitempty"`
	CostInputPerMTok  float64 `json:"cost_input_per_mtok,omitempty"`
	CostOutputPerMTok float64 `json:"cost_output_per_mtok,omitempty"`
	SWEBenchVerified  float64 `json:"swe_bench_verified,omitempty"`
	OpenRouterRefID   string  `json:"openrouter_ref_id,omitempty"`
	SpeedTokensPerSec float64 `json:"speed_tokens_per_sec,omitempty"`
}

func cmdCatalogModels(workDir string, args []string) int {
	fs := flag.NewFlagSet("catalog models", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelFlag := fs.String("model", "", "Show details for a specific model ID")
	formatFlag := fs.String("format", "", "Output format: json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	_, catalog, _, err := loadCatalogRuntime(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	modelsMap := catalog.AllModels()
	if len(modelsMap) == 0 {
		fmt.Println("no model entries in catalog (requires v4+ manifest)")
		return 0
	}

	// Collect model IDs.
	ids := make([]string, 0, len(modelsMap))
	if *modelFlag != "" {
		entry, ok := catalog.LookupModel(*modelFlag)
		if !ok {
			fmt.Fprintf(os.Stderr, "error: model %q not found in catalog\n", *modelFlag)
			return 1
		}
		modelsMap = map[string]modelcatalog.ModelEntry{*modelFlag: entry}
		ids = append(ids, *modelFlag)
	} else {
		for id := range modelsMap {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	if *formatFlag == "json" {
		rows := make([]catalogModelRow, 0, len(ids))
		for _, id := range ids {
			e := modelsMap[id]
			rows = append(rows, catalogModelRow{
				ID:                id,
				ProviderSystem:    e.ProviderSystem,
				CostInputPerMTok:  catalogCostInputPerM(e),
				CostOutputPerMTok: catalogCostOutputPerM(e),
				SWEBenchVerified:  e.SWEBenchVerified,
				OpenRouterRefID:   catalogOpenRouterID(e),
				SpeedTokensPerSec: e.SpeedTokensPerSec,
			})
		}
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: marshal json: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	if *modelFlag != "" && len(ids) == 1 {
		// Detailed view for a single model.
		id := ids[0]
		e := modelsMap[id]
		fmt.Printf("id:                    %s\n", id)
		if e.ProviderSystem != "" {
			fmt.Printf("provider_system:       %s\n", e.ProviderSystem)
		}
		if inputCost := catalogCostInputPerM(e); inputCost > 0 {
			fmt.Printf("cost_input_per_m:      $%.2f\n", inputCost)
			fmt.Printf("cost_output_per_m:     $%.2f\n", catalogCostOutputPerM(e))
		}
		if e.SWEBenchVerified > 0 {
			fmt.Printf("swe_bench_verified:    %.1f\n", e.SWEBenchVerified)
		}
		if e.SpeedTokensPerSec > 0 {
			fmt.Printf("speed_tokens_per_sec:  %.1f\n", e.SpeedTokensPerSec)
		}
		if openRouterID := catalogOpenRouterID(e); openRouterID != "" {
			fmt.Printf("openrouter_id:         %s\n", openRouterID)
		}
		return 0
	}

	// Table view.
	fmt.Printf("%-28s %-12s %-12s %-12s %s\n", "MODEL", "PROVIDER", "INPUT/MTok", "OUTPUT/MTok", "SWE-bench")
	for _, id := range ids {
		e := modelsMap[id]
		inputStr := "-"
		outputStr := "-"
		sweStr := "-"
		if inputCost := catalogCostInputPerM(e); inputCost > 0 {
			inputStr = fmt.Sprintf("$%.2f", inputCost)
		}
		if outputCost := catalogCostOutputPerM(e); outputCost > 0 {
			outputStr = fmt.Sprintf("$%.2f", outputCost)
		}
		if e.SWEBenchVerified > 0 {
			sweStr = fmt.Sprintf("%.1f", e.SWEBenchVerified)
		}
		fmt.Printf("%-28s %-12s %-12s %-12s %s\n",
			id,
			e.ProviderSystem,
			inputStr,
			outputStr,
			sweStr,
		)
	}
	return 0
}

func catalogCostInputPerM(e modelcatalog.ModelEntry) float64 {
	if e.CostInputPerM != 0 {
		return e.CostInputPerM
	}
	return e.CostInputPerMTok
}

func catalogCostOutputPerM(e modelcatalog.ModelEntry) float64 {
	if e.CostOutputPerM != 0 {
		return e.CostOutputPerM
	}
	return e.CostOutputPerMTok
}

func catalogOpenRouterID(e modelcatalog.ModelEntry) string {
	if e.OpenRouterID != "" {
		return e.OpenRouterID
	}
	return e.OpenRouterRefID
}

// observationRow is a JSON-serializable observation entry.
type observationRow struct {
	ProviderSystem        string  `json:"provider_system"`
	Model                 string  `json:"model"`
	Samples               int     `json:"samples"`
	AvgOutputTokensPerSec float64 `json:"avg_output_tokens_per_sec"`
}

func cmdCatalogObservations(_ string, args []string) int {
	fs := flag.NewFlagSet("catalog observations", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	formatFlag := fs.String("format", "", "Output format: json")
	providerFlag := fs.String("provider", "", "Filter by provider")
	modelFlag := fs.String("model", "", "Filter by model")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	storePath := observations.DefaultStorePath()
	store, err := observations.LoadStore(storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load observations: %v\n", err)
		return 1
	}

	// Collect and filter rows.
	var rows []observationRow
	for _, key := range store.AllKeys() {
		if *providerFlag != "" && key.ProviderSystem != *providerFlag {
			continue
		}
		if *modelFlag != "" && key.Model != *modelFlag {
			continue
		}
		ring := store.RingFor(key)
		if ring == nil || len(ring.Samples) == 0 {
			continue
		}
		mean, ok := ring.Mean()
		if !ok {
			// All-zero samples — omit.
			continue
		}
		rows = append(rows, observationRow{
			ProviderSystem:        key.ProviderSystem,
			Model:                 key.Model,
			Samples:               len(ring.Samples),
			AvgOutputTokensPerSec: mean,
		})
	}

	// Sort by provider then model for deterministic output.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ProviderSystem != rows[j].ProviderSystem {
			return rows[i].ProviderSystem < rows[j].ProviderSystem
		}
		return rows[i].Model < rows[j].Model
	})

	if *formatFlag == "json" {
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: marshal json: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	// Human-readable table.
	if len(rows) == 0 {
		fmt.Println("no observations recorded yet")
		return 0
	}
	fmt.Printf("%-16s %-28s %-8s %s\n", "PROVIDER", "MODEL", "SAMPLES", "AVG_TOK/S")
	for _, r := range rows {
		fmt.Printf("%-16s %-28s %-8d %.1f\n",
			r.ProviderSystem, r.Model, r.Samples, r.AvgOutputTokensPerSec)
	}
	return 0
}

func cmdCatalogCheck(workDir string, args []string) int {
	fs := flag.NewFlagSet("catalog check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("base-url", defaultCatalogBaseURL, "Published catalog base URL")
	channel := fs.String("channel", "stable", "Channel to inspect")
	version := fs.String("version", "", "Exact catalog version to inspect")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	_, catalog, _, err := loadCatalogRuntime(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	local := catalog.Metadata()

	index, _, err := fetchCatalogIndex(*baseURL, *channel, *version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	fmt.Printf("local_catalog_version: %s\n", blankIfEmpty(local.CatalogVersion))
	fmt.Printf("local_source: %s\n", local.ManifestSource)
	fmt.Printf("remote_catalog_version: %s\n", index.CatalogVersion)
	fmt.Printf("remote_schema_version: %d\n", index.SchemaVersion)
	fmt.Printf("remote_published_at: %s\n", index.PublishedAt)

	if local.CatalogVersion == index.CatalogVersion && local.CatalogVersion != "" {
		fmt.Println("status: up-to-date")
		return 0
	}
	fmt.Println("status: update-available")
	return 0
}

func cmdCatalogUpdate(workDir string, args []string) int {
	fs := flag.NewFlagSet("catalog update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("base-url", defaultCatalogBaseURL, "Published catalog base URL")
	channel := fs.String("channel", "stable", "Channel to install")
	version := fs.String("version", "", "Exact catalog version to install")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, _, manifestPath, err := loadCatalogRuntime(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	if cfg == nil {
		cfg = &agentConfig.Config{}
	}

	index, indexURL, err := fetchCatalogIndex(*baseURL, *channel, *version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	manifestURL, err := resolveRelativeURL(indexURL, index.ManifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	manifestData, err := fetchURL(manifestURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	sum := sha256.Sum256(manifestData)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, index.ManifestSHA256) {
		fmt.Fprintf(os.Stderr, "error: checksum mismatch: expected %s got %s\n", index.ManifestSHA256, got)
		return 1
	}

	if err := safefs.MkdirAll(filepath.Dir(manifestPath), 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "error: create manifest directory: %v\n", err)
		return 1
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(manifestPath), "models-*.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: create temp manifest: %v\n", err)
		return 1
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.Write(manifestData); err != nil {
		if closeErr := tmpFile.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "error: close temp manifest after write failure: %v\n", closeErr)
		}
		fmt.Fprintf(os.Stderr, "error: write temp manifest: %v\n", err)
		return 1
	}
	if err := tmpFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "error: close temp manifest: %v\n", err)
		return 1
	}

	if _, err := modelcatalog.Load(modelcatalog.LoadOptions{
		ManifestPath:    tmpPath,
		RequireExternal: true,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: validate downloaded manifest: %v\n", err)
		return 1
	}

	if err := os.Rename(tmpPath, manifestPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: install manifest: %v\n", err)
		return 1
	}

	fmt.Printf("installed catalog %s to %s\n", index.CatalogVersion, manifestPath)
	return 0
}

func cmdCatalogUpdatePricing(_ string, args []string) int {
	fs := flag.NewFlagSet("catalog update-pricing", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolve config dir: %v\n", err)
		return 1
	}
	manifestPath := filepath.Join(configDir, "agent", "models.yaml")

	updated, notFound, err := modelcatalog.UpdateManifestPricing(manifestPath, 15*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("Updated %d model(s) → %s\n", updated, manifestPath)
	if len(notFound) > 0 {
		fmt.Printf("Not found on OpenRouter: %s\n", strings.Join(notFound, ", "))
	}
	return 0
}

func loadCatalogRuntime(workDir string) (*agentConfig.Config, *modelcatalog.Catalog, string, error) {
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		return nil, nil, "", err
	}
	catalog, err := cfg.LoadModelCatalog()
	if err != nil {
		return nil, nil, "", err
	}
	return cfg, catalog, catalogManifestPath(cfg), nil
}

func catalogManifestPath(cfg *agentConfig.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.ModelCatalog.Manifest) != "" {
		return cfg.ModelCatalog.Manifest
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".config", "agent", "models.yaml")
	}
	return filepath.Join(configDir, "agent", "models.yaml")
}

func printResolvedSurface(catalog *modelcatalog.Catalog, ref string, surface modelcatalog.Surface) {
	resolved, err := catalog.Resolve(ref, modelcatalog.ResolveOptions{Surface: surface})
	if err != nil {
		var missingSurfaceErr *modelcatalog.MissingSurfaceError
		if errors.As(err, &missingSurfaceErr) {
			return
		}
		fmt.Printf("  %s: error=%s\n", surface, err)
		return
	}
	line := fmt.Sprintf("  %s: %s", surface, resolved.ConcreteModel)
	if resolved.SurfacePolicy.ReasoningDefault != "" {
		line += fmt.Sprintf(" (reasoning %s)", resolved.SurfacePolicy.ReasoningDefault)
	}
	fmt.Println(line)
}

func fetchCatalogIndex(baseURL, channel, version string) (catalogdist.Index, string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return catalogdist.Index{}, "", fmt.Errorf("catalog base URL is required")
	}
	channel = strings.TrimSpace(channel)
	version = strings.TrimSpace(version)

	indexPath := "stable/index.json"
	if version != "" {
		indexPath = path.Join("versions", version, "index.json")
	} else if channel != "" {
		indexPath = path.Join(channel, "index.json")
	}

	indexURL := baseURL + "/" + indexPath
	data, err := fetchURL(indexURL)
	if err != nil {
		return catalogdist.Index{}, "", err
	}
	var index catalogdist.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return catalogdist.Index{}, "", fmt.Errorf("decode catalog index %s: %w", indexURL, err)
	}
	return index, indexURL, nil
}

func fetchURL(rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL) // #nosec G107 -- URL is explicit operator input for catalog sync commands.
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetch %s: status %d: %s", rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	return data, nil
}

func resolveRelativeURL(baseURL, rel string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(rel)
	if err != nil {
		return "", err
	}
	return u.ResolveReference(ref).String(), nil
}

func blankIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
