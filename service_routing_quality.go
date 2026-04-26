package agent

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// ADR-006 §5: routing-quality is the user-facing measure of how often
// auto-routing produces a decision the caller is willing to live with.
// It is distinct from per-(provider, model) provider-reliability (the
// observed completion rate of a chosen candidate). The two compose:
// routing-quality × provider-reliability ≈ end-to-end completion rate.

// RoutingQualityMetrics is the bundle of three first-class metrics ADR-006
// makes operator-visible. AutoAcceptanceRate and OverrideDisagreementRate are
// fractions in [0,1]; OverrideClassBreakdown is the diagnostic pivot. All
// fields zero when the underlying window contains no requests.
type RoutingQualityMetrics struct {
	// AutoAcceptanceRate = no-override requests / total requests. Higher is
	// better. The headline number for routing health.
	AutoAcceptanceRate float64 `json:"auto_acceptance_rate"`

	// OverrideDisagreementRate = overrides where the user pin differs from
	// auto on at least one overridden axis / total overrides. Lower is
	// better. Coincidental-agreement overrides land in the denominator but
	// not the numerator.
	OverrideDisagreementRate float64 `json:"override_disagreement_rate"`

	// OverrideClassBreakdown is a pivot of (prompt_features bucket,
	// axis_overridden, match_per_axis) → count + outcome aggregates.
	// Sorted deterministically by (PromptFeatureBucket, Axis, Match) so
	// snapshot tests remain stable across runs.
	OverrideClassBreakdown []OverrideClassBucket `json:"override_class_breakdown,omitempty"`

	// TotalRequests is the total Execute count over the metric window
	// (including overridden requests). Surface for operator UIs that want
	// to display "k out of N" alongside the rate.
	TotalRequests int `json:"total_requests"`

	// TotalOverrides is the total override count over the metric window.
	// Equal to TotalRequests-(no-override requests).
	TotalOverrides int `json:"total_overrides"`
}

// OverrideClassBucket is one cell in the override-class pivot.
//
// Each override event contributes one bucket per overridden axis: an
// override that pins both Harness and Model produces two breakdown rows for
// that event, with axis="harness" and axis="model" respectively. The
// PromptFeatureBucket coalesces estimated_tokens / requires_tools /
// reasoning into a coarse string so operators can read the pivot without
// drowning in cardinality.
type OverrideClassBucket struct {
	PromptFeatureBucket string `json:"prompt_feature_bucket"`
	Axis                string `json:"axis"`
	Match               bool   `json:"match"`
	Count               int    `json:"count"`

	SuccessOutcomes   int `json:"success_outcomes"`
	StalledOutcomes   int `json:"stalled_outcomes"`
	FailedOutcomes    int `json:"failed_outcomes"`
	CancelledOutcomes int `json:"cancelled_outcomes"`
	UnknownOutcomes   int `json:"unknown_outcomes"`
}

// routingQualityRecord is one entry in the in-process routing-quality
// store. Override is nil for un-overridden requests; non-nil entries carry
// the full override event payload (with outcome stamped on after the final
// event).
type routingQualityRecord struct {
	at       time.Time
	override *ServiceOverrideData
}

// routingQualityStore is the service-scope in-memory ring of routing-quality
// records. Bounded so long-lived services don't grow unboundedly; the
// retention budget is sized so RouteStatus's "last 100 requests" window is
// always covered while UsageReport's "last 30d" window is best-effort (the
// authoritative cross-restart source remains session logs once persistence
// is added).
type routingQualityStore struct {
	mu      sync.RWMutex
	records []*routingQualityRecord
	cap     int
}

const routingQualityStoreCap = 1024

func newRoutingQualityStore() *routingQualityStore {
	return &routingQualityStore{cap: routingQualityStoreCap}
}

