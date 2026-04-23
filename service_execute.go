package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	geminiharness "github.com/DocumentDrivenDX/agent/internal/harnesses/gemini"
	opencodeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/opencode"
	piharness "github.com/DocumentDrivenDX/agent/internal/harnesses/pi"
	virtualprovider "github.com/DocumentDrivenDX/agent/internal/provider/virtual"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
	"github.com/DocumentDrivenDX/agent/internal/routing"
	"github.com/DocumentDrivenDX/agent/internal/sessionlog"
	"github.com/DocumentDrivenDX/agent/internal/tool"
)

// generateSessionID returns a unique session identifier for a new Execute.
func generateSessionID() string {
	return fmt.Sprintf("svc-%d", time.Now().UnixNano())
}

// defaultStallReadOnlyIterations is the conservative default applied when
// ServiceExecuteRequest.StallPolicy is nil. Mirrors today's circuit-breaker
// thresholds: a model that goes 25 turns without a write or final response
// is considered stuck.
const defaultStallReadOnlyIterations = 25

// defaultStallNoopCompactions is the default no-op compaction ceiling.
// Each pre-/post-turn checkpoint where the compactor declines to run counts.
// At >50 it is overwhelmingly likely the model is no longer producing
// useful state changes — fail fast.
const defaultStallNoopCompactions = 50

// readOnlyTools enumerates tool names that do not mutate filesystem or
// remote state. Used by the StallPolicy enforcement to detect "loops of
// reads with no writes". The list is conservative — when in doubt a tool
// is treated as side-effecting.
var readOnlyTools = map[string]bool{
	"read":       true,
	"read_file":  true,
	"grep":       true,
	"ls":         true,
	"find":       true,
	"cat":        true,
	"head":       true,
	"tail":       true,
	"stat":       true,
	"web_fetch":  true,
	"web_search": true,
}

// Execute runs an agent task in-process; emits Events on the returned
// channel until the task terminates (channel closes). The final event
// (type=final) carries status, usage, cost, session-log path, and the
// resolved fallback chain that fired.
//
// See CONTRACT-003 §"Behaviors the contract guarantees" for the full
// behavior contract this method honors:
//   - Orphan-model validation (Status=failed when Model unknown)
//   - Provider-deadline wrapping (Timeout + IdleTimeout + ProviderTimeout)
//   - StallPolicy enforcement (stall event before final)
//   - Route-reason attribution (routing_decision start, routing_actual final)
//   - OS-level subprocess cleanup on ctx.Done()
//   - Metadata bidirectional echo (events + session log)
//   - SessionLogDir per-request override
//
// Routing: under-specified requests (no PreResolved, no Harness) are
// dispatched through internal/routing.Resolve via ResolveRoute. Callers
// can run with bare Profile/ModelRef/Model/Provider — the engine picks.
// NativeProvider must still be supplied for the native path until
// provider construction lands in a follow-up.
func (s *service) Execute(ctx context.Context, req ServiceExecuteRequest) (<-chan ServiceEvent, error) {
	// Generate a session ID and register it in the hub so TailSessionLog
	// callers can subscribe before or during execution.
	sessionID := generateSessionID()
	s.hub.openSession(sessionID)

	outer := make(chan ServiceEvent, 64)

	// Resolve the route.
	decision, err := s.resolveExecuteRoute(req)
	if err != nil {
		if isExplicitPinError(err) {
			s.hub.closeSession(sessionID, ServiceEvent{})
			return nil, err
		}
		// Still return a channel that yields a single failed final event so
		// downstream consumers don't have to special-case the error path.
		// Also close the hub session so TailSessionLog subscribers unblock.
		go func() {
			emitFatalFinal(outer, req.Metadata, "failed", err.Error())
			// Drain outer to get the final event and forward to hub.
			// emitFatalFinal closes outer; read the single event from it.
		}()
		// We can't easily intercept emitFatalFinal here, so close the hub
		// session with an empty final immediately — callers on TailSessionLog
		// for a failed-route session get an empty close.
		go func() {
			// Wait briefly for emitFatalFinal to write.
			time.Sleep(10 * time.Millisecond)
			s.hub.closeSession(sessionID, ServiceEvent{})
		}()
		return outer, nil
	}

	// Metadata seam: every event we emit echoes req.Metadata.
	meta := req.Metadata

	// Wrap the inner channel through the hub so every event is broadcast to
	// TailSessionLog subscribers. The fan-out goroutine owns outer's close.
	inner := s.hub.wrapExecuteWithHub(sessionID, outer)

	// Emit start-of-execution routing_decision so consumers know the picked
	// chain before any real work fires. The actual chain (post-fallback) is
	// stamped onto the final event's RoutingActual field.
	go s.runExecute(ctx, req, *decision, meta, inner, sessionID)
	return outer, nil
}

