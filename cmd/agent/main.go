// Command ddx-agent is a standalone CLI that wraps the agent library.
// It proves the library works end-to-end and serves as the DDx harness backend.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/compaction"
	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/prompt"
	oaiProvider "github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
	"github.com/DocumentDrivenDX/agent/internal/session"
	"github.com/DocumentDrivenDX/agent/internal/tool"
	"github.com/DocumentDrivenDX/agent/occompat"
	"github.com/DocumentDrivenDX/agent/picompat"
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
	cliArgs := os.Args[1:]
	runSubcommand, cliArgs := normalizeRunSubcommand(cliArgs)

	fs := flag.NewFlagSet("ddx-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	// Define flags
	promptFlag := fs.String("p", "", "Prompt (use @file to read from file)")
	jsonOutput := fs.Bool("json", false, "Output result as JSON")
	providerFlag := fs.String("provider", "", "Named provider from config (e.g., vidar, openrouter)")
	backendFlag := fs.String("backend", "", "Deprecated named backend pool from config")
	model := fs.String("model", "", "Model route key or explicit concrete model override")
	modelRef := fs.String("model-ref", "", "Model catalog reference (alias, profile, or canonical target)")
	reasoningFlag := fs.String("reasoning", "", "Reasoning control: auto, off, low, medium, high, xhigh, max, or token budget")
	allowDeprecatedModel := fs.Bool("allow-deprecated-model", false, "Allow deprecated model catalog references")
	maxIter := fs.Int("max-iter", 0, "Max iterations")
	workDir := fs.String("work-dir", "", "Working directory")
	version := fs.Bool("version", false, "Print version")
	sysPromptFlag := fs.String("system", "", "System prompt (appended to preset)")
	presetFlag := fs.String("preset", "", "System prompt preset: default, smart, cheap, minimal, benchmark")

	if err := fs.Parse(cliArgs); err != nil {
		return 2
	}

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
	args := fs.Args()
	if !runSubcommand && len(args) > 0 {
		switch args[0] {
		case "log":
			return cmdLog(wd, args[1:])
		case "replay":
			return cmdReplay(wd, args[1:])
		case "usage":
			return cmdUsage(wd, args[1:])
		case "models":
			return cmdModels(wd, *providerFlag, args[1:])
		case "check":
			return cmdCheck(wd, *providerFlag, args[1:])
		case "providers":
			return cmdProviders(wd, *jsonOutput)
		case "catalog":
			return cmdCatalog(wd, args[1:])
		case "route-status":
			return cmdRouteStatus(wd, args[1:])
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
	for _, warning := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "ddx-agent: warning: %s\n", warning)
	}
	if *backendFlag != "" {
		fmt.Fprintln(os.Stderr, "ddx-agent: warning: --backend is deprecated; use --model or --model-ref instead")
	}

	// Check for zero-config discovery
	checkZeroConfigDiscovery(cfg)

	// Fail fast if no provider is configured before we spend tokens on a doomed run.
	// Only applies to actual execution paths, not to help or metadata subcommands.
	// `len(args) == 0` with no prompt flag is ambiguous — skip validation there to
	// preserve the existing "no prompt" error precedence.
	if len(cfg.Providers) == 0 && (*promptFlag != "" || runSubcommand) {
		fmt.Fprintln(os.Stderr, "error: no providers configured — run 'ddx-agent import pi' or 'ddx-agent import opencode', set AGENT_PROVIDER/AGENT_BASE_URL, or create .agent/config.yaml")
		return 2
	}

	// Check for drift
	checkDrift(cfg, wd)

	// Resolve prompt
	var promptText string
	var promptMetadata map[string]string
	if runSubcommand && *promptFlag == "" && len(args) > 0 {
		promptText, promptMetadata, err = parsePromptInput(strings.Join(args, " "))
	} else {
		promptText, promptMetadata, err = resolvePrompt(*promptFlag)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}
	if promptText == "" {
		fmt.Fprintln(os.Stderr, "error: no prompt provided (use -p or pipe to stdin)")
		fs.Usage()
		return 2
	}

	preset, err := resolvePreset(*presetFlag, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}

	overrides := agentConfig.ProviderOverrides{
		Model:           *model,
		ModelRef:        *modelRef,
		AllowDeprecated: *allowDeprecatedModel,
	}
	selection, p, pc, err := resolveProviderForRun(cfg, wd, *backendFlag, *providerFlag, overrides)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}
	resolvedReasoning, err := resolveRunReasoning(cfg, selection, *reasoningFlag)
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
	tools := buildToolsForPreset(wd, preset, bashOutputFilterConfig(cfg.Tools.Bash.OutputFilter))

	// Build system prompt
	sysPrompt := prompt.NewFromPreset(preset).
		WithTools(tools).
		WithContextFiles(prompt.LoadContextFiles(wd)).
		WithWorkDir(wd)

	if *sysPromptFlag != "" {
		sysPrompt.WithAppend(*sysPromptFlag)
	}

	// Session logger
	sessionID := fmt.Sprintf("s-%d", os.Getpid())
	logger := session.NewLogger(sessionLogDir(wd, cfg), sessionID)
	defer logger.Close()

	// Parse reasoning stall timeout
	reasoningStallTimeout, err := cfg.ParseReasoningStallTimeout()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}

	// Signal context is created early so discovery can be cancelled on interrupt.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Resolve model limits. Precedence:
	//   1. Explicit config (pc.ContextWindow / pc.MaxTokens)
	//   2. Live provider API (LookupModelLimits)
	//   3. Model catalog context_window (for servers like LM Studio that omit
	//      context_length from /v1/models entirely)
	resolvedContextWindow := pc.ContextWindow
	resolvedMaxTokens := pc.MaxTokens
	if resolvedContextWindow == 0 || resolvedMaxTokens == 0 {
		limits := agentConfig.LookupModelLimits(ctx, pc, selection.ResolvedModel)
		if resolvedContextWindow == 0 {
			resolvedContextWindow = limits.ContextLength
		}
		if resolvedMaxTokens == 0 {
			resolvedMaxTokens = limits.MaxCompletionTokens
		}
	}
	if resolvedContextWindow == 0 && selection.ResolvedModel != "" {
		if catalog, err := cfg.LoadModelCatalog(); err == nil && catalog != nil {
			resolvedContextWindow = catalog.ContextWindowForModel(selection.ResolvedModel)
		}
	}

	// Build compaction config from resolved context window.
	compactionCfg := compaction.DefaultConfig()
	if resolvedContextWindow > 0 {
		compactionCfg.ContextWindow = resolvedContextWindow
		if resolvedMaxTokens > 0 {
			compactionCfg.ReserveTokens = resolvedMaxTokens
		}
	}
	if cfg.CompactionPercent > 0 {
		compactionCfg.EffectivePercent = cfg.CompactionPercent
	}
	compactor := compaction.NewCompactor(compactionCfg)

	// Build request
	req := agentcore.Request{
		Prompt:                promptText,
		SystemPrompt:          sysPrompt.Build(),
		Provider:              p,
		Tools:                 tools,
		MaxIterations:         iterations,
		WorkDir:               wd,
		Metadata:              promptMetadata,
		SelectedProvider:      selection.Provider,
		SelectedRoute:         selection.Route,
		RequestedModel:        selection.RequestedModel,
		RequestedModelRef:     selection.RequestedModelRef,
		ResolvedModelRef:      selection.ResolvedModelRef,
		ResolvedModel:         selection.ResolvedModel,
		NoStream:              selection.NoStream,
		Callback:              logger.Callback(),
		Telemetry:             cfg.BuildTelemetry(),
		ReasoningByteLimit:    cfg.ReasoningByteLimit,
		ReasoningStallTimeout: reasoningStallTimeout,
		MaxTokens:             resolvedMaxTokens,
		Reasoning:             resolvedReasoning,
		Compactor:             compactor,
	}

	var result agentcore.Result
	if os.Getenv("DDX_AGENT_USE_SERVICE_CONTRACT") == "1" {
		result, err = executeViaServiceContract(ctx, req, agentConfig.NewServiceConfig(cfg, wd))
	} else {
		result, err = agentcore.Run(ctx, req)
	}
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
	case agentcore.StatusSuccess, agentcore.StatusIterationLimit:
		return 0
	default:
		return 1
	}
}