// recordRequest appends a request to the store and returns the freshly
// allocated record. override may be nil for the no-override case. The
// returned pointer remains valid even after the bounded ring rotates,
// allowing callers (the override fan-out goroutine) to back-write the
// post-execution outcome without racing the ring's eviction.
func (s *routingQualityStore) recordRequest(at time.Time, override *ServiceOverrideData) *routingQualityRecord {
	if s == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	rec := &routingQualityRecord{at: at.UTC()}
	if override != nil {
		clone := *override
		rec.override = &clone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
	if s.cap > 0 && len(s.records) > s.cap {
		drop := len(s.records) - s.cap
		s.records = s.records[drop:]
	}
	return rec
}

// snapshotRecent returns up to maxN of the most recent records, optionally
// filtered by since (zero means no time filter). Records are returned in
// insertion order (oldest first within the slice).
func (s *routingQualityStore) snapshotRecent(maxN int, since time.Time) []*routingQualityRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*routingQualityRecord, 0, len(s.records))
	for _, r := range s.records {
		if !since.IsZero() && r.at.Before(since) {
			continue
		}
		out = append(out, r)
	}
	if maxN > 0 && len(out) > maxN {
		out = out[len(out)-maxN:]
	}
	return out
}

// snapshotWindow returns records whose timestamps fall within [start, end).
// Either bound may be zero to mean "unbounded".
func (s *routingQualityStore) snapshotWindow(start, end time.Time) []*routingQualityRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*routingQualityRecord, 0, len(s.records))
	for _, r := range s.records {
		if !start.IsZero() && r.at.Before(start) {
			continue
		}
		if !end.IsZero() && !r.at.Before(end) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// computeRoutingQualityMetrics is the pure aggregator. Test entry point.
// totalRequests is the headline denominator (the count of Execute calls
// over the window — overridden and non-overridden alike). overrides is the
// list of override events recorded over the same window. The function does
// not interact with the store, so tests can feed synthetic data directly.
func computeRoutingQualityMetrics(totalRequests int, overrides []ServiceOverrideData) RoutingQualityMetrics {
	m := RoutingQualityMetrics{
		TotalRequests:  totalRequests,
		TotalOverrides: len(overrides),
	}
	if totalRequests > 0 {
		noOverride := totalRequests - len(overrides)
		if noOverride < 0 {
			noOverride = 0
		}
		m.AutoAcceptanceRate = float64(noOverride) / float64(totalRequests)
	}
	if len(overrides) > 0 {
		disagree := 0
		for _, ov := range overrides {
			if overrideDisagreesOnAnyAxis(ov) {
				disagree++
			}
		}
		m.OverrideDisagreementRate = float64(disagree) / float64(len(overrides))
	}
	m.OverrideClassBreakdown = buildOverrideClassBreakdown(overrides)
	return m
}

// computeRoutingQualityMetricsFromRecords aggregates the store-side record
// shape directly. Used by RouteStatus once it has selected its window.
// UsageReport sources from session logs instead — see
// aggregateRoutingQualityFromSessionLogs.
func computeRoutingQualityMetricsFromRecords(records []*routingQualityRecord) RoutingQualityMetrics {
	overrides := make([]ServiceOverrideData, 0, len(records))
	for _, r := range records {
		if r == nil || r.override == nil {
			continue
		}
		overrides = append(overrides, *r.override)
	}
	return computeRoutingQualityMetrics(len(records), overrides)
}

// overrideDisagreesOnAnyAxis returns true when the user pin differs from
// auto on at least one of the overridden axes. Coincidental-agreement
// overrides (every overridden axis matches auto) return false.
func overrideDisagreesOnAnyAxis(ov ServiceOverrideData) bool {
	if len(ov.AxesOverridden) == 0 {
		return false
	}
	for _, axis := range ov.AxesOverridden {
		match, ok := ov.MatchPerAxis[axis]
		if !ok || !match {
			return true
		}
	}
	return false
}

// buildOverrideClassBreakdown pivots overrides into a deterministic list of
// (PromptFeatureBucket, Axis, Match) buckets with outcome aggregates. Each
// override contributes one bucket increment per overridden axis.
func buildOverrideClassBreakdown(overrides []ServiceOverrideData) []OverrideClassBucket {
	if len(overrides) == 0 {
		return nil
	}
	type key struct {
		bucket string
		axis   string
		match  bool
	}
	tally := make(map[key]*OverrideClassBucket)
	for _, ov := range overrides {
		bucket := promptFeatureBucket(ov.PromptFeatures)
		for _, axis := range ov.AxesOverridden {
			match := ov.MatchPerAxis[axis]
			k := key{bucket: bucket, axis: axis, match: match}
			b, ok := tally[k]
			if !ok {
				b = &OverrideClassBucket{
					PromptFeatureBucket: bucket,
					Axis:                axis,
					Match:               match,
				}
				tally[k] = b
			}
			b.Count++
			switch outcomeStatus(ov.Outcome) {
			case "success":
				b.SuccessOutcomes++
			case "stalled":
				b.StalledOutcomes++
			case "failed":
				b.FailedOutcomes++
			case "cancelled":
				b.CancelledOutcomes++
			default:
				b.UnknownOutcomes++
			}
		}
	}
	out := make([]OverrideClassBucket, 0, len(tally))
	for _, b := range tally {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PromptFeatureBucket != out[j].PromptFeatureBucket {
			return out[i].PromptFeatureBucket < out[j].PromptFeatureBucket
		}
		if out[i].Axis != out[j].Axis {
			return out[i].Axis < out[j].Axis
		}
		// false < true to keep mismatch rows ahead of coincidental-agreement
		return !out[i].Match && out[j].Match
	})
	return out
}

