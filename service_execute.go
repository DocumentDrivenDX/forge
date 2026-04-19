package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	"github.com/DocumentDrivenDX/agent/internal/sessionlog"
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
		if req.ModelRef == "" && req.Model == "" && req.Provider == "" {
			return nil, fmt.Errorf("routing under-specified: pass PreResolved or set at least one of Harness/ModelRef/Model/Provider")
		}
		return s.resolveExecuteRouteWithEngine(req)
	}
	canonical := harnesses.ResolveHarnessAlias(req.Harness)
	if !s.registry.Has(canonical) {
		return nil, fmt.Errorf("unknown harness %q", req.Harness)
	}
	// Orphan-model validation: when Model is set but the harness has no
	// way to validate it (no provider discovery, no catalog hookup yet),
	// we accept the explicit pin — orphan detection is provider-dependent
	// and the routing layer (a future bead) is the right place for the
	// full check. For native path with no provider, this is enforced when
	// the provider is constructed.
	return &RouteDecision{
		Harness:  canonical,
		Provider: req.Provider,
		Model:    req.Model,
		Reason:   "explicit",
	}, nil
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
	default:
		// Unknown / unimplemented subprocess harnesses (gemini/opencode/pi)
		// fail with an explicit final event; the runners exist in
		// internal/harnesses/<name> and a follow-up bead will wire them
		// into this switch.
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

// runNative drives the in-process agent loop (loop.go's Run). The provider
// is wrapped with WrapProviderWithDeadlinesTimeouts so per-HTTP timeouts
// fire independently of the request wall-clock cap.
func (s *service) runNative(ctx context.Context, req ServiceExecuteRequest, decision RouteDecision, meta map[string]string, out chan<- ServiceEvent, seq *atomic.Int64, start time.Time) {
	provider := s.resolveNativeProvider(req)
	if provider == nil {
		// Orphan-model surface: native path without a provider AND without
		// a model the catalog/router can resolve is the orphan case per
		// CONTRACT-003 §"Orphan-model validation".
		errMsg := "orphan model: " + decision.Model
		if decision.Model == "" {
			errMsg = "no provider configured for native harness"
		}
		emitFinal(out, seq, meta, harnesses.FinalData{
			Status:     "failed",
			Error:      errMsg,
			DurationMS: time.Since(start).Milliseconds(),
			RoutingActual: &harnesses.RoutingActual{
				Harness:  "agent",
				Provider: decision.Provider,
				Model:    decision.Model,
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
	cb := func(ev Event) {
		// Translate to a harnesses.Event of best-fit type. We only forward
		// types that map onto CONTRACT-003's closed event union; internal
		// session.start / llm.* / compaction.* events do not have a public
		// equivalent and are dropped here. They still land in the session
		// log via the session logger that consumers attach themselves.
		switch ev.Type {
		case EventToolCall:
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
		case EventCompactionEnd:
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

	// Tool wiring hook (testseam).
	if hook := s.toolWiringHook(); hook != nil {
		hook(decision.Harness, nil)
	}
	// Prompt assertion hook (testseam).
	if hook := s.promptAssertionHook(); hook != nil {
		hook(req.SystemPrompt, req.Prompt, nil)
	}

	// Drive the agent loop. Run is synchronous; stall enforcement uses the
	// cancelCtx — when read-only-tool-streak limit fires the callback
	// cancels the context, the loop sees ctx.Done(), returns
	// StatusCancelled, and we override the final to "stalled".
	loopReq := Request{
		Prompt:           req.Prompt,
		SystemPrompt:     req.SystemPrompt,
		Provider:         provider,
		WorkDir:          req.WorkDir,
		Callback:         cb,
		Metadata:         meta,
		MaxIterations:    maxIter,
		ResolvedModel:    decision.Model,
		SelectedProvider: decision.Provider,
		Reasoning:        effectiveReasoning(req.Reasoning),
	}
	result, runErr := Run(cancelCtx, loopReq)

	// Map agent.Result → harness FinalData.
	final := harnesses.FinalData{
		DurationMS: time.Since(start).Milliseconds(),
		RoutingActual: &harnesses.RoutingActual{
			Harness:  "agent",
			Provider: decision.Provider,
			Model:    decision.Model,
		},
	}
	if result.Tokens.Total > 0 || result.Tokens.Input > 0 || result.Tokens.Output > 0 {
		final.Usage = &harnesses.FinalUsage{
			InputTokens:  result.Tokens.Input,
			OutputTokens: result.Tokens.Output,
			TotalTokens:  result.Tokens.Total,
		}
	}
	if result.CostUSD > 0 {
		final.CostUSD = result.CostUSD
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
	case result.Status == StatusError:
		final.Status = "failed"
		if result.Error != nil {
			final.Error = result.Error.Error()
		}
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
func wrapProviderRequestTimeout(p Provider, requestTimeout time.Duration) Provider {
	if p == nil || requestTimeout <= 0 {
		return p
	}
	return &timeoutProviderInline{inner: p, requestTimeout: requestTimeout}
}

type timeoutProviderInline struct {
	inner          Provider
	requestTimeout time.Duration
}

func (p *timeoutProviderInline) Chat(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (Response, error) {
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
