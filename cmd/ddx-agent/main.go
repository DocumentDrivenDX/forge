// Command ddx-agent is a standalone CLI that wraps the agent library.
// It proves the library works end-to-end and serves as the DDx harness backend.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent"
	agentConfig "github.com/DocumentDrivenDX/agent/config"
	"github.com/DocumentDrivenDX/agent/occompat"
	"github.com/DocumentDrivenDX/agent/picompat"
	"github.com/DocumentDrivenDX/agent/prompt"
	"github.com/DocumentDrivenDX/agent/session"
	"github.com/DocumentDrivenDX/agent/tool"
)

// Version info set via -ldflags.
var (
	Version   = "dev"
	BuildTime = ""
	GitCommit = ""
)

func main() {
	os.Exit(run())
}

func run() int {
	// Define flags
	promptFlag := flag.String("p", "", "Prompt (use @file to read from file)")
	jsonOutput := flag.Bool("json", false, "Output result as JSON")
	providerFlag := flag.String("provider", "", "Named provider from config (e.g., vidar, openrouter)")
	backendFlag := flag.String("backend", "", "Named backend pool from config (e.g., code-fast-local)")
	model := flag.String("model", "", "Explicit model name override (bypasses catalog)")
	modelRef := flag.String("model-ref", "", "Model catalog reference (alias, profile, or canonical target)")
	allowDeprecatedModel := flag.Bool("allow-deprecated-model", false, "Allow deprecated model catalog references")
	maxIter := flag.Int("max-iter", 0, "Max iterations")
	workDir := flag.String("work-dir", "", "Working directory")
	version := flag.Bool("version", false, "Print version")
	sysPromptFlag := flag.String("system", "", "System prompt (appended to preset)")
	presetFlag := flag.String("preset", "", "System prompt preset (agent, benchmark, claude, codex, cursor, minimal)")

	flag.Parse()

	if *version {
		fmt.Printf("ddx-agent %s (commit %s, built %s)\n", Version, GitCommit, BuildTime)
		return 0
	}

	// Resolve working directory early (needed for config loading)
	wd := *workDir
	if wd == "" {
		wd, _ = os.Getwd()
	}

	// Handle subcommands
	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "log":
			return cmdLog(wd, args[1:])
		case "replay":
			return cmdReplay(wd, args[1:])
		case "models":
			return cmdModels(wd, *providerFlag, args[1:])
		case "check":
			return cmdCheck(wd, *providerFlag, args[1:])
		case "providers":
			return cmdProviders(wd, *jsonOutput)
		case "import":
			return cmdImport(wd, args[1:])
		case "version":
			return cmdVersion(args[1:])
		case "update":
			return cmdUpdate(args[1:])
		}
	}

	// Load config
	cfg, err := agentConfig.Load(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}

	// Check for zero-config discovery
	checkZeroConfigDiscovery(cfg)

	// Check for drift
	checkDrift(cfg, wd)

	// Resolve prompt
	promptText, err := resolvePrompt(*promptFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}
	if promptText == "" {
		fmt.Fprintln(os.Stderr, "error: no prompt provided (use -p or pipe to stdin)")
		flag.Usage()
		return 2
	}

	overrides := agentConfig.ProviderOverrides{
		Model:           *model,
		ModelRef:        *modelRef,
		AllowDeprecated: *allowDeprecatedModel,
	}
	_, p, _, err := resolveProviderForRun(cfg, wd, *backendFlag, *providerFlag, overrides)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}

	// Resolve max iterations
	iterations := cfg.MaxIterations
	if *maxIter > 0 {
		iterations = *maxIter
	}

	// Build tools
	tools := []agent.Tool{
		&tool.ReadTool{WorkDir: wd},
		&tool.WriteTool{WorkDir: wd},
		&tool.EditTool{WorkDir: wd},
		&tool.BashTool{WorkDir: wd},
		&tool.GlobTool{WorkDir: wd},
		&tool.GrepTool{WorkDir: wd},
		&tool.LsTool{WorkDir: wd},
		&tool.PatchTool{WorkDir: wd},
	}

	// Build system prompt
	preset := *presetFlag
	if preset == "" {
		preset = cfg.Preset
	}
	if preset == "" {
		preset = "agent"
	}
	sysPrompt := prompt.NewFromPreset(preset).
		WithTools(tools).
		WithContextFiles(prompt.LoadContextFiles(wd)).
		WithWorkDir(wd)

	if *sysPromptFlag != "" {
		sysPrompt.WithAppend(*sysPromptFlag)
	}

	// Session logger
	sessionID := fmt.Sprintf("s-%d", os.Getpid())
	logger := session.NewLogger(cfg.SessionLogDir, sessionID)
	defer logger.Close()

	// Build request
	req := agent.Request{
		Prompt:        promptText,
		SystemPrompt:  sysPrompt.Build(),
		Provider:      p,
		Tools:         tools,
		MaxIterations: iterations,
		WorkDir:       wd,
		Callback:      logger.Callback(),
	}

	// Run with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	result, err := agent.Run(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	// Output result
	if *jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(result.Output)
		if result.Output != "" && !strings.HasSuffix(result.Output, "\n") {
			fmt.Println()
		}
	}

	// Status to stderr
	fmt.Fprintf(os.Stderr, "[%s] tokens: %d in / %d out", result.Status, result.Tokens.Input, result.Tokens.Output)
	if result.CostUSD > 0 {
		fmt.Fprintf(os.Stderr, " | cost: $%.4f", result.CostUSD)
	}
	fmt.Fprintln(os.Stderr)

	switch result.Status {
	case agent.StatusSuccess:
		return 0
	default:
		return 1
	}
}