// resolveExecuteRoute reduces the request to a concrete RouteDecision.
// PreResolved wins outright; otherwise the request is dispatched through
// the routing engine (internal/routing.Resolve) when under-specified, or
// accepted verbatim when Harness is set explicitly.
func (s *service) resolveExecuteRoute(req ServiceExecuteRequest) (*RouteDecision, error) {
	if req.PreResolved != nil {
		return req.PreResolved, nil
	}
	// If Harness is omitted but the engine has enough hints (Profile/ModelRef/
	// Model/Provider) to disambiguate, route through ResolveRoute.
	if req.Harness == "" {
		if req.Profile == "" && req.ModelRef == "" && req.Model == "" && req.Provider == "" {
			return nil, fmt.Errorf("routing under-specified: pass PreResolved or set at least one of Harness/Profile/ModelRef/Model/Provider")
		}
		return s.resolveExecuteRouteWithEngine(req)
	}
	canonical := harnesses.ResolveHarnessAlias(req.Harness)
	if !s.registry.Has(canonical) {
		return nil, fmt.Errorf("unknown harness %q", req.Harness)
	}
	cfg, _ := s.registry.Get(canonical)
	if err := validateExplicitHarnessProfile(canonical, cfg, req.Profile); err != nil {
		return nil, err
	}
	if err := validateExplicitHarnessModel(canonical, cfg, req.Model, req.Provider); err != nil {
		return nil, err
	}
	if err := validateExplicitHarnessReasoning(canonical, cfg, req.Reasoning); err != nil {
		return nil, err
	}
	return &RouteDecision{
		Harness:  canonical,
		Provider: req.Provider,
		Model:    resolveSubprocessModelAlias(canonical, req.Model),
		Reason:   "explicit",
	}, nil
}

func validateExplicitHarnessProfile(name string, cfg harnesses.HarnessConfig, profile string) error {
	constraint, ok := explicitProfileConstraint(profile)
	if !ok {
		return nil
	}
	switch constraint {
	case routing.ProviderPreferenceLocalOnly:
		if !cfg.IsLocal {
			return &ErrProfilePinConflict{
				Profile:           profile,
				ConflictingPin:    "Harness=" + name,
				ProfileConstraint: constraint,
			}
		}
	case routing.ProviderPreferenceSubscriptionOnly:
		if !cfg.IsSubscription {
			return &ErrProfilePinConflict{
				Profile:           profile,
				ConflictingPin:    "Harness=" + name,
				ProfileConstraint: constraint,
			}
		}
	}
	return nil
}

func explicitProfileConstraint(profile string) (string, bool) {
	switch profile {
	case "local", "offline", "air-gapped":
		return routing.ProviderPreferenceLocalOnly, true
	case "smart", "code-smart", "code-high":
		return routing.ProviderPreferenceSubscriptionOnly, true
	default:
		return "", false
	}
}

func isExplicitPinError(err error) bool {
	var modelErr *ErrHarnessModelIncompatible
	if errors.As(err, &modelErr) {
		return true
	}
	var profileErr *ErrProfilePinConflict
	return errors.As(err, &profileErr)
}

func validateExplicitHarnessModel(name string, cfg harnesses.HarnessConfig, model, provider string) error {
	if model == "" || cfg.TestOnly || cfg.IsHTTPProvider || name == "agent" {
		return nil
	}
	if modelSupportedForHarness(name, cfg, model, provider) {
		return nil
	}
	supportedModels := subprocessHarnessModelIDs(name, cfg)
	return &ErrHarnessModelIncompatible{
		Harness:         name,
		Model:           model,
		SupportedModels: append([]string(nil), supportedModels...),
	}
}

func modelSupportedForHarness(name string, cfg harnesses.HarnessConfig, model, provider string) bool {
	for _, known := range subprocessHarnessModelIDs(name, cfg) {
		if model == known {
			return true
		}
	}
	switch name {
	case "codex":
		return strings.HasPrefix(model, "gpt-")
	case "claude":
		return strings.HasPrefix(model, "claude-")
	case "pi":
		// Pi can route to non-Gemini backends (lmstudio, omlx, etc.) when a
		// provider is pinned. The pi CLI owns per-provider model validation
		// in that case, so the agent-side gate trusts the provider pin and
		// defers concrete model-ID checks to pi --list-models / pi itself.
		return provider != ""
	default:
		return len(cfg.Models) == 0
	}
}

