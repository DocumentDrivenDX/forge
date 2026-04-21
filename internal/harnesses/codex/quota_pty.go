package codex

import (
	"context"
	"fmt"
	"strings"
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

func ReadCodexQuotaViaPTY(timeout time.Duration, opts ...QuotaPTYOption) ([]harnesses.QuotaWindow, error) {
	windows, _, err := captureCodexQuotaViaPTY(context.Background(), timeout, opts...)
	return windows, err
}

func RefreshCodexQuotaViaPTY(timeout time.Duration, opts ...QuotaPTYOption) (CodexQuotaSnapshot, error) {
	windows, _, err := captureCodexQuotaViaPTY(context.Background(), timeout, opts...)
	if err != nil {
		return CodexQuotaSnapshot{}, err
	}
	return CodexQuotaSnapshot{
		CapturedAt: time.Now().UTC(),
		Windows:    windows,
		Source:     "pty",
	}, nil
}

func ReadCodexQuotaFromCassette(dir string) ([]harnesses.QuotaWindow, error) {
	reader, err := cassette.Open(dir)
	if err != nil {
		return nil, err
	}
	text := reader.Final().FinalText
	if text == "" {
		frames := reader.Frames()
		if len(frames) > 0 {
			text = strings.Join(frames[len(frames)-1].Text, "\n")
		}
	}
	windows := parseCodexStatusOutput(text)
	if len(windows) == 0 {
		return nil, fmt.Errorf("no quota windows found in codex quota cassette")
	}
	return windows, nil
}

func captureCodexQuotaViaPTY(ctx context.Context, timeout time.Duration, opts ...QuotaPTYOption) ([]harnesses.QuotaWindow, ptyquota.Result, error) {
	cfg := quotaPTYOptions{binary: "codex", args: []string{"--no-alt-screen"}}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	var windows []harnesses.QuotaWindow
	result, err := ptyquota.Run(ctx, ptyquota.Config{
		HarnessName:        "codex",
		Binary:             cfg.binary,
		Args:               cfg.args,
		Workdir:            cfg.workdir,
		Env:                cfg.env,
		Command:            "/status\r",
		ReadyMarkers:       []string{"›", "> "},
		DoneMarkers:        []string{"/status", "% left"},
		ResetBeforeCommand: true,
		Timeout:            timeout,
		Size:               session.Size{Rows: 50, Cols: 220},
		CassetteDir:        cfg.cassetteDir,
		Quota: func(text string) (cassette.QuotaRecord, error) {
			windows = parseCodexStatusOutput(text)
			if len(windows) == 0 {
				return cassette.QuotaRecord{}, fmt.Errorf("no quota windows found in codex /status output")
			}
			return quotaRecord(windows), nil
		},
	})
	if err != nil {
		return nil, result, err
	}
	if len(windows) == 0 {
		windows = parseCodexStatusOutput(result.Text)
	}
	if len(windows) == 0 {
		return nil, result, fmt.Errorf("no quota windows found in codex /status output")
	}
	return windows, result, nil
}

func quotaRecord(windows []harnesses.QuotaWindow) cassette.QuotaRecord {
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
	return cassette.QuotaRecord{Source: "pty", Status: string(ptyquota.StatusOK), Windows: records}
}
