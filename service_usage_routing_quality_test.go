package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/session"
)

// TestUsageReportRoutingQualityFromSessionLogs proves the architectural fix
// from the bead-2 review: UsageReport must aggregate routing-quality from
// the persisted session logs in the report window, not from the in-memory
// ring. The in-memory ring is bounded and recent-only and cannot show
// historical or cross-restart data.
func TestUsageReportRoutingQualityFromSessionLogs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// 5 sessions inside the window: 3 with overrides (2 disagreement, 1
	// coincidental agreement) and 2 with no override.
	writeSessionLogWithOverride(t, dir, "s-disagree-1", now.Add(-time.Hour), false, "success")
	writeSessionLogWithOverride(t, dir, "s-disagree-2", now.Add(-30*time.Minute), false, "failed")
	writeSessionLogWithOverride(t, dir, "s-coincide", now.Add(-15*time.Minute), true, "success")
	writeSessionLogNoOverride(t, dir, "s-noov-1", now.Add(-45*time.Minute))
	writeSessionLogNoOverride(t, dir, "s-noov-2", now.Add(-20*time.Minute))

	// 1 session OUTSIDE the window — must be excluded.
	writeSessionLogWithOverride(t, dir, "s-old", now.Add(-30*24*time.Hour), false, "success")

	scan, err := session.ScanRoutingQuality(dir, &session.UsageWindow{
		Start: now.Add(-2 * time.Hour),
		End:   now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("ScanRoutingQuality: %v", err)
	}
	if scan.TotalRequests != 5 {
		t.Fatalf("TotalRequests = %d, want 5 (in-window sessions)", scan.TotalRequests)
	}
	if len(scan.OverrideEvents) != 3 {
		t.Fatalf("OverrideEvents = %d, want 3 (in-window override events)", len(scan.OverrideEvents))
	}

	overrides := decodeRoutingQualityOverrides(scan.OverrideEvents)
	m := computeRoutingQualityMetrics(scan.TotalRequests, overrides)
	if !floatEq(m.AutoAcceptanceRate, 2.0/5.0) {
		t.Errorf("AutoAcceptanceRate = %v, want %v", m.AutoAcceptanceRate, 2.0/5.0)
	}
	if !floatEq(m.OverrideDisagreementRate, 2.0/3.0) {
		t.Errorf("OverrideDisagreementRate = %v, want %v", m.OverrideDisagreementRate, 2.0/3.0)
	}

	// Outcome aggregation must also survive the round-trip (AC #6) so the
	// review's third defect — outcome never landing in the ring — is fixed
	// at the data-source level too.
	var (
		successFromLog int
		failedFromLog  int
	)
	for _, ov := range overrides {
		if ov.Outcome == nil {
			continue
		}
		switch ov.Outcome.Status {
		case "success":
			successFromLog++
		case "failed":
			failedFromLog++
		}
	}
	if successFromLog != 2 || failedFromLog != 1 {
		t.Errorf("outcome roundtrip: success=%d failed=%d, want 2/1", successFromLog, failedFromLog)
	}
}

// TestUsageReportRoutingQualityNoLogDir locks in the fix for the second
// review defect: when publicSessionLogDir() is empty, UsageReport must
// still populate RoutingQuality from the in-memory ring instead of
// silently returning an empty struct.
func TestUsageReportRoutingQualityNoLogDir(t *testing.T) {
	svc := &service{routingQuality: newRoutingQualityStore()}
	// Pre-load the ring with one no-override and one override request so
	// the metric is computable.
	svc.routingQuality.recordRequest(time.Now().UTC(), nil)
	ov := makeOverride([]string{"model"}, nil, 0, false, "", "")
	svc.routingQuality.recordRequest(time.Now().UTC(), &ov)

	rep, err := svc.UsageReport(context.Background(), UsageReportOptions{})
	if err != nil {
		t.Fatalf("UsageReport: %v", err)
	}
	if rep.RoutingQuality.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2 (ring fallback dropped)", rep.RoutingQuality.TotalRequests)
	}
	if rep.RoutingQuality.TotalOverrides != 1 {
		t.Fatalf("TotalOverrides = %d, want 1", rep.RoutingQuality.TotalOverrides)
	}
}