func executeViaServiceContract(ctx context.Context, req agentcore.Request, serviceConfig agent.ServiceConfig) (agentcore.Result, error) {
	svc, err := agent.New(agent.ServiceOptions{ServiceConfig: serviceConfig})
	if err != nil {
		return agentcore.Result{}, err
	}

	temperature := float32(0)
	if req.Temperature != nil {
		temperature = float32(*req.Temperature)
	}
	ch, err := svc.Execute(ctx, agent.ServiceExecuteRequest{
		Prompt:       req.Prompt,
		SystemPrompt: req.SystemPrompt,
		Model:        req.ResolvedModel,
		Provider:     req.SelectedProvider,
		Harness:      "agent",
		WorkDir:      req.WorkDir,
		Temperature:  temperature,
		Seed:         req.Seed,
		Reasoning:    req.Reasoning,
		Metadata:     req.Metadata,
		PreResolved: &agent.RouteDecision{
			Harness:  "agent",
			Provider: req.SelectedProvider,
			Model:    req.ResolvedModel,
			Reason:   "cli pre-resolved provider",
		},
	})
	if err != nil {
		return agentcore.Result{}, err
	}

	result := agentcore.Result{
		SelectedProvider:  req.SelectedProvider,
		SelectedRoute:     req.SelectedRoute,
		RequestedModel:    req.RequestedModel,
		RequestedModelRef: req.RequestedModelRef,
		ResolvedModelRef:  req.ResolvedModelRef,
		ResolvedModel:     req.ResolvedModel,
		Reasoning:         req.Reasoning,
		Model:             req.ResolvedModel,
	}
	var output strings.Builder
	var sawFinal bool
	for ev := range ch {
		decoded, err := agent.DecodeServiceEvent(ev)
		if err != nil {
			return result, fmt.Errorf("decode service event: %w", err)
		}
		switch decoded.Type {
		case agent.ServiceEventTypeRoutingDecision:
			if decoded.RoutingDecision != nil && decoded.RoutingDecision.SessionID != "" {
				result.SessionID = decoded.RoutingDecision.SessionID
			}
		case agent.ServiceEventTypeTextDelta:
			if decoded.TextDelta != nil {
				output.WriteString(decoded.TextDelta.Text)
			}
		case agent.ServiceEventTypeFinal:
			sawFinal = true
			if decoded.Final == nil {
				return result, fmt.Errorf("decode service final event: missing final payload")
			}
			result.Status = serviceStatusToLegacyStatus(decoded.Final.Status)
			if decoded.Final.Error != "" {
				result.Error = errors.New(decoded.Final.Error)
			}
			if decoded.Final.Usage != nil {
				result.Tokens.Input = decoded.Final.Usage.InputTokens
				result.Tokens.Output = decoded.Final.Usage.OutputTokens
				result.Tokens.Total = decoded.Final.Usage.TotalTokens
			}
			result.CostUSD = decoded.Final.CostUSD
			if decoded.Final.RoutingActual != nil {
				result.SelectedProvider = decoded.Final.RoutingActual.Provider
				result.ResolvedModel = decoded.Final.RoutingActual.Model
				result.Model = decoded.Final.RoutingActual.Model
				result.AttemptedProviders = append([]string(nil), decoded.Final.RoutingActual.FallbackChainFired...)
			}
		}
	}
	if !sawFinal {
		return result, fmt.Errorf("service contract execution ended without final event")
	}
	result.Output = output.String()
	return result, nil
}

