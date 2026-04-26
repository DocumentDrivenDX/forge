package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

// overrideAxis* enumerates the three independently-tracked override axes.
const (
	overrideAxisHarness  = "harness"
	overrideAxisProvider = "provider"
	overrideAxisModel    = "model"
)

// overrideContext carries the per-Execute override-event payload from the
// service entrypoint through to the channel-fan-out goroutine, where the
// terminal outcome is stamped on right before the final event is forwarded.
//
// record points at the live in-memory ring entry for this request (set by
// Execute via recordRoutingQualityForRequest); the fan-out goroutine
// back-writes the outcome onto it when the override event fires so
// RouteStatus surfaces non-zero outcome aggregates.
//
// sl points at the request's session log (set in runExecute after
// openSessionLog); the fan-out goroutine writes the override event there
// so UsageReport can recompute routing-quality from persisted session logs
// across restarts and beyond the in-memory ring's bounded retention.
type overrideContext struct {
	payload ServiceOverrideData
	emitted atomic.Bool
	record  *routingQualityRecord
	sl      atomic.Pointer[serviceSessionLog]
}

// axesOverridden returns the canonical, ordered list of axes the caller
// pinned. Order is fixed (harness, provider, model) so consumers can
// compare across requests.
func axesOverridden(req ServiceExecuteRequest) []string {
	axes := make([]string, 0, 3)
	if req.Harness != "" {
		axes = append(axes, overrideAxisHarness)
	}
	if req.Provider != "" {
		axes = append(axes, overrideAxisProvider)
	}
	if req.Model != "" {
		axes = append(axes, overrideAxisModel)
	}
	return axes
}

// buildOverrideContext computes the override event payload for req, including
// the second (axes-stripped) ResolveRoute call. Returns nil when no override
// axis is set on the request.
//
// The auto-decision call is best-effort: if it fails (e.g., empty inputs
// after stripping leave nothing to resolve), the auto_decision and component
// fields are left zero. The override event still fires per ADR-006 §7.
func (s *service) buildOverrideContext(ctx context.Context, req ServiceExecuteRequest) *overrideContext {
	axes := axesOverridden(req)
	if len(axes) == 0 {
		return nil
	}

	userPin := ServiceOverridePin{
		Harness:  req.Harness,
		Provider: req.Provider,
		Model:    req.Model,
	}

	auto, autoScore, autoComps := s.resolveAutoDecisionForOverride(ctx, req, axes)

	matchPerAxis := make(map[string]bool, len(axes))
	for _, axis := range axes {
		switch axis {
		case overrideAxisHarness:
			matchPerAxis[axis] = userPin.Harness != "" && userPin.Harness == auto.Harness
		case overrideAxisProvider:
			matchPerAxis[axis] = userPin.Provider != "" && userPin.Provider == auto.Provider
		case overrideAxisModel:
			matchPerAxis[axis] = userPin.Model != "" && userPin.Model == auto.Model
		}
	}

	return &overrideContext{
		payload: ServiceOverrideData{
			UserPin:        userPin,
			AutoDecision:   auto,
			AxesOverridden: axes,
			MatchPerAxis:   matchPerAxis,
			AutoScore:      autoScore,
			AutoComponents: autoComps,
			PromptFeatures: buildPromptFeatures(req),
			ReasonHint:     overrideReasonHint(req),
		},
	}
}

// resolveAutoDecisionForOverride runs ResolveRoute with the override axes
// stripped to produce the unconstrained auto pick. Returns zero values when
// the resolution does not yield a usable decision.
func (s *service) resolveAutoDecisionForOverride(ctx context.Context, req ServiceExecuteRequest, axes []string) (ServiceOverridePin, float64, ServiceOverrideAutoComponents) {
	// Strip the overridden axes; keep all non-axis intent fields verbatim.
	// The non-axes branches are unreachable in practice (if the axis is in
	// `axes`, the field on req is non-empty), but we surface non-axis user
	// inputs explicitly so future axis additions are obvious here.
	stripped := RouteRequest{
		Profile:               req.Profile,
		ModelRef:              req.ModelRef,
		Reasoning:             req.Reasoning,
		Permissions:           req.Permissions,
		EstimatedPromptTokens: req.EstimatedPromptTokens,
		RequiresTools:         req.RequiresTools,
		CachePolicy:           req.CachePolicy,
	}
	if !stringIn(axes, overrideAxisHarness) {
		stripped.Harness = req.Harness
	}
	if !stringIn(axes, overrideAxisProvider) {
		stripped.Provider = req.Provider
	}
	if !stringIn(axes, overrideAxisModel) {
		stripped.Model = req.Model
	}

	dec, err := s.ResolveRoute(ctx, stripped)
	if err != nil || dec == nil || dec.Harness == "" {
		return ServiceOverridePin{}, 0, ServiceOverrideAutoComponents{}
	}
	auto := ServiceOverridePin{
		Harness:  dec.Harness,
		Provider: dec.Provider,
		Model:    dec.Model,
	}
	for _, c := range dec.Candidates {
		if !c.Eligible {
			continue
		}
		if c.Harness == dec.Harness && c.Provider == dec.Provider && c.Model == dec.Model {
			return auto, c.Score, ServiceOverrideAutoComponents{
				Cost:        c.Components.Cost,
				LatencyMS:   c.Components.LatencyMS,
				SuccessRate: c.Components.SuccessRate,
				Capability:  c.Components.Capability,
			}
		}
	}
	return auto, 0, ServiceOverrideAutoComponents{}
}