func validateExplicitHarnessReasoning(name string, cfg harnesses.HarnessConfig, value Reasoning) error {
	if cfg.TestOnly {
		return nil
	}
	if len(cfg.ReasoningLevels) == 0 && cfg.MaxReasoningTokens <= 0 {
		return nil
	}
	policy, err := reasoning.ParseString(string(value))
	if err != nil {
		return fmt.Errorf("unsupported reasoning %q for harness %q: %w", value, name, err)
	}
	switch policy.Kind {
	case reasoning.KindUnset, reasoning.KindAuto, reasoning.KindOff:
		return nil
	case reasoning.KindTokens:
		if cfg.MaxReasoningTokens <= 0 {
			return fmt.Errorf("unsupported reasoning %q for harness %q; token budgets are not supported", value, name)
		}
		if policy.Tokens > cfg.MaxReasoningTokens {
			return fmt.Errorf("unsupported reasoning %q for harness %q; max token budget is %d", value, name, cfg.MaxReasoningTokens)
		}
		return nil
	case reasoning.KindNamed:
		for _, supported := range cfg.ReasoningLevels {
			if string(policy.Value) == supported {
				return nil
			}
		}
		return fmt.Errorf("unsupported reasoning %q for harness %q; supported reasoning: %s", value, name, strings.Join(cfg.ReasoningLevels, ", "))
	default:
		return fmt.Errorf("unsupported reasoning %q for harness %q", value, name)
	}
}

// runExecute is the per-Execute goroutine. It owns the channel close path
// and the final event emit. All termination paths funnel through emitFinal
// so the channel always sees a final event before close.
func (s *service) runExecute(ctx context.Context, req ServiceExecuteRequest, decision RouteDecision, meta map[string]string, out chan<- ServiceEvent, sessionID string) {
	defer close(out)

	start := time.Now()
	var seq atomic.Int64

	// Wall-clock cap.
	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	// Emit routing_decision start event. Include session_id so callers can
	// extract it and pass to TailSessionLog.
	emitJSON(out, &seq, harnesses.EventTypeRoutingDecision, meta, map[string]any{
		"harness":    decision.Harness,
		"provider":   decision.Provider,
		"endpoint":   decision.Endpoint,
		"model":      decision.Model,
		"reason":     decision.Reason,
		"session_id": sessionID,
	})

	// Branch: native ("agent") path runs the in-process loop; subprocess
	// paths instantiate the matching internal/harnesses/<name>.Runner.
	switch decision.Harness {
	case "agent", "":
		s.runNative(runCtx, req, decision, meta, out, &seq, start)
	case "claude":
		s.runSubprocess(runCtx, req, decision, meta, out, &seq, start, &claudeharness.Runner{})
	case "codex":
		s.runSubprocess(runCtx, req, decision, meta, out, &seq, start, &codexharness.Runner{})
	case "gemini":
		s.runSubprocess(runCtx, req, decision, meta, out, &seq, start, &geminiharness.Runner{})
	case "opencode":
		s.runSubprocess(runCtx, req, decision, meta, out, &seq, start, &opencodeharness.Runner{})
	case "pi":
		s.runSubprocess(runCtx, req, decision, meta, out, &seq, start, &piharness.Runner{})
	case "virtual":
		s.runVirtual(runCtx, req, decision, meta, out, &seq, start)
	case "script":
		s.runScript(runCtx, req, decision, meta, out, &seq, start)
	default:
		if cfg, ok := s.registry.Get(decision.Harness); ok && cfg.IsHTTPProvider {
			s.runNative(runCtx, req, decision, meta, out, &seq, start)
			return
		}
		// Unknown / unimplemented subprocess harnesses fail with an explicit
		// final event so callers do not silently fall back.
		emitFinal(out, &seq, meta, harnesses.FinalData{
			Status:     "failed",
			Error:      fmt.Sprintf("harness %q dispatch not yet wired in service.Execute", decision.Harness),
			DurationMS: time.Since(start).Milliseconds(),
			RoutingActual: &harnesses.RoutingActual{
				Harness:  decision.Harness,
				Provider: decision.Provider,
				Model:    decision.Model,
			},
		})
	}
}