func serviceStatusToLegacyStatus(status string) agentcore.Status {
	switch status {
	case "success":
		return agentcore.StatusSuccess
	case string(agentcore.StatusIterationLimit):
		return agentcore.StatusIterationLimit
	case "cancelled":
		return agentcore.StatusCancelled
	default:
		return agentcore.StatusError
	}
}

func normalizeRunSubcommand(args []string) (bool, []string) {
	if len(args) == 0 {
		return false, args
	}
	boolFlags := map[string]bool{
		"allow-deprecated-model": true,
		"json":                   true,
		"version":                true,
	}
	valueFlags := map[string]bool{
		"backend":   true,
		"max-iter":  true,
		"model":     true,
		"model-ref": true,
		"p":         true,
		"preset":    true,
		"provider":  true,
		"reasoning": true,
		"system":    true,
		"work-dir":  true,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "run":
			out := append([]string{}, args[:i]...)
			out = append(out, args[i+1:]...)
			return true, out
		case strings.HasPrefix(arg, "-"):
			flagName := strings.TrimLeft(arg, "-")
			if boolFlags[flagName] {
				continue
			}
			if valueFlags[flagName] && i+1 < len(args) {
				i++
			}
		default:
			return false, args
		}
	}

	return false, args
}

// resolveProviderForRun selects and builds the provider for a run.
//
// Resolution order (matching SD-005):
//  1. If backendName is non-empty, resolve that backend pool.
//  2. Else if cfg.DefaultBackend is set and providerName is empty, resolve
//     the default backend pool.
//  3. Else fall back to direct provider selection (providerName or cfg.DefaultName).
type providerSelection struct {
	Route             string
	Provider          string
	RequestedModel    string
	RequestedModelRef string
	ResolvedModelRef  string
	ResolvedModel     string
	ReasoningDefault  agent.Reasoning
	NoStream          bool
}

func resolveRunReasoning(cfg *agentConfig.Config, selection providerSelection, flagValue string) (agent.Reasoning, error) {
	explicit := strings.TrimSpace(flagValue) != ""
	if !explicit {
		return selection.ReasoningDefault, nil
	}
	policy, err := reasoning.ParseString(flagValue)
	if err != nil {
		return "", err
	}
	if policy.Kind == reasoning.KindAuto {
		if selection.ReasoningDefault != "" {
			return selection.ReasoningDefault, nil
		}
		return agent.ReasoningAuto, nil
	}
	if policy.Kind == reasoning.KindTokens || policy.Value == reasoning.ReasoningMax {
		maxTokens := lookupReasoningMaxTokens(cfg, selection.ResolvedModel)
		if maxTokens > 0 {
			if policy.Value == reasoning.ReasoningMax {
				return agent.ReasoningTokens(maxTokens), nil
			}
			if policy.Tokens > maxTokens {
				return "", fmt.Errorf("reasoning: token budget %d exceeds maximum %d for model %q", policy.Tokens, maxTokens, selection.ResolvedModel)
			}
		}
	}
	return agent.Reasoning(policy.Value), nil
}

func lookupReasoningMaxTokens(cfg *agentConfig.Config, model string) int {
	if model == "" {
		return 0
	}
	catalog, err := cfg.LoadModelCatalog()
	if err != nil || catalog == nil {
		return 0
	}
	if entry, ok := catalog.LookupModel(model); ok {
		return entry.ReasoningMaxTokens
	}
	return 0
}