// resolveProviderForRun selects and builds the provider for a run.
//
// Resolution order (matching SD-005):
//  1. If backendName is non-empty, resolve that backend pool.
//  2. Else if cfg.DefaultBackend is set and providerName is empty, resolve
//     the default backend pool.
//  3. Else fall back to direct provider selection (providerName or cfg.DefaultName).
func resolveProviderForRun(cfg *agentConfig.Config, workDir, backendName, providerName string, overrides agentConfig.ProviderOverrides) (string, agent.Provider, agentConfig.ProviderConfig, error) {
	// Determine whether to use a backend pool.
	effectiveBackend := backendName
	if effectiveBackend == "" && providerName == "" {
		effectiveBackend = cfg.DefaultBackend
	}

	if effectiveBackend != "" {
		counter, err := readAndIncrementBackendCounter(workDir, effectiveBackend)
		if err != nil {
			// Non-fatal: fall back to counter 0.
			counter = 0
		}
		p, pc, _, err := cfg.ResolveBackend(effectiveBackend, counter, overrides)
		if err != nil {
			return "", nil, agentConfig.ProviderConfig{}, err
		}
		return effectiveBackend, p, pc, nil
	}

	// Direct provider selection.
	if providerName == "" {
		providerName = cfg.DefaultName()
	}
	p, pc, _, err := cfg.BuildProviderWithOverrides(providerName, overrides)
	if err != nil {
		return "", nil, agentConfig.ProviderConfig{}, err
	}
	return providerName, p, pc, nil
}

// backendStateFile returns the path to the per-backend round-robin counter file.
func backendStateFile(workDir, backendName string) string {
	return filepath.Join(workDir, ".agent", "backend-state-"+backendName+".counter")
}

// backendCounterMu serializes counter reads and writes within a process.
var backendCounterMu sync.Mutex

// readAndIncrementBackendCounter reads the current round-robin counter for a
// backend pool, increments it, writes it back, and returns the value that was
// read (i.e. the counter to use for this request).
func readAndIncrementBackendCounter(workDir, backendName string) (int, error) {
	backendCounterMu.Lock()
	defer backendCounterMu.Unlock()

	path := backendStateFile(workDir, backendName)

	// Read existing counter; treat missing file as counter 0.
	var counter int
	if data, err := os.ReadFile(path); err == nil {
		if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &counter); err != nil {
			counter = 0
		}
	}

	// Write incremented counter.
	next := counter + 1
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", next)), 0600)

	return counter, nil
}

