package session

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
)

// RoutingQualityScan is the result of walking session logs in a directory
// to recover routing-quality inputs (ADR-006 §5). TotalRequests counts
// session.start events whose timestamp falls within window; OverrideEvents
// holds the raw EventOverride / EventRejectedOverride records, decoding
// of which is the agent package's responsibility (the payload type lives
// there).
type RoutingQualityScan struct {
	TotalRequests  int
	OverrideEvents []agent.Event
}

// ScanRoutingQuality walks every .jsonl session log in logDir and produces
// the inputs needed to recompute routing-quality metrics over window.
//
// The scan is the authoritative data source for windowed reporting because
// the in-memory ring is bounded and recent-only — it cannot produce
// historical, cross-restart, or window-bounded views without persisted
// session logs.
//
// A nil window means "no time filter" (include every session). Sessions
// without a session.start event are skipped (incomplete logs).
func ScanRoutingQuality(logDir string, window *UsageWindow) (*RoutingQualityScan, error) {
	scan := &RoutingQualityScan{}
	if logDir == "" {
		return scan, nil
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return scan, nil
		}
		return nil, fmt.Errorf("routing-quality: reading session log dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(logDir, entry.Name())
		events, err := ReadEvents(path)
		if err != nil {
			return nil, fmt.Errorf("routing-quality: reading %s: %w", path, err)
		}
		var startedAt time.Time
		var haveStart bool
		var fileOverrides []agent.Event
		for _, e := range events {
			switch e.Type {
			case agent.EventSessionStart:
				if !haveStart {
					startedAt = e.Timestamp.UTC()
					haveStart = true
				}
			case agent.EventOverride, agent.EventRejectedOverride:
				fileOverrides = append(fileOverrides, e)
			}
		}
		if !haveStart {
			continue
		}
		if window != nil && !window.Contains(startedAt) {
			continue
		}
		scan.TotalRequests++
		scan.OverrideEvents = append(scan.OverrideEvents, fileOverrides...)
	}
	return scan, nil
}