func resolveProviderForRun(cfg *agentConfig.Config, workDir, backendName, providerName string, overrides agentConfig.ProviderOverrides) (providerSelection, agentcore.Provider, agentConfig.ProviderConfig, error) {
	routeKey, routeModelRef, useLegacyBackend, err := resolveRouteTarget(cfg, backendName, providerName, overrides)
	if err != nil {
		return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
	}
	if routeKey != "" {
		selection, p, pc, err := buildRouteSelection(cfg, workDir, routeKey, routeModelRef, overrides.AllowDeprecated)
		if err != nil {
			return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
		}
		return selection, p, pc, nil
	}
	if useLegacyBackend != "" {
		counter, err := readAndIncrementRouteCounter(workDir, legacyRouteCounterName(useLegacyBackend))
		if err != nil {
			counter = 0
		}
		p, pc, resolved, err := cfg.ResolveBackend(useLegacyBackend, counter, overrides)
		if err != nil {
			return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
		}
		selection := providerSelection{
			Route:             useLegacyBackend,
			ResolvedModel:     pc.Model,
			RequestedModelRef: overrides.ModelRef,
		}
		if bc, ok := cfg.GetBackend(useLegacyBackend); ok && len(bc.Providers) > 0 {
			idx := selectBackendProviderIndex(bc.Strategy, counter, len(bc.Providers))
			selection.Provider = bc.Providers[idx]
		}
		if resolved != nil {
			selection.ResolvedModelRef = resolved.CanonicalID
			selection.ReasoningDefault = agent.Reasoning(resolved.SurfacePolicy.ReasoningDefault)
			if resolved.ConcreteModel != "" {
				selection.ResolvedModel = resolved.ConcreteModel
			}
		}
		return selection, p, pc, nil
	}

	// Direct provider selection or exact model pin.
	if providerName == "" {
		providerName = cfg.DefaultName()
	}
	p, pc, resolved, err := cfg.BuildProviderWithOverrides(providerName, overrides)
	if err != nil {
		return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
	}
	selection := providerSelection{
		Route:             providerName,
		Provider:          providerName,
		RequestedModel:    overrides.Model,
		RequestedModelRef: overrides.ModelRef,
		ResolvedModel:     pc.Model,
	}
	if resolved != nil {
		selection.ResolvedModelRef = resolved.CanonicalID
		selection.ReasoningDefault = agent.Reasoning(resolved.SurfacePolicy.ReasoningDefault)
		if resolved.ConcreteModel != "" {
			selection.ResolvedModel = resolved.ConcreteModel
		}
	}
	return selection, p, pc, nil
}

func resolveRouteTarget(cfg *agentConfig.Config, backendName, providerName string, overrides agentConfig.ProviderOverrides) (routeKey string, routeModelRef string, legacyBackend string, err error) {
	if providerName != "" {
		return "", "", "", nil
	}
	if backendName != "" {
		return "", "", backendName, nil
	}
	if overrides.Model != "" {
		return overrides.Model, "", "", nil
	}
	if overrides.ModelRef != "" {
		resolved, err := resolveCanonicalModelRef(cfg, overrides.ModelRef, overrides.AllowDeprecated)
		if err != nil {
			return "", "", "", err
		}
		if _, ok := cfg.GetModelRoute(resolved.CanonicalID); ok {
			return resolved.CanonicalID, overrides.ModelRef, "", nil
		}
		if _, ok := cfg.GetModelRoute(overrides.ModelRef); ok {
			return overrides.ModelRef, overrides.ModelRef, "", nil
		}
		return resolved.CanonicalID, overrides.ModelRef, "", nil
	}
	if cfg.Routing.DefaultModel != "" {
		return cfg.Routing.DefaultModel, "", "", nil
	}
	if cfg.Routing.DefaultModelRef != "" {
		resolved, err := resolveCanonicalModelRef(cfg, cfg.Routing.DefaultModelRef, true)
		if err != nil {
			return "", "", "", err
		}
		if _, ok := cfg.GetModelRoute(resolved.CanonicalID); ok {
			return resolved.CanonicalID, cfg.Routing.DefaultModelRef, "", nil
		}
		if _, ok := cfg.GetModelRoute(cfg.Routing.DefaultModelRef); ok {
			return cfg.Routing.DefaultModelRef, cfg.Routing.DefaultModelRef, "", nil
		}
		return resolved.CanonicalID, cfg.Routing.DefaultModelRef, "", nil
	}
	if cfg.DefaultBackend != "" {
		return "", "", cfg.DefaultBackend, nil
	}
	return "", "", "", nil
}