func resolvePrompt(p string) (string, error) {
	if p != "" {
		if strings.HasPrefix(p, "@") {
			data, err := os.ReadFile(p[1:])
			if err != nil {
				return "", fmt.Errorf("reading prompt file: %w", err)
			}
			return string(data), nil
		}
		return p, nil
	}

	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}

	return "", nil
}

func cmdProviders(workDir string, jsonOut bool) int {
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	if jsonOut {
		data, _ := json.MarshalIndent(cfg.Providers, "", "  ")
		fmt.Println(string(data))
		return 0
	}

	defName := cfg.DefaultName()
	fmt.Printf("%-12s %-15s %-40s %-30s %s\n", "NAME", "TYPE", "URL", "MODEL", "STATUS")
	for _, name := range cfg.ProviderNames() {
		pc := cfg.Providers[name]
		status := checkProviderStatus(pc)
		marker := " "
		if name == defName {
			marker = "*"
		}
		url := pc.BaseURL
		if url == "" {
			url = "(api)"
		}
		if len(url) > 38 {
			url = url[:38] + ".."
		}
		modelStr := pc.Model
		if len(modelStr) > 28 {
			modelStr = modelStr[:28] + ".."
		}
		fmt.Printf("%s%-11s %-15s %-40s %-30s %s\n", marker, name, pc.Type, url, modelStr, status)
	}
	return 0
}

func cmdModels(workDir, providerName string, args []string) int {
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	showAll := len(args) > 0 && args[0] == "--all"

	if showAll {
		for _, name := range cfg.ProviderNames() {
			pc := cfg.Providers[name]
			fmt.Printf("[%s]\n", name)
			models := listModels(pc)
			if len(models) == 0 {
				fmt.Println("  (unavailable)")
			} else {
				for _, m := range models {
					fmt.Printf("  %s\n", m)
				}
			}
			fmt.Println()
		}
		return 0
	}

	name := providerName
	if name == "" {
		name = cfg.DefaultName()
	}
	pc, ok := cfg.GetProvider(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "error: unknown provider %q\n", name)
		return 1
	}

	if pc.Type == "anthropic" {
		fmt.Println("Anthropic does not support model listing.")
		fmt.Printf("Configured model: %s\n", pc.Model)
		return 0
	}

	models := listModels(pc)
	for _, m := range models {
		fmt.Println(m)
	}
	return 0
}

func cmdCheck(workDir, providerName string, args []string) int {
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	checkAll := len(args) > 0 && args[0] == "--all"

	if checkAll {
		allOk := true
		for _, name := range cfg.ProviderNames() {
			pc := cfg.Providers[name]
			status := checkProviderStatus(pc)
			ok := strings.Contains(status, "connected") || strings.Contains(status, "configured")
			marker := "ok"
			if !ok {
				marker = "FAIL"
				allOk = false
			}
			fmt.Printf("[%s] %s: %s\n", marker, name, status)
		}
		if !allOk {
			return 1
		}
		return 0
	}

	name := providerName
	if name == "" {
		name = cfg.DefaultName()
	}
	pc, ok := cfg.GetProvider(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "error: unknown provider %q\n", name)
		return 1
	}

	fmt.Printf("Provider: %s (%s)\n", name, pc.Type)
	if pc.BaseURL != "" {
		fmt.Printf("URL:      %s\n", pc.BaseURL)
	}
	status := checkProviderStatus(pc)
	fmt.Printf("Status:   %s\n", status)
	if pc.Model != "" {
		fmt.Printf("Model:    %s\n", pc.Model)
	}

	if strings.Contains(status, "unreachable") || strings.Contains(status, "no API") {
		return 1
	}
	return 0
}