func outcomeStatus(o *ServiceOverrideOutcome) string {
	if o == nil {
		return ""
	}
	return o.Status
}

// promptFeatureBucket coalesces (estimated_tokens, requires_tools, reasoning)
// into a stable, low-cardinality string label. Buckets are deliberately
// coarse so the breakdown stays human-scannable; finer-grained slicing is
// future work.
func promptFeatureBucket(pf ServiceOverridePromptFeatures) string {
	parts := make([]string, 0, 3)
	parts = append(parts, "tokens="+tokenSizeBucket(pf.EstimatedTokens))
	if pf.RequiresTools {
		parts = append(parts, "tools=yes")
	} else {
		parts = append(parts, "tools=no")
	}
	if pf.Reasoning != "" {
		parts = append(parts, "reasoning="+pf.Reasoning)
	} else {
		parts = append(parts, "reasoning=none")
	}
	return strings.Join(parts, ",")
}

// recordRoutingQualityForRequest records one Execute call into the
// service's routing-quality store. ovr may be nil for non-overridden
// requests; when non-nil, the recorded record pointer is stashed onto the
// override context so the fan-out goroutine can back-write the
// post-execution outcome (success / stalled / failed / cancelled) once the
// final event arrives. The back-write is what makes the in-memory ring's
// outcome aggregates real rather than always-zero (ADR-006 §5).
func (s *service) recordRoutingQualityForRequest(ovr *overrideContext) {
	if s == nil || s.routingQuality == nil {
		return
	}
	now := time.Now().UTC()
	if ovr == nil {
		s.routingQuality.recordRequest(now, nil)
		return
	}
	payload := ovr.payload
	rec := s.routingQuality.recordRequest(now, &payload)
	ovr.record = rec
}

// stampOutcomeOnRecord copies the post-execution outcome into the ring
// record stored on ovr. Safe to call once after the override event is
// emitted (the channel send-receive between runExecute and the fan-out
// goroutine establishes happens-before, so plain field writes are race-free
// here).
func stampOutcomeOnRecord(rec *routingQualityRecord, outcome *ServiceOverrideOutcome) {
	if rec == nil || outcome == nil || rec.override == nil {
		return
	}
	clone := *outcome
	rec.override.Outcome = &clone
}

func tokenSizeBucket(tokens *int) string {
	if tokens == nil {
		return "unknown"
	}
	t := *tokens
	switch {
	case t <= 0:
		return "unknown"
	case t < 1000:
		return "tiny"
	case t < 4000:
		return "small"
	case t < 16000:
		return "medium"
	case t < 64000:
		return "large"
	default:
		return "xlarge"
	}
}