func (s *service) runVirtual(ctx context.Context, req ServiceExecuteRequest, decision RouteDecision, meta map[string]string, out chan<- ServiceEvent, seq *atomic.Int64, start time.Time) {
	inlineText := meta["virtual.response"]
	cfg := virtualprovider.Config{
		DictDir: meta["virtual.dict_dir"],
	}
	if inlineText != "" {
		cfg.InlineResponses = []virtualprovider.InlineResponse{{
			PromptMatch: metaValue(meta, "virtual.prompt_match", req.Prompt),
			Response: agentcore.Response{
				Content: inlineText,
				Usage: agentcore.TokenUsage{
					Input:  metadataInt(meta, "virtual.input_tokens"),
					Output: metadataInt(meta, "virtual.output_tokens"),
					Total:  metadataInt(meta, "virtual.total_tokens"),
				},
				Model: metaValue(meta, "virtual.model", decision.Model),
			},
			DelayMS: metadataInt(meta, "virtual.delay_ms"),
		}}
	}
	if cfg.DictDir == "" && len(cfg.InlineResponses) == 0 {
		emitFinal(out, seq, meta, harnesses.FinalData{
			Status:     "failed",
			Error:      "virtual harness requires metadata virtual.response or virtual.dict_dir",
			DurationMS: time.Since(start).Milliseconds(),
			RoutingActual: &harnesses.RoutingActual{
				Harness:  decision.Harness,
				Provider: decision.Provider,
				Model:    decision.Model,
			},
		})
		return
	}

	resp, err := virtualprovider.New(cfg).Chat(ctx, []agentcore.Message{{Role: agentcore.RoleUser, Content: req.Prompt}}, nil, agentcore.Options{})
	final := harnesses.FinalData{
		DurationMS: time.Since(start).Milliseconds(),
		RoutingActual: &harnesses.RoutingActual{
			Harness:  decision.Harness,
			Provider: decision.Provider,
			Model:    metaValue(meta, "virtual.model", decision.Model),
		},
	}
	if err != nil {
		final.Status = "failed"
		final.Error = err.Error()
		emitFinal(out, seq, meta, final)
		return
	}
	final.Status = "success"
	final.FinalText = resp.Content
	if resp.Usage.Input > 0 || resp.Usage.Output > 0 || resp.Usage.Total > 0 {
		final.Usage = &harnesses.FinalUsage{
			InputTokens:  harnesses.IntPtr(resp.Usage.Input),
			OutputTokens: harnesses.IntPtr(resp.Usage.Output),
			TotalTokens:  harnesses.IntPtr(resp.Usage.Total),
			Source:       harnesses.UsageSourceFallback,
		}
	}
	if resp.Content != "" {
		emitJSONRaw(out, seq, harnesses.EventTypeTextDelta, meta, harnesses.TextDeltaData{Text: resp.Content})
	}
	emitFinal(out, seq, meta, final)
}

func (s *service) runScript(ctx context.Context, req ServiceExecuteRequest, decision RouteDecision, meta map[string]string, out chan<- ServiceEvent, seq *atomic.Int64, start time.Time) {
	delay := metadataInt(meta, "script.delay_ms")
	if delay > 0 {
		timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			emitFinal(out, seq, meta, harnesses.FinalData{
				Status:     "cancelled",
				Error:      ctx.Err().Error(),
				DurationMS: time.Since(start).Milliseconds(),
				RoutingActual: &harnesses.RoutingActual{
					Harness:  decision.Harness,
					Provider: decision.Provider,
					Model:    decision.Model,
				},
			})
			return
		case <-timer.C:
		}
	}

	text, ok := meta["script.stdout"]
	if !ok {
		emitFinal(out, seq, meta, harnesses.FinalData{
			Status:     "failed",
			Error:      "script harness requires metadata script.stdout",
			DurationMS: time.Since(start).Milliseconds(),
			RoutingActual: &harnesses.RoutingActual{
				Harness:  decision.Harness,
				Provider: decision.Provider,
				Model:    decision.Model,
			},
		})
		return
	}
	exitCode := metadataInt(meta, "script.exit_code")
	final := harnesses.FinalData{
		Status:     "success",
		ExitCode:   exitCode,
		FinalText:  text,
		DurationMS: time.Since(start).Milliseconds(),
		RoutingActual: &harnesses.RoutingActual{
			Harness:  decision.Harness,
			Provider: decision.Provider,
			Model:    decision.Model,
		},
	}
	if text != "" {
		emitJSONRaw(out, seq, harnesses.EventTypeTextDelta, meta, harnesses.TextDeltaData{Text: text})
	}
	if exitCode != 0 {
		final.Status = "failed"
		final.Error = metaValue(meta, "script.stderr", fmt.Sprintf("script exited with status %d", exitCode))
	}
	emitFinal(out, seq, meta, final)
}

func metaValue(meta map[string]string, key, fallback string) string {
	if meta == nil {
		return fallback
	}
	if v := meta[key]; v != "" {
		return v
	}
	return fallback
}

func metadataInt(meta map[string]string, key string) int {
	if meta == nil {
		return 0
	}
	n, _ := strconv.Atoi(meta[key])
	return n
}

func nativeToolsForRequest(req ServiceExecuteRequest) []agentcore.Tool {
	if req.Tools != nil {
		return req.Tools
	}
	return tool.BuiltinToolsForPreset(req.WorkDir, req.ToolPreset, tool.BashOutputFilterConfig{})
}

