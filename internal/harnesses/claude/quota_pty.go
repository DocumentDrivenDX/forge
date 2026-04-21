package claude

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	"github.com/DocumentDrivenDX/agent/internal/harnesses/ptyquota"
	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
)

type quotaPTYOptions struct {
	binary      string
	args        []string
	workdir     string
	env         []string
	cassetteDir string
}

type QuotaPTYOption func(*quotaPTYOptions)

func WithQuotaPTYCommand(binary string, args ...string) QuotaPTYOption {
	return func(opts *quotaPTYOptions) {
		opts.binary = binary
		opts.args = append([]string(nil), args...)
	}
}

func WithQuotaPTYWorkdir(workdir string) QuotaPTYOption {
	return func(opts *quotaPTYOptions) {
		opts.workdir = workdir
	}
}

func WithQuotaPTYEnv(env ...string) QuotaPTYOption {
	return func(opts *quotaPTYOptions) {
		opts.env = append([]string(nil), env...)
	}
}

func WithQuotaPTYCassetteDir(dir string) QuotaPTYOption {
	return func(opts *quotaPTYOptions) {
		opts.cassetteDir = dir
	}
}

func ReadClaudeQuotaViaPTY(timeout time.Duration, opts ...QuotaPTYOption) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, error) {
	windows, account, _, err := captureClaudeQuotaViaPTY(context.Background(), timeout, opts...)
	return windows, account, err
}

func RefreshClaudeQuotaViaPTY(timeout time.Duration, opts ...QuotaPTYOption) (ClaudeQuotaSnapshot, error) {
	windows, account, _, err := captureClaudeQuotaViaPTY(context.Background(), timeout, opts...)
	if err != nil {
		return ClaudeQuotaSnapshot{}, err
	}
	return claudeQuotaSnapshotFromWindows(windows, account), nil
}

func ReadClaudeQuotaFromCassette(dir string) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, error) {
	reader, err := cassette.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	text := reader.Final().FinalText
	if text == "" {
		frames := reader.Frames()
		if len(frames) > 0 {
			text = stringsJoinLines(frames[len(frames)-1].Text)
		}
	}
	windows, account := parseClaudeUsageOutput(text)
	if len(windows) == 0 {
		return nil, account, fmt.Errorf("no quota windows found in claude quota cassette")
	}
	return windows, account, nil
}

func captureClaudeQuotaViaPTY(ctx context.Context, timeout time.Duration, opts ...QuotaPTYOption) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, ptyquota.Result, error) {
	cfg := quotaPTYOptions{binary: "claude"}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	var windows []harnesses.QuotaWindow
	var account *harnesses.AccountInfo
	result, err := ptyquota.Run(ctx, ptyquota.Config{
		HarnessName:  "claude",
		Binary:       cfg.binary,
		Args:         cfg.args,
		Workdir:      cfg.workdir,
		Env:          cfg.env,
		Command:      "/usage\r",
		ReadyMarkers: []string{"❯", "> "},
		DoneWhen:     claudeUsageComplete,
		Timeout:      timeout,
		Size:         session.Size{Rows: 50, Cols: 220},
		CassetteDir:  cfg.cassetteDir,
		Quota: func(text string) (cassette.QuotaRecord, error) {
			windows, account = parseClaudeUsageOutput(text)
			if len(windows) == 0 {
				return cassette.QuotaRecord{}, fmt.Errorf("no quota windows found in claude /usage output")
			}
			if !hasQuotaWindow(windows, "session") || (!hasQuotaWindow(windows, "weekly-all") && !hasQuotaWindow(windows, "weekly-sonnet")) {
				return cassette.QuotaRecord{}, fmt.Errorf("incomplete claude /usage output")
			}
			return quotaRecord(windows, map[string]any{"plan_type": accountPlan(account)}), nil
		},
	})
	if err != nil {
		return nil, nil, result, err
	}
	if len(windows) == 0 {
		windows, account = parseClaudeUsageOutput(result.Text)
	}
	if len(windows) == 0 {
		return nil, account, result, fmt.Errorf("no quota windows found in claude /usage output")
	}
	return windows, account, result, nil
}

func claudeQuotaSnapshotFromWindows(windows []harnesses.QuotaWindow, account *harnesses.AccountInfo) ClaudeQuotaSnapshot {
	fiveHourUsed := usedPercentFor(windows, "session")
	weeklyUsed := usedPercentFor(windows, "weekly-all")
	if weeklyUsed < 0 {
		weeklyUsed = usedPercentFor(windows, "weekly-sonnet")
	}
	return ClaudeQuotaSnapshot{
		CapturedAt:        time.Now().UTC(),
		FiveHourLimit:     100,
		FiveHourRemaining: remainingPercent(fiveHourUsed),
		WeeklyLimit:       100,
		WeeklyRemaining:   remainingPercent(weeklyUsed),
		Source:            "pty",
		Account:           account,
	}
}

func usedPercentFor(windows []harnesses.QuotaWindow, limitID string) float64 {
	for _, window := range windows {
		if window.LimitID == limitID {
			return window.UsedPercent
		}
	}
	return -1
}

func hasQuotaWindow(windows []harnesses.QuotaWindow, limitID string) bool {
	return usedPercentFor(windows, limitID) >= 0
}

func claudeUsageComplete(text string) bool {
	windows, _ := parseClaudeUsageOutput(text)
	return hasQuotaWindow(windows, "session") && (hasQuotaWindow(windows, "weekly-all") || hasQuotaWindow(windows, "weekly-sonnet"))
}

func remainingPercent(used float64) int {
	if used < 0 {
		return 0
	}
	remaining := int(math.Round(100 - used))
	if remaining < 0 {
		return 0
	}
	if remaining > 100 {
		return 100
	}
	return remaining
}

func quotaRecord(windows []harnesses.QuotaWindow, metadata map[string]any) cassette.QuotaRecord {
	records := make([]map[string]any, 0, len(windows))
	for _, window := range windows {
		records = append(records, map[string]any{
			"name":           window.Name,
			"limit_id":       window.LimitID,
			"window_minutes": window.WindowMinutes,
			"used_percent":   window.UsedPercent,
			"resets_at":      window.ResetsAt,
			"state":          window.State,
		})
	}
	return cassette.QuotaRecord{Source: "pty", Status: string(ptyquota.StatusOK), Windows: records, Metadata: metadata}
}

func accountPlan(account *harnesses.AccountInfo) string {
	if account == nil {
		return ""
	}
	return account.PlanType
}

func stringsJoinLines(lines []string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}