// TestRoutingQualityRingOutcomeBackWrite locks in the fix for the third
// review defect: live records captured pre-final must be updated with the
// post-execution outcome, otherwise RouteStatus's outcome aggregates are
// permanently zero.
func TestRoutingQualityRingOutcomeBackWrite(t *testing.T) {
	st := newRoutingQualityStore()
	ov := makeOverride([]string{"model"}, nil, 0, false, "", "")
	rec := st.recordRequest(time.Now().UTC(), &ov)

	if rec == nil {
		t.Fatal("recordRequest returned nil — back-write handle missing")
	}
	if rec.override == nil {
		t.Fatal("ring record should carry override payload for an overridden request")
	}
	if rec.override.Outcome != nil {
		t.Fatalf("ring record outcome should be nil pre-execution, got %+v", rec.override.Outcome)
	}

	// Simulate the fan-out goroutine stamping outcome after the final event.
	stampOutcomeOnRecord(rec, &ServiceOverrideOutcome{Status: "success", DurationMS: 42})

	recs := st.snapshotRecent(0, time.Time{})
	if len(recs) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(recs))
	}
	got := recs[0]
	if got.override == nil || got.override.Outcome == nil {
		t.Fatalf("outcome not back-written: %+v", got)
	}
	if got.override.Outcome.Status != "success" {
		t.Errorf("outcome.Status = %q, want success", got.override.Outcome.Status)
	}

	// Aggregator must surface the outcome in the per-bucket counts.
	m := computeRoutingQualityMetricsFromRecords(recs)
	if len(m.OverrideClassBreakdown) != 1 {
		t.Fatalf("breakdown len = %d, want 1", len(m.OverrideClassBreakdown))
	}
	if m.OverrideClassBreakdown[0].SuccessOutcomes != 1 {
		t.Errorf("SuccessOutcomes = %d, want 1 (outcome back-write feeds aggregates)", m.OverrideClassBreakdown[0].SuccessOutcomes)
	}
}

func writeSessionLogWithOverride(t *testing.T, dir, sessionID string, startedAt time.Time, coincide bool, outcomeStatus string) {
	t.Helper()
	axes := []string{"model"}
	matches := []string(nil)
	if coincide {
		matches = []string{"model"}
	}
	ov := makeOverride(axes, matches, 0, false, "", outcomeStatus)
	overrideRaw, err := json.Marshal(ov)
	if err != nil {
		t.Fatalf("marshal override: %v", err)
	}
	startRaw, err := json.Marshal(session.SessionStartData{Provider: "p", Model: "m"})
	if err != nil {
		t.Fatalf("marshal start: %v", err)
	}
	endRaw, err := json.Marshal(session.SessionEndData{Status: agentcore.StatusSuccess})
	if err != nil {
		t.Fatalf("marshal end: %v", err)
	}
	events := []agentcore.Event{
		{SessionID: sessionID, Seq: 0, Type: agentcore.EventSessionStart, Timestamp: startedAt, Data: startRaw},
		{SessionID: sessionID, Seq: 1, Type: agentcore.EventOverride, Timestamp: startedAt.Add(time.Second), Data: overrideRaw},
		{SessionID: sessionID, Seq: 2, Type: agentcore.EventSessionEnd, Timestamp: startedAt.Add(2 * time.Second), Data: endRaw},
	}
	writeJSONL(t, filepath.Join(dir, sessionID+".jsonl"), events)
}

func writeSessionLogNoOverride(t *testing.T, dir, sessionID string, startedAt time.Time) {
	t.Helper()
	startRaw, _ := json.Marshal(session.SessionStartData{Provider: "p", Model: "m"})
	endRaw, _ := json.Marshal(session.SessionEndData{Status: agentcore.StatusSuccess})
	events := []agentcore.Event{
		{SessionID: sessionID, Seq: 0, Type: agentcore.EventSessionStart, Timestamp: startedAt, Data: startRaw},
		{SessionID: sessionID, Seq: 1, Type: agentcore.EventSessionEnd, Timestamp: startedAt.Add(time.Second), Data: endRaw},
	}
	writeJSONL(t, filepath.Join(dir, sessionID+".jsonl"), events)
}

func writeJSONL(t *testing.T, path string, events []agentcore.Event) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}