func nativePermissionMode(permissions string) (string, error) {
	switch permissions {
	case "", "safe":
		return "safe", nil
	case "unrestricted":
		return "unrestricted", nil
	case "supervised":
		return "", fmt.Errorf("native agent permission mode %q is unsupported because no approval loop is available", permissions)
	default:
		return "", fmt.Errorf("native agent permission mode %q is unsupported", permissions)
	}
}

func filterNativeToolsForPermission(tools []agentcore.Tool, permission string) []agentcore.Tool {
	if permission == "unrestricted" {
		return tools
	}
	filtered := make([]agentcore.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if readOnlyTools[tool.Name()] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func toolNames(tools []agentcore.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		names = append(names, tool.Name())
	}
	return names
}

// runNative drives the in-process agent loop (loop.go's Run). The provider
// is wrapped with WrapProviderWithDeadlinesTimeouts so per-HTTP timeouts
// fire independently of the request wall-clock cap.
func (s *service) runNative(ctx context.Context, req ServiceExecuteRequest, decision RouteDecision, meta map[string]string, out chan<- ServiceEvent, seq *atomic.Int64, start time.Time) {
	resolvedProvider := s.resolveNativeProvider(nativeProviderRequest(req, decision))
	provider := resolvedProvider.Provider
	actualHarness := decision.Harness
	if actualHarness == "" {
		actualHarness = "agent"
	}
	actualProvider := resolvedProvider.Name
	if actualProvider == "" {
		actualProvider = decision.Provider
	}
	actualModel := decision.Model
	if actualModel == "" {
		actualModel = resolvedProvider.Entry.Model
	}
	if provider == nil {
		emitFinal(out, seq, meta, harnesses.FinalData{
			Status:     "failed",
			Error:      s.nativeProviderNotConfiguredError(req, decision),
			DurationMS: time.Since(start).Milliseconds(),
			RoutingActual: &harnesses.RoutingActual{
				Harness:  actualHarness,
				Provider: actualProvider,
				Model:    actualModel,
			},
		})
		return
	}
	permission, permissionErr := nativePermissionMode(req.Permissions)
	if permissionErr != nil {
		emitFinal(out, seq, meta, harnesses.FinalData{
			Status:     "failed",
			Error:      permissionErr.Error(),
			DurationMS: time.Since(start).Milliseconds(),
			RoutingActual: &harnesses.RoutingActual{
				Harness:  actualHarness,
				Provider: actualProvider,
				Model:    actualModel,
			},
		})
		return
	}

	// Provider-deadline wrapping: every HTTP call gets the per-request cap.
	// We mirror internal/execution.WrapProviderWithDeadlinesTimeouts in
	// package-local form to avoid the import cycle (internal/execution
	// imports the agent package; agent.service.Execute therefore wraps
	// the provider through the local helper below).
	if req.ProviderTimeout > 0 {
		provider = wrapProviderRequestTimeout(provider, req.ProviderTimeout)
	}

	// Stall policy: derive an implicit MaxIterations ceiling (~2× read-only
	// limit) so callers don't have to configure it directly.
	policy := req.StallPolicy
	if policy == nil {
		policy = &StallPolicy{
			MaxReadOnlyToolIterations: defaultStallReadOnlyIterations,
			MaxNoopCompactions:        defaultStallNoopCompactions,
		}
	}
	maxIter := policy.MaxReadOnlyToolIterations * 2
	if maxIter == 0 {
		maxIter = 100 // safety net when policy disables read-only tracking
	}

	// Stall tracking state, updated from the loop callback.
	var (
		readOnlyStreak atomic.Int64
		stalled        atomic.Bool
		stallReason    atomic.Value // string
		stallCount     atomic.Int64
	)
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Bridge agent.Event (loop callback) → harnesses.Event (out chan).
	cb := func(ev agentcore.Event) {
		// Translate to a harnesses.Event of best-fit type. We only forward
		// types that map onto CONTRACT-003's closed event union; internal
		// session.start / llm.* / compaction.* events do not have a public
		// equivalent and are dropped here. They still land in the session
		// log via the session logger that consumers attach themselves.
		switch ev.Type {
		case agentcore.EventToolCall:
			// Map tool.call → tool_call + tool_result. The loop emits a
			// single combined event with input + output; we split.
			var payload map[string]any
			_ = json.Unmarshal(ev.Data, &payload)
			toolName, _ := payload["tool"].(string)
			input, _ := payload["input"].(json.RawMessage)
			if input == nil {
				if rawIn, err := json.Marshal(payload["input"]); err == nil {
					input = rawIn
				}
			}
			callID := fmt.Sprintf("call-%d", ev.Seq)
			emitJSONRaw(out, seq, harnesses.EventTypeToolCall, meta, harnesses.ToolCallData{
				ID:    callID,
				Name:  toolName,
				Input: input,
			})
			outputStr, _ := payload["output"].(string)
			errStr, _ := payload["error"].(string)
			durMS, _ := payload["duration_ms"].(float64)
			emitJSONRaw(out, seq, harnesses.EventTypeToolResult, meta, harnesses.ToolResultData{
				ID:         callID,
				Output:     outputStr,
				Error:      errStr,
				DurationMS: int64(durMS),
			})
			// Stall accounting: read-only tool runs increment the streak;
			// any side-effecting tool resets it.
			if readOnlyTools[toolName] {
				if v := readOnlyStreak.Add(1); policy.MaxReadOnlyToolIterations > 0 && int(v) >= policy.MaxReadOnlyToolIterations {
					if stalled.CompareAndSwap(false, true) {
						stallReason.Store("read_only_tools_exceeded")
						stallCount.Store(v)
						cancel()
					}
				}
			} else {
				readOnlyStreak.Store(0)
			}
		case agentcore.EventCompactionEnd:
			// We only emit compaction events for *real* compaction work.
			// loop.go runCompaction already suppresses no-op start/end pairs;
			// the event we get here represents an actually-performed compaction.
			var payload map[string]any
			_ = json.Unmarshal(ev.Data, &payload)
			before, _ := payload["messages_before"].(float64)
			after, _ := payload["messages_after"].(float64)
			tokensBefore, _ := payload["tokens_before"].(float64)
			tokensAfter, _ := payload["tokens_after"].(float64)
			tokensFreed := int(tokensBefore - tokensAfter)
			emitJSONRaw(out, seq, harnesses.EventTypeCompaction, meta, map[string]any{
				"messages_before": int(before),
				"messages_after":  int(after),
				"tokens_freed":    tokensFreed,
			})
			// Compaction assertion hook (testseam) fires on real compactions only.
			if hook := s.compactionAssertionHook(); hook != nil {
				hook(int(before), int(after), tokensFreed)
			}
		}
	}

	tools := filterNativeToolsForPermission(nativeToolsForRequest(req), permission)

	// Tool wiring hook (testseam).
	if hook := s.toolWiringHook(); hook != nil {
		hook(decision.Harness, toolNames(tools))
	}
	// Prompt assertion hook (testseam).
	if hook := s.promptAssertionHook(); hook != nil {
		hook(req.SystemPrompt, req.Prompt, nil)
	}

	// Drive the agent loop. Run is synchronous; stall enforcement uses the
	// cancelCtx — when read-only-tool-streak limit fires the callback
	// cancels the context, the loop sees ctx.Done(), returns
	// StatusCancelled, and we override the final to "stalled".
	temperature := float64(req.Temperature)
	loopReq := agentcore.Request{
		Prompt:           req.Prompt,
		SystemPrompt:     req.SystemPrompt,
		Provider:         provider,
		Tools:            tools,
		WorkDir:          req.WorkDir,
		Callback:         cb,
		Metadata:         meta,
		MaxIterations:    maxIter,
		ResolvedModel:    actualModel,
		SelectedProvider: actualProvider,
		Temperature:      &temperature,
		Seed:             req.Seed,
		Reasoning:        effectiveReasoning(req.Reasoning),
		NoStream:         true,
	}
	result, runErr := agentcore.Run(cancelCtx, loopReq)

	// Map agent.Result → harness FinalData.
	final := harnesses.FinalData{
		DurationMS: time.Since(start).Milliseconds(),
		FinalText:  result.Output,
		RoutingActual: &harnesses.RoutingActual{
			Harness:  actualHarness,
			Provider: actualProvider,
			Model:    actualModel,
		},
	}
	if result.Tokens.Total > 0 || result.Tokens.Input > 0 || result.Tokens.Output > 0 {
		final.Usage = &harnesses.FinalUsage{
			InputTokens:      harnesses.IntPtr(result.Tokens.Input),
			OutputTokens:     harnesses.IntPtr(result.Tokens.Output),
			CacheReadTokens:  nil,
			CacheWriteTokens: nil,
			TotalTokens:      harnesses.IntPtr(result.Tokens.Total),
			Source:           harnesses.UsageSourceFallback,
		}
		if result.Tokens.CacheRead > 0 {
			final.Usage.CacheReadTokens = harnesses.IntPtr(result.Tokens.CacheRead)
		}
		if result.Tokens.CacheWrite > 0 {
			final.Usage.CacheWriteTokens = harnesses.IntPtr(result.Tokens.CacheWrite)
		}
		if result.Tokens.CacheRead > 0 || result.Tokens.CacheWrite > 0 {
			final.Usage.CacheTokens = harnesses.IntPtr(result.Tokens.CacheRead + result.Tokens.CacheWrite)
		}
	}
	if result.CostUSD > 0 {
		final.CostUSD = result.CostUSD
	}
	if result.Output != "" {
		emitJSONRaw(out, seq, harnesses.EventTypeTextDelta, meta, harnesses.TextDeltaData{Text: result.Output})
	}
	switch {
	case stalled.Load():
		final.Status = "stalled"
		reason, _ := stallReason.Load().(string)
		// Emit the stall event before the final per CONTRACT-003.
		emitJSONRaw(out, seq, harnesses.EventTypeStall, meta, map[string]any{
			"reason": reason,
			"count":  stallCount.Load(),
		})
		final.Error = reason
	case ctx.Err() == context.DeadlineExceeded || (req.Timeout > 0 && time.Since(start) >= req.Timeout):
		final.Status = "timed_out"
		final.Error = "wall-clock timeout"
	case ctx.Err() == context.Canceled:
		final.Status = "cancelled"
	case runErr != nil:
		final.Status = "failed"
		final.Error = runErr.Error()
	case result.Status == agentcore.StatusError:
		final.Status = "failed"
		if result.Error != nil {
			final.Error = result.Error.Error()
		}
	case result.Status == agentcore.StatusIterationLimit:
		final.Status = string(agentcore.StatusIterationLimit)
	default:
		final.Status = "success"
	}

	// Session log path: when the caller supplied a SessionLogDir, place a
	// per-session JSONL there. We write a minimal final-event line so the
	// path exists and is non-empty even when no streaming sink is wired
	// (e.g., FakeProvider tests). Real session-log writing is the session
	// package's job and runs alongside this.
	if req.SessionLogDir != "" {
		final.SessionLogPath = writeSessionLogStub(req.SessionLogDir, result.SessionID, final, meta)
	}

	emitFinal(out, seq, meta, final)
}

func nativeProviderRequest(req ServiceExecuteRequest, decision RouteDecision) ServiceExecuteRequest {
	out := req
	if decision.Provider != "" {
		out.Provider = decision.Provider
	}
	if decision.Model != "" {
		out.Model = decision.Model
	}
	if decision.Harness != "" {
		out.Harness = decision.Harness
	}
	return out
}

// runSubprocess delegates to a Runner under internal/harnesses/<name>. It
// re-uses the wall-clock-bounded ctx so PTY/orphan reaping is automatic
// when our ctx (which already carries the request Timeout) cancels.
func (s *service) runSubprocess(ctx context.Context, req ServiceExecuteRequest, decision RouteDecision, meta map[string]string, out chan<- ServiceEvent, seq *atomic.Int64, start time.Time, runner harnesses.Harness) {
	hReq := harnesses.ExecuteRequest{
		Prompt:        req.Prompt,
		SystemPrompt:  req.SystemPrompt,
		Provider:      decision.Provider,
		Model:         decision.Model,
		WorkDir:       req.WorkDir,
		Permissions:   req.Permissions,
		Temperature:   req.Temperature,
		Seed:          req.Seed,
		Reasoning:     adapterReasoning(req.Reasoning),
		Timeout:       req.Timeout,
		IdleTimeout:   req.IdleTimeout,
		SessionLogDir: req.SessionLogDir,
		Metadata:      meta,
	}
	in, err := runner.Execute(ctx, hReq)
	if err != nil {
		emitFinal(out, seq, meta, harnesses.FinalData{
			Status:     "failed",
			Error:      err.Error(),
			DurationMS: time.Since(start).Milliseconds(),
			RoutingActual: &harnesses.RoutingActual{
				Harness:  decision.Harness,
				Provider: decision.Provider,
				Model:    decision.Model,
			},
		})
		return
	}
	// Forward events. Stamp metadata onto each (subprocess runners already
	// echo metadata, but we re-stamp defensively to match the contract's
	// "every Event carries it" guarantee).
	for ev := range in {
		if ev.Metadata == nil {
			ev.Metadata = meta
		}
		if ev.Type == harnesses.EventTypeFinal {
			ev = stampSubprocessFinalRouting(ev, decision)
		}
		// Re-sequence events so a single Execute presents a monotonically
		// increasing sequence to the consumer.
		ev.Sequence = seq.Add(1) - 1
		select {
		case out <- ev:
		case <-ctx.Done():
			return
		}
	}
}

func stampSubprocessFinalRouting(ev ServiceEvent, decision RouteDecision) ServiceEvent {
	var final harnesses.FinalData
	if err := json.Unmarshal(ev.Data, &final); err != nil {
		return ev
	}
	if final.RoutingActual == nil {
		final.RoutingActual = &harnesses.RoutingActual{
			Harness:  decision.Harness,
			Provider: decision.Provider,
			Model:    decision.Model,
		}
	}
	raw, err := json.Marshal(final)
	if err != nil {
		return ev
	}
	ev.Data = raw
	return ev
}

// emitFinal wraps a FinalData into a ServiceEvent and writes it to out.
// The channel close happens in the caller via defer; this only writes the
// terminator event.
func emitFinal(out chan<- ServiceEvent, seq *atomic.Int64, meta map[string]string, final harnesses.FinalData) {
	raw, err := json.Marshal(final)
	if err != nil {
		raw = []byte(`{"status":"failed","error":"marshal final"}`)
	}
	ev := harnesses.Event{
		Type:     harnesses.EventTypeFinal,
		Sequence: seq.Add(1) - 1,
		Time:     time.Now().UTC(),
		Metadata: meta,
		Data:     raw,
	}
	select {
	case out <- ev:
	case <-time.After(time.Second):
	}
}

// emitFatalFinal is used when Execute itself can't construct a route. It
// writes a single failed final event then closes the channel — used for
// the "no consumer goroutine" path so we still satisfy the channel
// contract.
func emitFatalFinal(out chan<- ServiceEvent, meta map[string]string, status, errMsg string) {
	defer close(out)
	final := harnesses.FinalData{Status: status, Error: errMsg}
	raw, _ := json.Marshal(final)
	ev := harnesses.Event{
		Type:     harnesses.EventTypeFinal,
		Sequence: 0,
		Time:     time.Now().UTC(),
		Metadata: meta,
		Data:     raw,
	}
	select {
	case out <- ev:
	case <-time.After(time.Second):
	}
}

// emitJSON marshals payload and writes a typed event to out.
func emitJSON(out chan<- ServiceEvent, seq *atomic.Int64, t harnesses.EventType, meta map[string]string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ev := harnesses.Event{
		Type:     t,
		Sequence: seq.Add(1) - 1,
		Time:     time.Now().UTC(),
		Metadata: meta,
		Data:     raw,
	}
	select {
	case out <- ev:
	case <-time.After(time.Second):
	}
}

// emitJSONRaw is the typed-payload variant used inside the loop callback.
func emitJSONRaw(out chan<- ServiceEvent, seq *atomic.Int64, t harnesses.EventType, meta map[string]string, payload any) {
	emitJSON(out, seq, t, meta, payload)
}

// errProviderRequestTimeout is the package-local equivalent of
// internal/execution.ErrProviderRequestTimeout. service.Execute can't
// import internal/execution (cycle) so we declare the sentinel locally.
var errProviderRequestTimeout = errors.New("provider request timeout")

// wrapProviderRequestTimeout decorates p with a per-Chat wall-clock cap.
// It is the in-package mirror of execution.WrapProviderWithDeadlinesTimeouts
// minus the streaming variant (the in-process loop uses the non-streaming
// Provider interface in this code path; streaming wrapping lives in
// internal/execution and is reachable via the CLI command layer).
func wrapProviderRequestTimeout(p agentcore.Provider, requestTimeout time.Duration) agentcore.Provider {
	if p == nil || requestTimeout <= 0 {
		return p
	}
	return &timeoutProviderInline{inner: p, requestTimeout: requestTimeout}
}

type timeoutProviderInline struct {
	inner          agentcore.Provider
	requestTimeout time.Duration
}

func (p *timeoutProviderInline) Chat(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolDef, opts agentcore.Options) (agentcore.Response, error) {
	if p.requestTimeout <= 0 {
		return p.inner.Chat(ctx, messages, tools, opts)
	}
	cctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()
	resp, err := p.inner.Chat(cctx, messages, tools, opts)
	if err != nil && ctx.Err() == nil && cctx.Err() == context.DeadlineExceeded {
		return resp, fmt.Errorf("%w: wall-clock %s", errProviderRequestTimeout, p.requestTimeout)
	}
	return resp, err
}

// writeSessionLogStub creates the session log directory and writes a single
// final-event line so the per-request session log path is real on disk.
// Real progress streaming is the session package's responsibility and is
// orthogonal to this stub. Returns the absolute file path.
func writeSessionLogStub(dir, sessionID string, final harnesses.FinalData, meta map[string]string) string {
	if sessionID == "" {
		sessionID = fmt.Sprintf("s-%d", time.Now().UnixNano())
	}
	f, err := sessionlog.OpenAppend(dir, sessionID)
	if err != nil {
		return ""
	}
	defer f.Close()
	path := f.Name()
	line, err := json.Marshal(map[string]any{
		"type":     "final",
		"final":    final,
		"metadata": meta,
		"time":     time.Now().UTC(),
	})
	if err == nil {
		// Stamp metadata onto the session log line per CONTRACT-003.
		_, _ = f.Write(append(line, '\n'))
	}
	return path
}