func cmdLog(workDir string, args []string) int {
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	if len(args) > 0 {
		path := filepath.Join(cfg.SessionLogDir, args[0]+".jsonl")
		events, err := session.ReadEvents(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			return 1
		}
		for _, e := range events {
			data, _ := json.MarshalIndent(e, "", "  ")
			fmt.Println(string(data))
		}
		return 0
	}

	entries, err := os.ReadDir(cfg.SessionLogDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			name := strings.TrimSuffix(e.Name(), ".jsonl")
			info, _ := e.Info()
			if info != nil {
				fmt.Printf("%s  %s  %d bytes\n", name, info.ModTime().Format("2006-01-02 15:04"), info.Size())
			} else {
				fmt.Println(name)
			}
		}
	}
	return 0
}

func cmdReplay(workDir string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ddx-agent replay <session-id>")
		return 2
	}
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	path := filepath.Join(cfg.SessionLogDir, args[0]+".jsonl")
	if err := session.Replay(path, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	return 0
}

func checkProviderStatus(pc agentConfig.ProviderConfig) string {
	if pc.Type == "anthropic" {
		if pc.APIKey == "" {
			return "no API key"
		}
		return "api key configured"
	}

	url := pc.BaseURL
	if url == "" {
		return "no URL configured"
	}
	modelsURL := strings.TrimSuffix(url, "/") + "/models"
	if strings.HasSuffix(url, "/v1") {
		modelsURL = url + "/models"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(modelsURL)
	if err != nil {
		return fmt.Sprintf("unreachable (%s)", strings.Split(err.Error(), ": ")[len(strings.Split(err.Error(), ": "))-1])
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "connected (parse error)"
	}
	return fmt.Sprintf("connected (%d models)", len(result.Data))
}

func listModels(pc agentConfig.ProviderConfig) []string {
	if pc.Type == "anthropic" {
		return nil
	}
	url := pc.BaseURL
	if url == "" {
		return nil
	}
	modelsURL := strings.TrimSuffix(url, "/") + "/models"
	if strings.HasSuffix(url, "/v1") {
		modelsURL = url + "/models"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(modelsURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var models []string
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models
}

func cmdImport(workDir string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ddx-agent import <pi|opencode> [--diff] [--merge] [--project]")
		return 2
	}

	source := args[0]
	if source != "pi" && source != "opencode" {
		fmt.Fprintf(os.Stderr, "error: unknown source %q (use 'pi' or 'opencode')\n", source)
		return 2
	}

	// Parse flags
	var diffOnly, merge, project bool
	for _, arg := range args[1:] {
		switch arg {
		case "--diff":
			diffOnly = true
		case "--merge":
			merge = true
		case "--project":
			project = true
		}
	}

	// Determine output path
	var configPath string
	if project {
		fmt.Fprintln(os.Stderr, "ddx-agent: warning: writing API keys to project config (.agent/config.yaml)")
		fmt.Fprintln(os.Stderr, "ddx-agent: ensure .agent/config.yaml is in .gitignore before committing")
		fmt.Fprint(os.Stderr, "Proceed? [y/N] ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			return 0
		}
		configPath = filepath.Join(workDir, ".agent", "config.yaml")
		os.MkdirAll(filepath.Join(workDir, ".agent"), 0755)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
			return 1
		}
		configPath = filepath.Join(home, ".config", "agent", "config.yaml")
		os.MkdirAll(filepath.Dir(configPath), 0755)
	}

	// Import based on source
	if source == "pi" {
		return importPi(configPath, diffOnly, merge)
	} else {
		return importOpenCode(configPath, diffOnly, merge)
	}
}

func importPi(configPath string, diffOnly, merge bool) int {
	piDir := picompat.DefaultPiDir()
	if piDir == "" {
		fmt.Fprintln(os.Stderr, "error: cannot determine pi config directory")
		return 1
	}

	if !picompat.CheckExists() {
		fmt.Fprintf(os.Stderr, "error: pi config not found at %s\n", piDir)
		return 1
	}

	// Translate pi config
	result, err := picompat.Translate(piDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	// Show diff if requested
	if diffOnly {
		return showDiff("pi", result)
	}

	// Compute source hash
	sourceHash, _ := picompat.ComputeSourceHash(piDir)

	// Load existing config
	cfg, err := agentConfig.Load(filepath.Dir(configPath))
	if err != nil {
		cfg = &agentConfig.Config{}
	}

	// Merge or replace
	if merge {
		for name, pc := range result.Providers {
			if existing, exists := cfg.Providers[name]; exists {
				// Update api_key only for existing providers
				existing.APIKey = pc.APIKey
				cfg.Providers[name] = existing
				fmt.Printf("updated: %s\n", name)
			} else {
				cfg.Providers[name] = pc
				fmt.Printf("added: %s\n", name)
			}
		}
	} else {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]agentConfig.ProviderConfig)
		}
		for name, pc := range result.Providers {
			cfg.Providers[name] = pc
			fmt.Printf("imported: %s\n", name)
		}
	}

	// Set default
	if result.Default != "" {
		cfg.Default = result.Default
	}

	// Set import metadata
	cfg.ImportedFrom = &agentConfig.ImportMetadata{
		Source:     "pi",
		Timestamp:  time.Now().Format(time.RFC3339),
		SourceHash: sourceHash,
	}

	// Write config with secure permissions
	if err := writeConfig(configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	// Show warnings
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "ddx-agent: warning: %s\n", w)
	}

	fmt.Printf("imported to %s\n", configPath)
	return 0
}