func buildRouteSelection(cfg *agentConfig.Config, workDir, routeKey, routeModelRef string, allowDeprecated bool) (providerSelection, agentcore.Provider, agentConfig.ProviderConfig, error) {
	var explicitRoute *agentConfig.ModelRouteConfig
	if route, ok := cfg.GetModelRoute(routeKey); ok {
		explicitRoute = &route
	}
	plan, err := buildSmartRoutePlan(cfg, workDir, routeKey, routeModelRef, allowDeprecated, explicitRoute, nil)
	if err != nil {
		return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
	}
	if len(plan.Order) == 0 {
		return providerSelection{}, nil, agentConfig.ProviderConfig{}, fmt.Errorf("config: no route candidates available for %q", routeKey)
	}
	selected := plan.Candidates[plan.Order[0]]
	if routeModelRef != "" && (selected.Model == "" || selected.Model == routeKey) {
		resolvedPC, _, err := cfg.ResolveProviderConfig(selected.Provider, agentConfig.ProviderOverrides{ModelRef: routeModelRef, AllowDeprecated: allowDeprecated})
		if err != nil {
			return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
		}
		selected.Model = resolvedPC.Model
	}
	p, pc, resolved, err := cfg.BuildProviderWithOverrides(selected.Provider, agentConfig.ProviderOverrides{
		Model:           selected.Model,
		ModelRef:        routeModelRef,
		AllowDeprecated: allowDeprecated,
	})
	if err != nil {
		return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
	}
	pc.Model = selected.Model
	routeCandidates := make([]agentConfig.ModelRouteCandidateConfig, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		if routeModelRef != "" && (candidate.Model == "" || candidate.Model == routeKey) {
			resolvedPC, _, err := cfg.ResolveProviderConfig(candidate.Provider, agentConfig.ProviderOverrides{ModelRef: routeModelRef, AllowDeprecated: allowDeprecated})
			if err != nil {
				return providerSelection{}, nil, agentConfig.ProviderConfig{}, err
			}
			candidate.Model = resolvedPC.Model
		}
		routeCandidates = append(routeCandidates, agentConfig.ModelRouteCandidateConfig{
			Provider: candidate.Provider,
			Model:    candidate.Model,
			Priority: candidate.Priority,
		})
	}
	p = newRouteProvider(cfg, workDir, routeKey, routeKey, routeModelRef, agentConfig.ModelRouteConfig{
		Strategy:   plan.Strategy,
		Candidates: routeCandidates,
	}, plan.Order, selected.Provider, allowDeprecated)

	selection := providerSelection{
		Route:             routeKey,
		Provider:          selected.Provider,
		RequestedModel:    routeKey,
		RequestedModelRef: routeModelRef,
		ResolvedModel:     pc.Model,
		NoStream:          true,
	}
	if resolved != nil {
		selection.ResolvedModelRef = resolved.CanonicalID
		selection.ReasoningDefault = agent.Reasoning(resolved.SurfacePolicy.ReasoningDefault)
		if resolved.ConcreteModel != "" {
			selection.ResolvedModel = resolved.ConcreteModel
		}
	} else if routeModelRef != "" {
		selection.ResolvedModelRef = routeKey
	}
	if selection.ReasoningDefault == "" && routeModelRef != "" {
		if catalogResolved, err := resolveCanonicalModelRef(cfg, routeModelRef, allowDeprecated); err == nil {
			selection.ReasoningDefault = agent.Reasoning(catalogResolved.SurfacePolicy.ReasoningDefault)
			if selection.ResolvedModelRef == "" || selection.ResolvedModelRef == routeKey {
				selection.ResolvedModelRef = catalogResolved.CanonicalID
			}
		}
	}
	return selection, p, pc, nil
}

func resolveCanonicalModelRef(cfg *agentConfig.Config, ref string, allowDeprecated bool) (*modelcatalog.ResolvedTarget, error) {
	catalog, err := cfg.LoadModelCatalog()
	if err != nil {
		return nil, err
	}
	surfaces := []modelcatalog.Surface{
		modelcatalog.SurfaceAgentOpenAI,
		modelcatalog.SurfaceAgentAnthropic,
	}
	var lastErr error
	for _, surface := range surfaces {
		resolved, err := catalog.Resolve(ref, modelcatalog.ResolveOptions{
			Surface:         surface,
			AllowDeprecated: allowDeprecated,
		})
		if err == nil {
			return &resolved, nil
		}
		var missingSurfaceErr *modelcatalog.MissingSurfaceError
		if errors.As(err, &missingSurfaceErr) {
			lastErr = err
			continue
		}
		return nil, err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("config: cannot resolve model reference %q", ref)
}

func selectBackendProviderIndex(strategy string, counter, n int) int {
	if n <= 0 {
		return 0
	}
	if strategy == "round-robin" {
		return counter % n
	}
	return 0
}

func resolvePreset(flagValue string, cfg *agentConfig.Config) (string, error) {
	preset := flagValue
	if preset == "" {
		preset = cfg.Preset
	}
	return prompt.ResolvePresetName(preset)
}

func buildToolsForPreset(workDir, preset string, bashFilter ...tool.BashOutputFilterConfig) []agentcore.Tool {
	filter := tool.BashOutputFilterConfig{}
	if len(bashFilter) > 0 {
		filter = bashFilter[0]
	}
	tools := []agentcore.Tool{
		&tool.ReadTool{WorkDir: workDir},
		&tool.WriteTool{WorkDir: workDir},
		&tool.EditTool{WorkDir: workDir},
		&tool.BashTool{WorkDir: workDir, OutputFilter: filter},
		&tool.FindTool{WorkDir: workDir},
		&tool.GrepTool{WorkDir: workDir},
		&tool.LsTool{WorkDir: workDir},
		&tool.PatchTool{WorkDir: workDir},
	}
	if preset != "benchmark" {
		taskStore := tool.NewTaskStore()
		tools = append(tools, &tool.TaskTool{Store: taskStore})
	}
	return tools
}

func bashOutputFilterConfig(cfg agentConfig.BashOutputFilterConfig) tool.BashOutputFilterConfig {
	return tool.BashOutputFilterConfig{
		Mode:         cfg.Mode,
		RTKBinary:    cfg.RTKBinary,
		MaxBytes:     cfg.MaxBytes,
		RawOutputDir: cfg.RawOutputDir,
	}
}

func buildProviderFromResolvedConfig(name string, pc agentConfig.ProviderConfig) (agentcore.Provider, error) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{name: pc},
	}
	return cfg.BuildProvider(name)
}

