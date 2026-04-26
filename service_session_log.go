package agent

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	"github.com/DocumentDrivenDX/agent/internal/session"
)

// serviceSessionLog is the service-owned writer that persists the session
// lifecycle records (session.start + session.end) for one Execute call. It
// is constructed in runExecute after the route is resolved and torn down in
// a defer on the same goroutine. Sub-paths (runNative, runSubprocess, ...)
// emit the terminal event through finalizeAndEmit, which delegates here to
// keep the session log in sync with the public event stream.
type serviceSessionLog struct {
	logger    *session.Logger
	path      string
	sessionID string
	endOnce   sync.Once
	endWrote  atomic.Bool
	closeOnce sync.Once
}

// openSessionLog creates the session-log writer for req and emits the
// session.start record. A nil-or-empty req.SessionLogDir yields a no-op
// writer so callers don't need to branch on whether logging is enabled.
func (s *service) openSessionLog(req ServiceExecuteRequest, decision RouteDecision, sessionID string) *serviceSessionLog {
	if req.SessionLogDir == "" || sessionID == "" {
		return &serviceSessionLog{}
	}
	logger := session.NewLogger(req.SessionLogDir, sessionID)
	sl := &serviceSessionLog{
		logger:    logger,
		path:      filepath.Join(req.SessionLogDir, sessionID+".jsonl"),
		sessionID: sessionID,
	}
	start := session.SessionStartData{
		Provider:          s.providerTypeLabel(decision.Provider),
		Model:             decision.Model,
		SelectedProvider:  decision.Provider,
		SelectedRoute:     req.SelectedRoute,
		RequestedModel:    req.Model,
		RequestedModelRef: req.ModelRef,
		ResolvedModelRef:  req.ResolvedModelRef,
		ResolvedModel:     decision.Model,
		Reasoning:         req.Reasoning,
		WorkDir:           req.WorkDir,
		MaxIterations:     req.MaxIterations,
		Prompt:            req.Prompt,
		SystemPrompt:      req.SystemPrompt,
		Metadata:          req.Metadata,
	}
	logger.Emit(agentcore.EventSessionStart, start)
	return sl
}

// writeEnd records the terminal session.end event. It is idempotent: the
// first call wins. Callers should invoke this whenever a harnesses.FinalData
// is produced — finalizeAndEmit threads it together with the public-stream
// emit so the two views stay consistent.
func (sl *serviceSessionLog) writeEnd(req ServiceExecuteRequest, meta map[string]string, final harnesses.FinalData) {
	if sl == nil || sl.logger == nil {
		return
	}
	sl.endOnce.Do(func() {
		sl.endWrote.Store(true)
		end := session.SessionEndData{
			Status:            harnessStatusToCoreStatus(final.Status),
			Output:            final.FinalText,
			Tokens:            finalUsageToCoreTokens(final.Usage),
			DurationMs:        final.DurationMS,
			SelectedRoute:     req.SelectedRoute,
			RequestedModel:    req.Model,
			RequestedModelRef: req.ModelRef,
			ResolvedModelRef:  req.ResolvedModelRef,
			Reasoning:         req.Reasoning,
			Metadata:          meta,
			Error:             final.Error,
		}
		if final.CostUSD > 0 {
			cost := final.CostUSD
			end.CostUSD = &cost
		}
		if final.RoutingActual != nil {
			end.Model = final.RoutingActual.Model
			end.SelectedProvider = final.RoutingActual.Provider
			end.ResolvedModel = final.RoutingActual.Model
			end.AttemptedProviders = append([]string(nil), final.RoutingActual.FallbackChainFired...)
			if len(end.AttemptedProviders) > 1 {
				end.FailoverCount = len(end.AttemptedProviders) - 1
			}
		}
		sl.logger.Emit(agentcore.EventSessionEnd, end)
	})
}

// writeEvent persists a raw agent event to the session log. Used by the
// native loop callback to capture llm.request / llm.response / tool.call /
// compaction.* events so kept-sandbox bundles preserve a complete trace
// for benchmark reruns and post-mortem debugging. session.start and
// session.end are skipped because writeStart/writeEnd own those records and
// enrich them with service-side routing fields the loop does not see.
func (sl *serviceSessionLog) writeEvent(ev agentcore.Event) {
	if sl == nil || sl.logger == nil {
		return
	}
	switch ev.Type {
	case agentcore.EventSessionStart, agentcore.EventSessionEnd,
		agentcore.EventOverride, agentcore.EventRejectedOverride:
		return
	}
	sl.logger.Write(ev)
}

// writeOverrideEvent persists an override or rejected_override payload to
// the session log so windowed reporting (UsageReport, ADR-006 §5) can
// recompute routing-quality across restarts and beyond the in-memory
// ring's bounded retention. eventType is one of ServiceEventTypeOverride
// / ServiceEventTypeRejectedOverride.
func (sl *serviceSessionLog) writeOverrideEvent(eventType string, payload ServiceOverrideData) {
	if sl == nil || sl.logger == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	sl.logger.Write(agentcore.Event{
		SessionID: sl.sessionID,
		Type:      agentcore.EventType(eventType),
		Timestamp: time.Now().UTC(),
		Data:      raw,
	})
}

// close flushes the underlying file. Safe to call multiple times.
func (sl *serviceSessionLog) close() {
	if sl == nil || sl.logger == nil {
		return
	}
	sl.closeOnce.Do(func() {
		_ = sl.logger.Close()
	})
}

// endWritten reports whether writeEnd has already recorded a terminal event.
func (sl *serviceSessionLog) endWritten() bool {
	if sl == nil {
		return false
	}
	return sl.endWrote.Load()
}

// providerTypeLabel maps a configured provider name ("local") to its provider
// type ("lmstudio") when available. Returns the input unchanged if no
// ServiceConfig is attached or the name is not configured. Callers use this
// to populate session-log Provider fields, which historically carry the
// provider *type* rather than the configured name.
func (s *service) providerTypeLabel(name string) string {
	if s == nil || s.opts.ServiceConfig == nil || name == "" {
		return name
	}
	entry, ok := s.opts.ServiceConfig.Provider(name)
	if !ok || entry.Type == "" {
		return name
	}
	return entry.Type
}

// harnessStatusToCoreStatus maps a public harnesses.FinalData.Status string
// to an internal agentcore.Status. Unknown / error-y statuses collapse to
// StatusError so session.end always carries a well-defined status.
func harnessStatusToCoreStatus(status string) agentcore.Status {
	switch status {
	case "success":
		return agentcore.StatusSuccess
	case "iteration_limit":
		return agentcore.StatusIterationLimit
	case "cancelled":
		return agentcore.StatusCancelled
	default:
		return agentcore.StatusError
	}
}

// finalUsageToCoreTokens converts the public FinalUsage pointer form into
// the internal TokenUsage struct used by session.end. Nil usage yields a
// zero-value struct.
func finalUsageToCoreTokens(usage *harnesses.FinalUsage) agentcore.TokenUsage {
	if usage == nil {
		return agentcore.TokenUsage{}
	}
	return agentcore.TokenUsage{
		Input:      derefHarnessInt(usage.InputTokens),
		Output:     derefHarnessInt(usage.OutputTokens),
		CacheRead:  derefHarnessInt(usage.CacheReadTokens),
		CacheWrite: derefHarnessInt(usage.CacheWriteTokens),
		Total:      derefHarnessInt(usage.TotalTokens),
	}
}

func derefHarnessInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