func importOpenCode(configPath string, diffOnly, merge bool) int {
	opencodeDir := occompat.DefaultOpenCodeDir()
	if opencodeDir == "" {
		fmt.Fprintln(os.Stderr, "error: cannot determine opencode config directory")
		return 1
	}

	if !occompat.CheckExists() {
		fmt.Fprintf(os.Stderr, "error: opencode config not found at %s\n", opencodeDir)
		return 1
	}

	// Load auth
	auth, err := occompat.LoadAuth(opencodeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	// Get the API key from auth (use first entry)
	var authKey string
	for _, entry := range auth {
		authKey = entry.Key
		break
	}

	// Translate
	result := occompat.Translate(opencodeDir, authKey)

	// Show diff if requested
	if diffOnly {
		return showOpenCodeDiff(result)
	}

	// Compute source hash
	sourceHash, _ := occompat.ComputeSourceHash(opencodeDir)

	// Load existing config
	cfg, err := agentConfig.Load(filepath.Dir(configPath))
	if err != nil {
		cfg = &agentConfig.Config{}
	}

	// Merge or replace
	name := "opencode"
	if merge {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]agentConfig.ProviderConfig)
		}
		if existing, exists := cfg.Providers[name]; exists {
			existing.APIKey = result.Provider.APIKey
			cfg.Providers[name] = existing
			fmt.Printf("updated: %s\n", name)
		} else {
			cfg.Providers[name] = result.Provider
			fmt.Printf("added: %s\n", name)
		}
	} else {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]agentConfig.ProviderConfig)
		}
		cfg.Providers[name] = result.Provider
		fmt.Printf("imported: %s\n", name)
	}

	// Set import metadata
	cfg.ImportedFrom = &agentConfig.ImportMetadata{
		Source:     "opencode",
		Timestamp:  time.Now().Format(time.RFC3339),
		SourceHash: sourceHash,
	}

	// Write config with secure permissions
	if err := writeConfig(configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	// Show warnings
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "ddx-agent: warning: %s\n", w)
	}

	fmt.Printf("imported to %s\n", configPath)
	return 0
}