func legacyRouteCounterName(backendName string) string {
	return "backend-" + backendName
}

// routeStateFile returns the path to the per-route rotation counter file.
func routeStateFile(workDir, routeName string) string {
	return filepath.Join(workDir, ".agent", "route-state-"+routeStateKey(routeName)+".counter")
}

// routeCounterMu serializes counter reads and writes within a process.
var routeCounterMu sync.Mutex

// readAndIncrementRouteCounter reads the current rotation counter for a route,
// increments it, writes it back, and returns the value that was
// read (i.e. the counter to use for this request).
func readAndIncrementRouteCounter(workDir, routeName string) (int, error) {
	routeCounterMu.Lock()
	defer routeCounterMu.Unlock()

	path := routeStateFile(workDir, routeName)

	// Read existing counter; treat missing file as counter 0.
	var counter int
	if data, err := safefs.ReadFile(path); err == nil {
		if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &counter); err != nil {
			counter = 0
		}
	}

	// Write incremented counter.
	next := counter + 1
	if err := safefs.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return counter, err
	}
	if err := safefs.WriteFile(path, []byte(fmt.Sprintf("%d\n", next)), 0o600); err != nil {
		return counter, err
	}

	return counter, nil
}

type ddxPromptEnvelope struct {
	Kind           string          `json:"kind"`
	ID             string          `json:"id"`
	Title          string          `json:"title,omitempty"`
	Prompt         string          `json:"prompt"`
	Inputs         json.RawMessage `json:"inputs,omitempty"`
	ResponseSchema json.RawMessage `json:"response_schema,omitempty"`
	Callback       json.RawMessage `json:"callback,omitempty"`
}

func resolvePrompt(p string) (string, map[string]string, error) {
	var raw string
	if p != "" {
		if strings.HasPrefix(p, "@") {
			data, err := safefs.ReadFile(p[1:])
			if err != nil {
				return "", nil, fmt.Errorf("reading prompt file: %w", err)
			}
			raw = string(data)
		} else {
			raw = p
		}
	} else {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return "", nil, fmt.Errorf("reading stdin: %w", err)
			}
			raw = strings.TrimSpace(string(data))
		}
	}

	return parsePromptInput(raw)
}

func parsePromptInput(raw string) (string, map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil, nil
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return raw, nil, nil
	}
	kindRaw, kindOK := probe["kind"]
	if !kindOK {
		return raw, nil, nil
	}
	if !isPromptEnvelopeProbe(probe) {
		var kindValue any
		if err := json.Unmarshal(kindRaw, &kindValue); err == nil {
			if _, ok := kindValue.(string); !ok && hasPromptEnvelopeFields(probe) {
				return "", nil, fmt.Errorf("invalid prompt envelope: kind must be a string")
			}
		} else if hasPromptEnvelopeFields(probe) {
			return "", nil, fmt.Errorf("invalid prompt envelope: kind must be a string")
		}
		return raw, nil, nil
	}

	var env ddxPromptEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return "", nil, fmt.Errorf("parsing prompt envelope: %w", err)
	}
	if env.Kind == "" || env.ID == "" || env.Prompt == "" {
		return "", nil, fmt.Errorf("invalid prompt envelope: kind, id, and prompt are required")
	}

	metadata := map[string]string{
		"prompt.kind": env.Kind,
		"prompt.id":   env.ID,
	}
	if env.Title != "" {
		metadata["prompt.title"] = env.Title
	}
	if len(env.Inputs) > 0 {
		inputs, err := canonicalPromptJSON(env.Inputs)
		if err != nil {
			return "", nil, fmt.Errorf("normalizing prompt envelope inputs: %w", err)
		}
		metadata["prompt.inputs"] = inputs
	}
	if len(env.ResponseSchema) > 0 {
		schema, err := canonicalPromptJSON(env.ResponseSchema)
		if err != nil {
			return "", nil, fmt.Errorf("normalizing prompt envelope response schema: %w", err)
		}
		metadata["prompt.response_schema"] = schema
	}
	if len(env.Callback) > 0 {
		callback, err := canonicalPromptJSON(env.Callback)
		if err != nil {
			return "", nil, fmt.Errorf("normalizing prompt envelope callback: %w", err)
		}
		metadata["prompt.callback"] = callback
	}

	return env.Prompt, metadata, nil
}

func isPromptEnvelopeProbe(probe map[string]json.RawMessage) bool {
	kindRaw, kindOK := probe["kind"]
	if !kindOK {
		return false
	}

	var kind string
	if err := json.Unmarshal(kindRaw, &kind); err != nil {
		return false
	}
	if kind != "prompt" {
		return false
	}

	_, idOK := probe["id"]
	_, titleOK := probe["title"]
	_, promptOK := probe["prompt"]
	_, inputsOK := probe["inputs"]
	_, responseSchemaOK := probe["response_schema"]
	_, callbackOK := probe["callback"]

	return titleOK || promptOK || idOK || inputsOK || responseSchemaOK || callbackOK
}

func hasPromptEnvelopeFields(probe map[string]json.RawMessage) bool {
	_, titleOK := probe["title"]
	_, promptOK := probe["prompt"]
	_, idOK := probe["id"]
	_, inputsOK := probe["inputs"]
	_, responseSchemaOK := probe["response_schema"]
	_, callbackOK := probe["callback"]

	return titleOK || promptOK || idOK || inputsOK || responseSchemaOK || callbackOK
}

func canonicalPromptJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

func cmdProviders(workDir string, jsonOut bool) int {
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	if jsonOut {
		data, _ := json.MarshalIndent(redactedProviders(cfg.Providers), "", "  ")
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

type providerListEntry struct {
	Type    string            `json:"type"`
	BaseURL string            `json:"base_url"`
	Model   string            `json:"model"`
	Headers map[string]string `json:"headers,omitempty"`
}

func redactedProviders(providers map[string]agentConfig.ProviderConfig) map[string]providerListEntry {
	redacted := make(map[string]providerListEntry, len(providers))
	for name, pc := range providers {
		entry := providerListEntry{
			Type:    pc.Type,
			BaseURL: pc.BaseURL,
			Model:   pc.Model,
		}
		if len(pc.Headers) > 0 {
			headers := make(map[string]string, len(pc.Headers))
			for key := range pc.Headers {
				headers[key] = "[redacted]"
			}
			entry.Headers = headers
		}
		redacted[name] = entry
	}
	return redacted
}

func cmdModels(workDir, providerName string, args []string) int {
	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	// Load catalog once for all annotations.
	cat, _ := modelcatalog.Default()

	showAll := len(args) > 0 && args[0] == "--all"

	if showAll {
		for _, name := range cfg.ProviderNames() {
			pc := cfg.Providers[name]
			fmt.Printf("[%s]\n", name)
			printProviderModels(pc, cat)
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
		if pc.Model != "" {
			fmt.Printf("Configured model: %s\n", pc.Model)
		}
		return 0
	}

	printProviderModels(pc, cat)
	return 0
}

// printProviderModels probes a provider's /v1/models endpoint and prints the
// full ranked list. The auto-selected model is marked with "*". Catalog-
// recognized models show their catalog target ID in brackets.
func printProviderModels(pc agentConfig.ProviderConfig, cat *modelcatalog.Catalog) {
	if pc.Type == "anthropic" {
		fmt.Println("  (anthropic — no model listing endpoint)")
		return
	}

	// Build known-models map for ranking.
	var knownModels map[string]string
	if cat != nil {
		knownModels = cat.AllConcreteModels(modelcatalog.SurfaceAgentOpenAI)
	}

	ids := probeProviderModels(pc, routingProbeTimeout(nil)).models
	if len(ids) == 0 {
		fmt.Println("  (unavailable)")
		return
	}

	ranked, err := oaiProvider.RankModels(ids, knownModels, pc.ModelPattern)
	if err != nil {
		// Pattern compile error — fall back to plain list.
		for _, id := range ids {
			fmt.Printf("  %s\n", id)
		}
		return
	}

	// Determine the auto-selected model (first in ranked list when no static model).
	autoSelected := ""
	if pc.Model == "" && len(ranked) > 0 {
		autoSelected = ranked[0].ID
	}

	for _, sm := range ranked {
		marker := "  "
		if sm.ID == pc.Model {
			marker = "* "
		} else if sm.ID == autoSelected {
			marker = "> " // would be auto-selected
		}
		annotation := ""
		if sm.CatalogRef != "" {
			annotation = "  [catalog: " + sm.CatalogRef + "]"
		} else if sm.PatternMatch {
			annotation = "  [pattern]"
		}
		fmt.Printf("%s%s%s\n", marker, sm.ID, annotation)
	}
	if pc.Model == "" {
		fmt.Println()
		fmt.Println("  * = configured  > = would auto-select")
	}
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
	logDir := sessionLogDir(workDir, cfg)

	if len(args) > 0 {
		path := filepath.Join(logDir, args[0]+".jsonl")
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

	entries, err := os.ReadDir(logDir)
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
	path := filepath.Join(sessionLogDir(workDir, cfg), args[0]+".jsonl")
	if err := session.Replay(path, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	return 0
}

func cmdUsage(workDir string, args []string) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	since := fs.String("since", "", "Time window: today, 7d, 30d, YYYY-MM-DD, or YYYY-MM-DD..YYYY-MM-DD")
	jsonOutput := fs.Bool("json", false, "Output JSON")
	csvOutput := fs.Bool("csv", false, "Output CSV")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *jsonOutput && *csvOutput {
		fmt.Fprintln(os.Stderr, "error: choose only one of --json or --csv")
		return 2
	}

	now := time.Now().UTC()
	if _, err := session.ParseUsageWindow(*since, now); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}

	cfg, err := agentConfig.Load(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 2
	}

	report, err := session.AggregateUsage(sessionLogDir(workDir, cfg), session.UsageOptions{
		Since: *since,
		Now:   now,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	if *csvOutput {
		printUsageCSV(report)
		return 0
	}

	printUsageReport(report, *since)
	return 0
}

func sessionLogDir(workDir string, cfg *agentConfig.Config) string {
	if cfg == nil || cfg.SessionLogDir == "" {
		return filepath.Join(workDir, ".agent", "sessions")
	}
	if filepath.IsAbs(cfg.SessionLogDir) {
		return cfg.SessionLogDir
	}
	return filepath.Join(workDir, cfg.SessionLogDir)
}

func printUsageReport(report *session.UsageReport, since string) {
	if report.Window != nil {
		fmt.Printf("Window: %s .. %s\n", formatUsageWindowBound(report.Window.Start), formatUsageWindowBound(report.Window.End))
	} else if since != "" {
		fmt.Printf("Window: %s\n", since)
	}

	fmt.Printf("%-16s %-24s %8s %10s %10s %10s %14s %10s %10s %8s %10s %12s %12s\n",
		"PROVIDER", "MODEL", "SESSIONS", "INPUT", "OUTPUT", "TOTAL", "COST", "UNKNOWN", "CACHE-HIT%", "SUCCESS%", "COST/OK", "IN tok/s", "OUT tok/s")
	for _, row := range report.Rows {
		printUsageRow(row)
	}
	total := report.Totals
	total.Provider = "TOTAL"
	printUsageRow(total)
}

func formatUsageWindowBound(ts time.Time) string {
	if ts.IsZero() {
		return "(open)"
	}
	return ts.UTC().Format("2006-01-02")
}

func printUsageRow(row session.UsageRow) {
	cost := "unknown"
	if row.UnknownCostSessions == 0 && row.KnownCostUSD != nil {
		cost = fmt.Sprintf("$%.4f", *row.KnownCostUSD)
	}
	cacheHit := "-"
	if row.CacheReadTokens > 0 || row.InputTokens > 0 {
		cacheHit = fmt.Sprintf("%.1f%%", row.CacheHitRate()*100)
	}
	successRate := "-"
	if row.Sessions > 0 {
		successRate = fmt.Sprintf("%.1f%%", row.SuccessRate()*100)
	}
	costPerOK := "-"
	if cps := row.CostPerSuccess(); cps != nil {
		costPerOK = fmt.Sprintf("$%.4f", *cps)
	}
	fmt.Printf("%-16s %-24s %8d %10d %10d %10d %14s %10d %10s %8s %10s %12.1f %12.1f\n",
		row.Provider,
		row.Model,
		row.Sessions,
		row.InputTokens,
		row.OutputTokens,
		row.TotalTokens,
		cost,
		row.UnknownCostSessions,
		cacheHit,
		successRate,
		costPerOK,
		row.InputTokensPerSecond(),
		row.OutputTokensPerSecond(),
	)
}

func printUsageCSV(report *session.UsageReport) {
	fmt.Println("provider,model,sessions,success_sessions,failed_sessions,input_tokens,output_tokens,total_tokens,duration_ms,known_cost_usd,unknown_cost_sessions,success_rate,cost_per_success,input_tokens_per_second,output_tokens_per_second,cache_read_tokens,cache_write_tokens")
	for _, row := range report.Rows {
		printUsageCSVRow(row)
	}
	total := report.Totals
	total.Provider = "TOTAL"
	printUsageCSVRow(total)
}

func printUsageCSVRow(row session.UsageRow) {
	cost := ""
	if row.UnknownCostSessions == 0 && row.KnownCostUSD != nil {
		cost = fmt.Sprintf("%.4f", *row.KnownCostUSD)
	}
	successRate := fmt.Sprintf("%.4f", row.SuccessRate())
	costPerOK := ""
	if cps := row.CostPerSuccess(); cps != nil {
		costPerOK = fmt.Sprintf("%.4f", *cps)
	}
	fmt.Printf("%s,%s,%d,%d,%d,%d,%d,%d,%d,%s,%d,%s,%s,%.1f,%.1f,%d,%d\n",
		csvEscape(row.Provider),
		csvEscape(row.Model),
		row.Sessions,
		row.SuccessSessions,
		row.FailedSessions,
		row.InputTokens,
		row.OutputTokens,
		row.TotalTokens,
		row.DurationMs,
		cost,
		row.UnknownCostSessions,
		successRate,
		costPerOK,
		row.InputTokensPerSecond(),
		row.OutputTokensPerSecond(),
		row.CacheReadTokens,
		row.CacheWriteTokens,
	)
}

func csvEscape(value string) string {
	if strings.ContainsAny(value, "\",\n\r") {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}

func checkProviderStatus(pc agentConfig.ProviderConfig) string {
	probe := probeProviderModels(pc, routingProbeTimeout(nil))
	if pc.Type == "anthropic" {
		if probe.err != nil {
			return probe.err.Error()
		}
		return "api key configured"
	}
	if probe.err != nil {
		return probe.err.Error()
	}
	return fmt.Sprintf("http-connected (%d models listed)", len(probe.models))
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
		if _, err := fmt.Scanln(&response); err != nil {
			response = ""
		}
		if strings.ToLower(response) != "y" {
			return 0
		}
		configPath = filepath.Join(workDir, ".agent", "config.yaml")
		if err := safefs.MkdirAll(filepath.Join(workDir, ".agent"), 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot create .agent directory: %v\n", err)
			return 1
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
			return 1
		}
		configPath = filepath.Join(home, ".config", "agent", "config.yaml")
		if err := safefs.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot create config directory: %v\n", err)
			return 1
		}
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
	return safefs.WriteFile(path, data, 0o600)
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
	_ = safefs.WriteFile(checkFile, []byte(time.Now().Format(time.RFC3339)), 0o600)
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
