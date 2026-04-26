package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/session"
)

// UsageReportOptions configures DdxAgent.UsageReport.
type UsageReportOptions struct {
	// Since limits the report window. Supported values include "today", "7d",
	// "30d", a date range "YYYY-MM-DD..YYYY-MM-DD", or a single start date.
	// Empty means no window filter (all sessions).
	Since string
	// Now is the reference time for relative windows. Zero means time.Now().
	Now time.Time
}

// UsageReport is the public, service-owned aggregation over historical session
// logs. CLI subcommands consume this projection rather than re-reading the
// session-log JSONL schema directly.
//
// RoutingQuality (ADR-006 §5) summarizes how often auto-routing produced an
// acceptable decision over the report window. It is intentionally distinct
// from the per-(provider, model) provider-reliability rate carried on each
// UsageReportRow (Row.SuccessRate); the two metrics measure different
// things and should be presented as separate numbers in operator UIs.
type UsageReport struct {
	Window         *UsageReportWindow    `json:"window,omitempty"`
	Rows           []UsageReportRow      `json:"rows"`
	Totals         UsageReportRow        `json:"totals"`
	RoutingQuality RoutingQualityMetrics `json:"routing_quality"`
}

// UsageReportWindow describes the active reporting window.
type UsageReportWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// UsageReportRow aggregates usage for one provider/model pair.
type UsageReportRow struct {
	Provider            string   `json:"provider"`
	Model               string   `json:"model"`
	Sessions            int      `json:"sessions"`
	SuccessSessions     int      `json:"success_sessions"`
	FailedSessions      int      `json:"failed_sessions"`
	InputTokens         int      `json:"input_tokens"`
	OutputTokens        int      `json:"output_tokens"`
	TotalTokens         int      `json:"total_tokens"`
	DurationMs          int64    `json:"duration_ms"`
	KnownCostUSD        *float64 `json:"known_cost_usd"`
	UnknownCostSessions int      `json:"unknown_cost_sessions"`
	CacheReadTokens     int      `json:"cache_read_tokens"`
	CacheWriteTokens    int      `json:"cache_write_tokens"`
}

// SuccessRate returns the fraction of sessions that succeeded (0 if no sessions).
func (r UsageReportRow) SuccessRate() float64 {
	if r.Sessions == 0 {
		return 0
	}
	return float64(r.SuccessSessions) / float64(r.Sessions)
}

// CostPerSuccess returns the known cost divided by successful sessions, or nil
// if there are no successes or cost is unknown.
func (r UsageReportRow) CostPerSuccess() *float64 {
	if r.SuccessSessions == 0 || r.KnownCostUSD == nil {
		return nil
	}
	v := *r.KnownCostUSD / float64(r.SuccessSessions)
	return &v
}

// InputTokensPerSecond returns the average input-token throughput.
func (r UsageReportRow) InputTokensPerSecond() float64 {
	if r.DurationMs <= 0 {
		return 0
	}
	return float64(r.InputTokens) / (float64(r.DurationMs) / 1000)
}

// OutputTokensPerSecond returns the average output-token throughput.
func (r UsageReportRow) OutputTokensPerSecond() float64 {
	if r.DurationMs <= 0 {
		return 0
	}
	return float64(r.OutputTokens) / (float64(r.DurationMs) / 1000)
}

// CacheHitRate returns the fraction of input tokens served from cache (0..1).
func (r UsageReportRow) CacheHitRate() float64 {
	total := r.InputTokens + r.CacheReadTokens + r.CacheWriteTokens
	if total == 0 {
		return 0
	}
	return float64(r.CacheReadTokens) / float64(total)
}

// SessionLogEntry describes one historical session log file projected from the
// service-owned session-log directory. Consumers display these without reading
// directory contents directly.
type SessionLogEntry struct {
	SessionID string    `json:"session_id"`
	ModTime   time.Time `json:"mod_time"`
	Size      int64     `json:"size"`
}

// ValidateUsageSince returns nil when spec is a usage window value accepted by
// UsageReport. CLI subcommands call this to surface exit-code-2 validation
// errors before invoking the service.
func ValidateUsageSince(spec string) error {
	_, err := session.ParseUsageWindow(strings.TrimSpace(spec), time.Now().UTC())
	return err
}

// UsageReport aggregates token, cost, and reliability totals across the
// service-owned session-log directory.
//
// RoutingQuality is sourced from persisted session logs, not the in-memory
// ring (ADR-006 §5): UsageReport's --since window can extend across
// restarts and beyond the ring's bounded retention, so the authoritative
// data source has to be the on-disk session-log corpus that already
// carries override and rejected_override events. When no log directory is
// configured, the in-memory ring is used as a best-effort fallback so
// in-process callers still see live routing-quality numbers.
func (s *service) UsageReport(ctx context.Context, opts UsageReportOptions) (*UsageReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	logDir := s.publicSessionLogDir()
	if logDir == "" {
		// AC bug-fix: even without a log directory, surface the
		// in-memory ring's live routing-quality so the field is never
		// silently dropped.
		report := &UsageReport{}
		report.RoutingQuality = s.routingQualityFromRing(nil)
		return report, nil
	}
	internal, err := session.AggregateUsage(logDir, session.UsageOptions{
		Since: opts.Since,
		Now:   now,
	})
	if err != nil {
		return nil, err
	}
	report := convertUsageReport(internal)
	rq, err := s.routingQualityFromSessionLogs(logDir, report.Window)
	if err != nil {
		return nil, err
	}
	report.RoutingQuality = rq
	return report, nil
}