func showDiff(source string, result *picompat.TranslationResult) int {
	fmt.Printf("ddx-agent: %s config -- what would be imported:\n\n", source)
	for name, pc := range result.Providers {
		redactedKey := redactKey(pc.APIKey)
		fmt.Printf("[%s]\n", name)
		fmt.Printf("  type:    %s\n", pc.Type)
		if pc.BaseURL != "" {
			fmt.Printf("  url:     %s\n", pc.BaseURL)
		}
		if redactedKey != "" {
			fmt.Printf("  api_key: %s\n", redactedKey)
		}
		if pc.Model != "" {
			fmt.Printf("  model:   %s\n", pc.Model)
		}
		fmt.Println()
	}
	if result.Default != "" {
		fmt.Printf("default: %s\n", result.Default)
	}
	if len(result.Warnings) > 0 {
		fmt.Println("\nWarnings:")
		for _, w := range result.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}
	return 0
}

func showOpenCodeDiff(result *occompat.TranslationResult) int {
	fmt.Println("ddx-agent: opencode config -- what would be imported:")
	pc := result.Provider
	redactedKey := redactKey(pc.APIKey)
	fmt.Println("[opencode]")
	fmt.Printf("  type:    %s\n", pc.Type)
	if pc.BaseURL != "" {
		fmt.Printf("  url:     %s\n", pc.BaseURL)
	}
	if redactedKey != "" {
		fmt.Printf("  api_key: %s\n", redactedKey)
	}
	if len(pc.Headers) > 0 {
		fmt.Println("  headers:")
		for k, v := range pc.Headers {
			fmt.Printf("    %s: %s\n", k, v)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Println("\nWarnings:")
		for _, w := range result.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}
	return 0
}

func redactKey(key string) string {
	if key == "" || len(key) < 10 {
		return key
	}
	if len(key) <= 10 {
		return key
	}
	return key[:6] + "..." + key[len(key)-4:]
}

func writeConfig(path string, cfg *agentConfig.Config) error {
	data, err := agentConfig.Save(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// checkZeroConfigDiscovery emits a notice if no config exists but pi/opencode configs do.
func checkZeroConfigDiscovery(cfg *agentConfig.Config) {
	// Check if any providers are configured
	if len(cfg.Providers) > 0 {
		return
	}

	// Check for standard env vars
	if os.Getenv("ANTHROPIC_API_KEY") != "" ||
		os.Getenv("OPENAI_API_KEY") != "" ||
		os.Getenv("OPENROUTER_API_KEY") != "" {
		return
	}

	// Check for pi config
	if picompat.CheckExists() {
		piDir := picompat.DefaultPiDir()
		fmt.Fprintf(os.Stderr, "ddx-agent: no providers configured. Found pi config at %s — run 'ddx-agent import pi' to import.\n", piDir)
		return
	}

	// Check for opencode config
	if occompat.CheckExists() {
		opencodeDir := occompat.DefaultOpenCodeDir()
		fmt.Fprintf(os.Stderr, "ddx-agent: no providers configured. Found opencode config at %s — run 'ddx-agent import opencode' to import.\n", opencodeDir)
		return
	}
}

// checkDrift emits a notice if the source config has changed since import.
func checkDrift(cfg *agentConfig.Config, workDir string) {
	if cfg.ImportedFrom == nil || cfg.ImportedFrom.Source == "" {
		return
	}

	// Check daily debounce
	if !shouldCheckDrift(cfg.ImportedFrom.Source) {
		return
	}

	// Compute current hash
	var currentHash string
	var err error

	switch cfg.ImportedFrom.Source {
	case "pi":
		currentHash, err = picompat.ComputeSourceHash(picompat.DefaultPiDir())
	case "opencode":
		currentHash, err = occompat.ComputeSourceHash(occompat.DefaultOpenCodeDir())
	}

	if err != nil {
		return // Can't check, just skip
	}

	if currentHash != cfg.ImportedFrom.SourceHash {
		fmt.Fprintf(os.Stderr, "ddx-agent: %s config changed since import — run 'ddx-agent import %s --diff' to review\n",
			cfg.ImportedFrom.Source, cfg.ImportedFrom.Source)
	}
}

// shouldCheckDrift checks if we should perform a drift check (once per day).
func shouldCheckDrift(source string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}

	checkFile := filepath.Join(home, ".config", "agent", ".import-check-"+source)

	// Check if file exists and is recent (within 24 hours)
	info, err := os.Stat(checkFile)
	if err == nil {
		// File exists, check age
		age := time.Since(info.ModTime())
		if age < 24*time.Hour {
			return false
		}
	}

	// Update check file
	os.WriteFile(checkFile, []byte(time.Now().Format(time.RFC3339)), 0644)
	return true
}

// cmdVersion shows version with update availability check.
func cmdVersion(args []string) int {
	checkOnly := false
	for _, arg := range args {
		if arg == "--check-only" || arg == "-c" {
			checkOnly = true
		}
	}

	fmt.Printf("ddx-agent %s (commit %s, built %s)\n", Version, GitCommit, BuildTime)

	// Skip update check for dev builds or if requested
	if Version == "dev" || checkOnly {
		return 0
	}

	// Check for updates
	home, err := os.UserHomeDir()
	if err != nil {
		return 0 // Can't determine home dir, skip update check
	}
	cacheFile := filepath.Join(home, ".cache", "agent", "latest-version.json")

	release, err := GetLatestRelease(githubRepo, cacheFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (update check failed: %v)\n", err)
		return 0
	}

	currentVer, err := ParseSemVer(Version)
	if err != nil {
		return 0 // Invalid version format, skip comparison
	}

	latestVer, err := ParseSemVer(release.TagName)
	if err != nil {
		return 0 // Invalid latest version, skip comparison
	}

	if currentVer.Less(latestVer) {
		fmt.Printf("  Update available: %s\n", release.TagName)
	} else {
		fmt.Println("  (latest)")
	}

	return 0
}

// cmdUpdate checks for and performs updates.
func cmdUpdate(args []string) int {
	checkOnly := false
	force := false
	for _, arg := range args {
		switch arg {
		case "--check-only", "-c":
			checkOnly = true
		case "--force", "-f":
			force = true
		}
	}

	// Get current binary path
	binaryPath, err := FindBinaryPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		return 1
	}
	cacheFile := filepath.Join(home, ".cache", "agent", "latest-version.json")

	// Get latest release
	release, err := GetLatestRelease(githubRepo, cacheFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	currentVer, err := ParseSemVer(Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid current version format: %s\n", Version)
		return 1
	}

	latestVer, err := ParseSemVer(release.TagName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid latest version format: %s\n", release.TagName)
		return 1
	}

	fmt.Printf("Current: %s\n", Version)
	fmt.Printf("Latest:  %s\n", release.TagName)

	if !currentVer.Less(latestVer) {
		fmt.Println("\nYou are up to date!")
		return 0
	}

	if checkOnly {
		fmt.Println("\nUpdate available. Run 'ddx-agent update' to upgrade.")
		return 1 // Exit code 1 indicates outdated
	}

	// Show changelog summary (first few lines)
	fmt.Printf("\nRelease notes:\n")
	lines := strings.Split(release.Body, "\n")
	for i, line := range lines {
		if i >= 5 || (i > 0 && strings.HasPrefix(line, "##")) {
			break
		}
		if line != "" {
			fmt.Printf("  %s\n", line)
		}
	}

	// Prompt user
	if !force {
		fmt.Print("\nUpdate to " + release.TagName + "? [y/N] ")

		buf := make([]byte, 1)
		if _, err := os.Stdin.Read(buf); err != nil {
			fmt.Println("\nCancelled.")
			return 0
		}

		response := strings.ToLower(strings.TrimSpace(string(buf)))
		if response != "y" && response != "yes" {
			fmt.Println("Cancelled. Run 'ddx-agent update --force' to skip prompt.")
			return 0
		}
	}

	// Download new binary
	tmpPath, err := DownloadBinary(release.TagName, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		return 1
	}
	defer os.Remove(tmpPath) // Clean up temp file

	// Replace old binary
	if err := ReplaceBinary(binaryPath, tmpPath, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("\nSuccessfully updated to %s!\n", release.TagName)
	return 0
}