func stringIn(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// buildPromptFeatures populates the prompt-features stanza of the override
// event. estimated_tokens is set from req.EstimatedPromptTokens when > 0;
// otherwise nil (best-effort harness-level tokenizer is deferred).
func buildPromptFeatures(req ServiceExecuteRequest) ServiceOverridePromptFeatures {
	pf := ServiceOverridePromptFeatures{
		RequiresTools: req.RequiresTools,
		Reasoning:     string(req.Reasoning),
	}
	if req.EstimatedPromptTokens > 0 {
		v := req.EstimatedPromptTokens
		pf.EstimatedTokens = &v
	}
	return pf
}

// overrideReasonHint returns the caller-supplied free-form reason from
// Metadata["override.reason"], or the empty string when unset.
func overrideReasonHint(req ServiceExecuteRequest) string {
	if req.Metadata == nil {
		return ""
	}
	return req.Metadata["override.reason"]
}

// makeOverrideEvent constructs the wire-level override event, stamping
// outcome from the corresponding final event. Also returns the populated
// payload so callers can back-write the outcome to the in-memory ring and
// persist the same payload to the session log in lock-step.
func makeOverrideEvent(ovr *overrideContext, sessionID string, finalEv ServiceEvent, meta map[string]string) (ServiceEvent, ServiceOverrideData, bool) {
	if ovr == nil {
		return ServiceEvent{}, ServiceOverrideData{}, false
	}
	payload := ovr.payload
	payload.SessionID = sessionID
	if final, ok := decodeFinalForOutcome(finalEv); ok {
		payload.Outcome = &ServiceOverrideOutcome{
			Status:     final.Status,
			CostUSD:    final.CostUSD,
			DurationMS: final.DurationMS,
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ServiceEvent{}, ServiceOverrideData{}, false
	}
	seq := finalEv.Sequence
	if seq > 0 {
		seq--
	}
	t := finalEv.Time
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return ServiceEvent{
		Type:     harnesses.EventType(ServiceEventTypeOverride),
		Sequence: seq,
		Time:     t,
		Metadata: meta,
		Data:     raw,
	}, payload, true
}

func decodeFinalForOutcome(ev ServiceEvent) (ServiceFinalData, bool) {
	var f ServiceFinalData
	if len(ev.Data) == 0 {
		return f, false
	}
	if err := json.Unmarshal(ev.Data, &f); err != nil {
		return f, false
	}
	return f, true
}

// makeRejectedOverrideEvent constructs a rejected_override event from the
// override context plus the typed pin error. Outcome is intentionally nil.
// Returns the populated payload alongside the wire event so callers can
// surface the same struct via ErrRejectedOverride.
func makeRejectedOverrideEvent(ovr *overrideContext, sessionID string, pinErr error, meta map[string]string) (ServiceEvent, ServiceOverrideData, bool) {
	if ovr == nil {
		return ServiceEvent{}, ServiceOverrideData{}, false
	}
	payload := ovr.payload
	payload.SessionID = sessionID
	payload.Outcome = nil
	if pinErr != nil {
		payload.RejectionError = pinErr.Error()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ServiceEvent{}, ServiceOverrideData{}, false
	}
	return ServiceEvent{
		Type:     harnesses.EventType(ServiceEventTypeRejectedOverride),
		Sequence: 0,
		Time:     time.Now().UTC(),
		Metadata: meta,
		Data:     raw,
	}, payload, true
}

// ErrRejectedOverride wraps a pin-rejection error and carries the
// rejected_override event payload that was generated for the failed pin.
// Callers can extract the payload via errors.As to surface override-quality
// telemetry even when Execute returns a typed pin error rather than a
// channel.
type ErrRejectedOverride struct {
	// Inner is the underlying pin error (e.g., *ErrHarnessModelIncompatible).
	Inner error
	// Event is the rejected_override payload (no outcome).
	Event ServiceOverrideData
}

func (e *ErrRejectedOverride) Error() string {
	if e == nil || e.Inner == nil {
		return "rejected override"
	}
	return e.Inner.Error()
}

func (e *ErrRejectedOverride) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Inner
}

// AsRejectedOverride extracts the rejected_override payload from err if
// present. Returns (data, true) when err carries the wrapper, (zero, false)
// otherwise.
func AsRejectedOverride(err error) (ServiceOverrideData, bool) {
	var w *ErrRejectedOverride
	if errors.As(err, &w) && w != nil {
		return w.Event, true
	}
	return ServiceOverrideData{}, false
}