// routingQualityFromSessionLogs is the windowed reader UsageReport calls
// to rebuild RoutingQualityMetrics from persisted override events. The
// session-log corpus is authoritative because it survives restarts and
// supports arbitrary --since windows; the in-memory ring is bounded and
// recent-only.
func (s *service) routingQualityFromSessionLogs(logDir string, window *UsageReportWindow) (RoutingQualityMetrics, error) {
	scanWindow := convertUsageWindow(window)
	scan, err := session.ScanRoutingQuality(logDir, scanWindow)
	if err != nil {
		return RoutingQualityMetrics{}, err
	}
	overrides := decodeRoutingQualityOverrides(scan.OverrideEvents)
	return computeRoutingQualityMetrics(scan.TotalRequests, overrides), nil
}

// routingQualityFromRing is the fallback path used when no session-log
// directory is configured: aggregate from the in-memory ring over window
// (nil = all records).
func (s *service) routingQualityFromRing(window *UsageReportWindow) RoutingQualityMetrics {
	if s == nil || s.routingQuality == nil {
		return RoutingQualityMetrics{}
	}
	var start, end time.Time
	if window != nil {
		start = window.Start
		end = window.End
	}
	records := s.routingQuality.snapshotWindow(start, end)
	return computeRoutingQualityMetricsFromRecords(records)
}

// decodeRoutingQualityOverrides converts persisted override / rejected_override
// session-log events back into ServiceOverrideData payloads. Events that
// fail to decode are skipped — a single corrupt record must not poison the
// whole report.
func decodeRoutingQualityOverrides(events []agentcore.Event) []ServiceOverrideData {
	if len(events) == 0 {
		return nil
	}
	out := make([]ServiceOverrideData, 0, len(events))
	for _, e := range events {
		var payload ServiceOverrideData
		if err := json.Unmarshal(e.Data, &payload); err != nil {
			continue
		}
		out = append(out, payload)
	}
	return out
}

func convertUsageWindow(w *UsageReportWindow) *session.UsageWindow {
	if w == nil {
		return nil
	}
	return &session.UsageWindow{Start: w.Start, End: w.End}
}

// ListSessionLogs returns the session log files known to the service, sorted
// alphabetically by session ID.
func (s *service) ListSessionLogs(ctx context.Context) ([]SessionLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logDir := s.publicSessionLogDir()
	if logDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil, err
	}
	out := make([]SessionLogEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		entry := SessionLogEntry{SessionID: id}
		if info, err := e.Info(); err == nil && info != nil {
			entry.ModTime = info.ModTime()
			entry.Size = info.Size()
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, nil
}

// WriteSessionLog renders every event in the named session log to w as
// indented JSON, one event per object. The format is service-owned; consumers
// do not parse it back into private session-log structs.
func (s *service) WriteSessionLog(ctx context.Context, sessionID string, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.sessionLogPath(sessionID)
	if err != nil {
		return err
	}
	events, err := session.ReadEvents(path)
	if err != nil {
		return err
	}
	for _, e := range events {
		data, err := json.MarshalIndent(e, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, string(data)); err != nil {
			return err
		}
	}
	return nil
}

// ReplaySession renders a human-readable conversation transcript for the named
// session log onto w.
func (s *service) ReplaySession(ctx context.Context, sessionID string, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.sessionLogPath(sessionID)
	if err != nil {
		return err
	}
	return session.Replay(path, w)
}

// publicSessionLogDir resolves the directory used by the public session-log
// projection methods. Resolution order:
//  1. ServiceOptions.SessionLogDir override.
//  2. ServiceConfig.WorkDir() + "/.agent/sessions" default.
//
// Returns "" when neither source supplies a directory.
func (s *service) publicSessionLogDir() string {
	if s == nil {
		return ""
	}
	if s.opts.SessionLogDir != "" {
		return s.opts.SessionLogDir
	}
	return s.serviceSessionLogDir()
}

func (s *service) sessionLogPath(sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	dir := s.publicSessionLogDir()
	if dir == "" {
		return "", fmt.Errorf("session log directory is not configured")
	}
	return filepath.Join(dir, sessionID+".jsonl"), nil
}

func convertUsageReport(in *session.UsageReport) *UsageReport {
	if in == nil {
		return &UsageReport{}
	}
	out := &UsageReport{
		Rows:   make([]UsageReportRow, 0, len(in.Rows)),
		Totals: convertUsageRow(in.Totals),
	}
	if in.Window != nil {
		out.Window = &UsageReportWindow{Start: in.Window.Start, End: in.Window.End}
	}
	for _, row := range in.Rows {
		out.Rows = append(out.Rows, convertUsageRow(row))
	}
	return out
}

func convertUsageRow(in session.UsageRow) UsageReportRow {
	row := UsageReportRow{
		Provider:            in.Provider,
		Model:               in.Model,
		Sessions:            in.Sessions,
		SuccessSessions:     in.SuccessSessions,
		FailedSessions:      in.FailedSessions,
		InputTokens:         in.InputTokens,
		OutputTokens:        in.OutputTokens,
		TotalTokens:         in.TotalTokens,
		DurationMs:          in.DurationMs,
		UnknownCostSessions: in.UnknownCostSessions,
		CacheReadTokens:     in.CacheReadTokens,
		CacheWriteTokens:    in.CacheWriteTokens,
	}
	if in.KnownCostUSD != nil {
		v := *in.KnownCostUSD
		row.KnownCostUSD = &v
	}
	return row
}
