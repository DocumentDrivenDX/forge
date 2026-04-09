package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent"
)

// UsageOptions configures how session logs are aggregated.
type UsageOptions struct {
	// Since limits the report window. Supported values include today, 7d, 30d,
	// a date range (YYYY-MM-DD..YYYY-MM-DD), or a single start date.
	Since string

	// Now is the reference time for relative windows. Zero means time.Now().
	Now time.Time
}

// UsageWindow describes the active reporting window.
type UsageWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Contains reports whether ts falls within the half-open usage window.
func (w UsageWindow) Contains(ts time.Time) bool {
	ts = ts.UTC()
	if !w.Start.IsZero() && ts.Before(w.Start) {
		return false
	}
	if !w.End.IsZero() && !ts.Before(w.End) {
		return false
	}
	return true
}

// UsageRow aggregates usage for one provider/model pair.
type UsageRow struct {
	Provider            string  `json:"provider"`
	Model               string  `json:"model"`
	Sessions            int     `json:"sessions"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	TotalTokens         int     `json:"total_tokens"`
	DurationMs          int64   `json:"duration_ms"`
	KnownCostUSD        float64 `json:"known_cost_usd"`
	UnknownCostSessions int     `json:"unknown_cost_sessions"`
}

// InputTokensPerSecond returns the average input-token throughput.
func (r UsageRow) InputTokensPerSecond() float64 {
	if r.DurationMs <= 0 {
		return 0
	}
	return float64(r.InputTokens) / (float64(r.DurationMs) / 1000)
}

// OutputTokensPerSecond returns the average output-token throughput.
func (r UsageRow) OutputTokensPerSecond() float64 {
	if r.DurationMs <= 0 {
		return 0
	}
	return float64(r.OutputTokens) / (float64(r.DurationMs) / 1000)
}

// UsageReport is the aggregate output for a session log scan.
type UsageReport struct {
	Window *UsageWindow `json:"window,omitempty"`
	Rows   []UsageRow   `json:"rows"`
	Totals UsageRow     `json:"totals"`
}

type usageSession struct {
	Provider    string
	Model       string
	StartedAt   time.Time
	EndedAt     time.Time
	DurationMs  int64
	Tokens      agent.TokenUsage
	KnownCost   float64
	UnknownCost bool
}

// AggregateUsage scans JSONL session logs in logDir and aggregates token and
// cost totals by provider/model.
func AggregateUsage(logDir string, opts UsageOptions) (*UsageReport, error) {
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	window, err := ParseUsageWindow(opts.Since, now)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &UsageReport{Window: window}, nil
		}
		return nil, fmt.Errorf("usage: reading session log dir: %w", err)
	}

	rows := make(map[string]*UsageRow)
	report := &UsageReport{Window: window}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}

		path := filepath.Join(logDir, entry.Name())
		events, err := ReadEvents(path)
		if err != nil {
			return nil, fmt.Errorf("usage: reading %s: %w", path, err)
		}

		session, ok := summarizeUsageSession(events)
		if !ok {
			continue
		}
		if window != nil && !window.Contains(session.StartedAt) {
			continue
		}

		key := session.Provider + "\x00" + session.Model
		row, ok := rows[key]
		if !ok {
			row = &UsageRow{Provider: session.Provider, Model: session.Model}
			rows[key] = row
		}

		row.Sessions++
		row.InputTokens += session.Tokens.Input
		row.OutputTokens += session.Tokens.Output
		row.TotalTokens += effectiveTotalTokens(session.Tokens)
		row.DurationMs += session.DurationMs
		if session.UnknownCost {
			row.UnknownCostSessions++
		} else {
			row.KnownCostUSD += session.KnownCost
		}

		report.Totals.Sessions++
		report.Totals.InputTokens += session.Tokens.Input
		report.Totals.OutputTokens += session.Tokens.Output
		report.Totals.TotalTokens += effectiveTotalTokens(session.Tokens)
		report.Totals.DurationMs += session.DurationMs
		if session.UnknownCost {
			report.Totals.UnknownCostSessions++
		} else {
			report.Totals.KnownCostUSD += session.KnownCost
		}
	}

	report.Rows = make([]UsageRow, 0, len(rows))
	for _, row := range rows {
		report.Rows = append(report.Rows, *row)
	}

	sort.Slice(report.Rows, func(i, j int) bool {
		if report.Rows[i].Provider != report.Rows[j].Provider {
			return report.Rows[i].Provider < report.Rows[j].Provider
		}
		return report.Rows[i].Model < report.Rows[j].Model
	})

	return report, nil
}

// ParseUsageWindow parses the --since value into a UTC time window.
func ParseUsageWindow(spec string, now time.Time) (*UsageWindow, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}

	now = now.UTC()
	switch spec {
	case "today":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		end := start.Add(24 * time.Hour)
		return &UsageWindow{Start: start, End: end}, nil
	case "7d":
		return &UsageWindow{Start: now.Add(-7 * 24 * time.Hour), End: now}, nil
	case "30d":
		return &UsageWindow{Start: now.Add(-30 * 24 * time.Hour), End: now}, nil
	}

	if strings.Contains(spec, "..") {
		parts := strings.SplitN(spec, "..", 2)
		start, err := parseUsageWindowEndpoint(parts[0], false)
		if err != nil {
			return nil, err
		}
		end, err := parseUsageWindowEndpoint(parts[1], true)
		if err != nil {
			return nil, err
		}
		if start != nil && end != nil && !start.Before(*end) {
			return nil, fmt.Errorf("usage: invalid time window %q", spec)
		}
		if start == nil && end == nil {
			return nil, fmt.Errorf("usage: invalid time window %q", spec)
		}
		window := &UsageWindow{}
		if start != nil {
			window.Start = start.UTC()
		}
		if end != nil {
			window.End = end.UTC()
		}
		return window, nil
	}

	start, err := parseUsageWindowEndpoint(spec, false)
	if err != nil {
		return nil, err
	}
	if start == nil {
		return nil, fmt.Errorf("usage: invalid time window %q", spec)
	}
	end := start.UTC().Add(24 * time.Hour)
	return &UsageWindow{Start: start.UTC(), End: end}, nil
}

func parseUsageWindowEndpoint(spec string, endExclusive bool) (*time.Time, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}

	if ts, err := time.Parse(time.RFC3339, spec); err == nil {
		ts = ts.UTC()
		return &ts, nil
	}

	if d, err := time.Parse("2006-01-02", spec); err == nil {
		d = d.UTC()
		if endExclusive {
			d = d.Add(24 * time.Hour)
		}
		return &d, nil
	}

	return nil, fmt.Errorf("usage: invalid time window endpoint %q", spec)
}

func summarizeUsageSession(events []agent.Event) (usageSession, bool) {
	var result usageSession
	var haveStart, haveEnd bool

	for _, e := range events {
		switch e.Type {
		case agent.EventSessionStart:
			if haveStart {
				continue
			}
			var data SessionStartData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				continue
			}
			result.Provider = data.Provider
			result.Model = data.Model
			result.StartedAt = e.Timestamp.UTC()
			haveStart = true

		case agent.EventSessionEnd:
			var data SessionEndData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				continue
			}
			result.EndedAt = e.Timestamp.UTC()
			result.DurationMs = data.DurationMs
			result.Tokens = data.Tokens
			if data.CostUSD == nil || *data.CostUSD < 0 {
				result.UnknownCost = true
				result.KnownCost = 0
			} else {
				result.UnknownCost = false
				result.KnownCost = *data.CostUSD
			}
			if result.Model == "" {
				result.Model = data.Model
			}
			haveEnd = true
		}
	}

	if !haveStart || !haveEnd {
		return usageSession{}, false
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	if result.Model == "" {
		result.Model = "unknown"
	}
	if result.DurationMs <= 0 && !result.StartedAt.IsZero() && !result.EndedAt.IsZero() {
		result.DurationMs = int64(result.EndedAt.Sub(result.StartedAt) / time.Millisecond)
	}
	return result, true
}

func effectiveTotalTokens(usage agent.TokenUsage) int {
	if usage.Total > 0 {
		return usage.Total
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}
