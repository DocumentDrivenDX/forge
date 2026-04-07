// Command forge is a standalone CLI that wraps the forge library.
// It proves the library works end-to-end and serves as the DDx harness backend.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/anthropics/forge"
	"github.com/anthropics/forge/provider/anthropic"
	oaiProvider "github.com/anthropics/forge/provider/openai"
	"github.com/anthropics/forge/session"
	"github.com/anthropics/forge/tool"
	"gopkg.in/yaml.v3"
)

// Version info set via -ldflags.
var (
	Version   = "dev"
	BuildTime = ""
	GitCommit = ""
)

// config mirrors the YAML config file structure.
type config struct {
	Provider      string `yaml:"provider"`
	BaseURL       string `yaml:"base_url"`
	APIKey        string `yaml:"api_key"`
	Model         string `yaml:"model"`
	MaxIterations int    `yaml:"max_iterations"`
	SessionLogDir string `yaml:"session_log_dir"`
}

func defaultConfig() config {
	return config{
		Provider:      "openai-compat",
		BaseURL:       "http://localhost:1234/v1",
		Model:         "",
		MaxIterations: 20,
		SessionLogDir: ".forge/sessions",
	}
}

func main() {
	os.Exit(run())
}

func run() int {
	// Define flags
	prompt := flag.String("p", "", "Prompt (use @file to read from file)")
	jsonOutput := flag.Bool("json", false, "Output result as JSON")
	provider := flag.String("provider", "", "Provider type (openai-compat or anthropic)")
	baseURL := flag.String("base-url", "", "Provider base URL")
	apiKey := flag.String("api-key", "", "API key")
	model := flag.String("model", "", "Model name")
	maxIter := flag.Int("max-iter", 0, "Max iterations")
	workDir := flag.String("work-dir", "", "Working directory")
	version := flag.Bool("version", false, "Print version")
	systemPrompt := flag.String("system", "", "System prompt")

	flag.Parse()

	if *version {
		fmt.Printf("forge %s (commit %s, built %s)\n", Version, GitCommit, BuildTime)
		return 0
	}

	// Handle subcommands
	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "log":
			return cmdLog(args[1:])
		case "replay":
			return cmdReplay(args[1:])
		case "version":
			fmt.Printf("forge %s (commit %s, built %s)\n", Version, GitCommit, BuildTime)
			return 0
		}
	}

	// Load config
	cfg := loadConfig()
	applyEnv(&cfg)
	applyFlags(&cfg, *provider, *baseURL, *apiKey, *model, *maxIter)

	// Resolve prompt
	promptText, err := resolvePrompt(*prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}
	if promptText == "" {
		fmt.Fprintln(os.Stderr, "error: no prompt provided (use -p or pipe to stdin)")
		flag.Usage()
		return 2
	}

	// Resolve working directory
	wd := *workDir
	if wd == "" {
		wd, _ = os.Getwd()
	}

	// Build provider
	p, err := buildProvider(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}

	// Build tools
	tools := []forge.Tool{
		&tool.ReadTool{WorkDir: wd},
		&tool.WriteTool{WorkDir: wd},
		&tool.EditTool{WorkDir: wd},
		&tool.BashTool{WorkDir: wd},
	}

	// Session logger
	sessionID := fmt.Sprintf("s-%d", os.Getpid())
	logger := session.NewLogger(cfg.SessionLogDir, sessionID)
	defer logger.Close()

	// Build request
	req := forge.Request{
		Prompt:        promptText,
		SystemPrompt:  *systemPrompt,
		Provider:      p,
		Tools:         tools,
		MaxIterations: cfg.MaxIterations,
		WorkDir:       wd,
		Callback:      logger.Callback(),
	}

	// Run with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	result, err := forge.Run(ctx, req)
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
	case forge.StatusSuccess:
		return 0
	default:
		return 1
	}
}

func loadConfig() config {
	cfg := defaultConfig()

	// Try project config first, then global
	paths := []string{
		".forge/config.yaml",
	}
	home, err := os.UserHomeDir()
	if err == nil {
		paths = append(paths, filepath.Join(home, ".config", "forge", "config.yaml"))
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: invalid config %s: %s\n", p, err)
		}
		break
	}
	return cfg
}

func applyEnv(cfg *config) {
	if v := os.Getenv("FORGE_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("FORGE_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("FORGE_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("FORGE_MODEL"); v != "" {
		cfg.Model = v
	}
}

func applyFlags(cfg *config, provider, baseURL, apiKey, model string, maxIter int) {
	if provider != "" {
		cfg.Provider = provider
	}
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if apiKey != "" {
		cfg.APIKey = apiKey
	}
	if model != "" {
		cfg.Model = model
	}
	if maxIter > 0 {
		cfg.MaxIterations = maxIter
	}
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

	// Try stdin if not a TTY
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

func buildProvider(cfg config) (forge.Provider, error) {
	switch cfg.Provider {
	case "openai-compat", "openai":
		return oaiProvider.New(oaiProvider.Config{
			BaseURL: cfg.BaseURL,
			APIKey:  cfg.APIKey,
			Model:   cfg.Model,
		}), nil
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKey: cfg.APIKey,
			Model:  cfg.Model,
		}), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (use openai-compat or anthropic)", cfg.Provider)
	}
}

func cmdLog(args []string) int {
	cfg := loadConfig()
	applyEnv(&cfg)

	if len(args) > 0 {
		// Show specific session
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

	// List sessions
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

func cmdReplay(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: forge replay <session-id>")
		return 2
	}

	cfg := loadConfig()
	applyEnv(&cfg)

	path := filepath.Join(cfg.SessionLogDir, args[0]+".jsonl")
	if err := session.Replay(path, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	return 0
}
