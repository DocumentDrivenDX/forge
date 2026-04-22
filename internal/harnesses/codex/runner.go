package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	"github.com/DocumentDrivenDX/agent/internal/sessionlog"
)

const defaultEventBuffer = 64

// Runner is the subprocess-backed codex harness. It launches codex in
// exec --json mode, parses each JSONL line into harness Events, and emits
// a final Event when the subprocess exits.
type Runner struct {
	// Binary is the absolute path to the codex executable. When empty the
	// runner resolves "codex" via PATH at Execute time.
	Binary string

	// BaseArgs is prepended to the per-request argument list.
	// Codex default: ["exec", "--json"]
	BaseArgs []string

	// PromptMode controls how the prompt is delivered:
	//   "arg" (default) — prompt is appended as the final positional argument
	//   "stdin"         — prompt is piped on stdin
	PromptMode string

	// EventBuffer overrides the per-Execute channel buffer size.
	EventBuffer int
}

// Info returns identity + capability metadata for this harness.
func (r *Runner) Info() harnesses.HarnessInfo {
	info := harnesses.HarnessInfo{
		Name:                 "codex",
		Type:                 "subprocess",
		IsLocal:              false,
		IsSubscription:       true,
		AutoRoutingEligible:  true,
		ExactPinSupport:      true,
		DefaultModel:         "gpt-5.4",
		SupportedPermissions: []string{"safe", "supervised", "unrestricted"},
		SupportedReasoning:   []string{"low", "medium", "high", "xhigh", "max"},
		CostClass:            "medium",
	}
	path := r.Binary
	if path == "" {
		if resolved, err := osexec.LookPath("codex"); err == nil {
			path = resolved
		}
	}
	if path != "" {
		info.Path = path
		info.Available = true
	} else {
		info.Error = "codex binary not found in PATH"
	}
	return info
}

// HealthCheck verifies the codex binary is present.
func (r *Runner) HealthCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := r.Binary
	if path == "" {
		resolved, err := osexec.LookPath("codex")
		if err != nil {
			return fmt.Errorf("codex binary not found: %w", err)
		}
		path = resolved
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat codex binary: %w", err)
	}
	if st.IsDir() {
		return fmt.Errorf("codex binary path is a directory: %s", path)
	}
	return nil
}

// Execute runs one resolved request through the codex CLI and emits
// JSONL-derived events on the returned channel.
func (r *Runner) Execute(ctx context.Context, req harnesses.ExecuteRequest) (<-chan harnesses.Event, error) {
	binary := r.Binary
	if binary == "" {
		resolved, err := osexec.LookPath("codex")
		if err != nil {
			return nil, fmt.Errorf("codex binary not found: %w", err)
		}
		binary = resolved
	}

	bufSize := r.EventBuffer
	if bufSize <= 0 {
		bufSize = defaultEventBuffer
	}

	out := make(chan harnesses.Event, bufSize)
	go r.run(ctx, binary, req, out)
	return out, nil
}

func (r *Runner) run(ctx context.Context, binary string, req harnesses.ExecuteRequest, out chan<- harnesses.Event) {
	defer close(out)

	start := time.Now()
	var seq int64

	agg, exitCode, stderr, runErr, status := r.runStreaming(ctx, binary, req, out, &seq)

	final := harnesses.FinalData{
		Status:     status,
		ExitCode:   exitCode,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if runErr != nil && status != "success" {
		final.Error = runErr.Error()
	} else if stderr != "" && status != "success" {
		final.Error = trimErrorBlob(stderr)
	}
	if agg != nil {
		final.FinalText = agg.FinalText
		final.Usage, final.Warnings = harnesses.ResolveFinalUsage(agg.UsageSources)
		agg.writeTokenCountQuotaCache()
	}

	finalRaw, err := json.Marshal(final)
	if err != nil {
		finalRaw = []byte(`{"status":"failed","error":"marshal final event"}`)
	}
	ev := harnesses.Event{
		Type:     harnesses.EventTypeFinal,
		Sequence: seq,
		Time:     time.Now().UTC(),
		Metadata: req.Metadata,
		Data:     finalRaw,
	}
	select {
	case out <- ev:
	case <-time.After(time.Second):
	}
}

func (a *streamAggregate) writeTokenCountQuotaCache() {
	if a == nil || len(a.TokenCountRateLimits) == 0 {
		return
	}
	var newest *CodexQuotaSnapshot
	for _, evidence := range a.TokenCountRateLimits {
		fallback := time.Now().UTC()
		snapshot, ok := CodexQuotaSnapshotFromTokenCountRateLimits(evidence.CapturedAt, fallback, evidence.RateLimits)
		if !ok {
			continue
		}
		snapshot.Source = "codex_exec_token_count"
		if newest == nil || snapshot.CapturedAt.After(newest.CapturedAt) {
			newest = snapshot
		}
	}
	if newest == nil {
		return
	}
	path, err := CodexQuotaCachePath()
	if err != nil {
		return
	}
	_ = WriteCodexQuota(path, *newest)
}

func (r *Runner) runStreaming(ctx context.Context, binary string, req harnesses.ExecuteRequest, out chan<- harnesses.Event, seq *int64) (agg *streamAggregate, exitCode int, stderr string, runErr error, status string) {
	base := r.BaseArgs
	if base == nil {
		base = []string{"exec", "--json"}
	}
	args := append([]string{}, base...)

	// Permission args: unrestricted adds --dangerously-bypass-approvals-and-sandbox
	if req.Permissions == "unrestricted" {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}

	// WorkDir flag: -C <dir>
	if req.WorkDir != "" {
		args = append(args, "-C", req.WorkDir)
	}

	// Model flag: -m <model>
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}

	// Reasoning flag: -c reasoning.effort=<reasoning>
	if value := harnesses.AdapterReasoningValue(req); value != "" {
		args = append(args, "-c", fmt.Sprintf("reasoning.effort=%s", value))
	}

	promptMode := r.PromptMode
	if promptMode == "" {
		promptMode = "arg"
	}
	if promptMode == "arg" && req.Prompt != "" {
		args = append(args, req.Prompt)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := harnesses.HarnessCommand(runCtx, binary, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if promptMode != "arg" {
		cmd.Stdin = strings.NewReader(req.Prompt)
	}
	setProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, -1, "", err, "failed"
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, -1, "", err, "failed"
	}

	if err := cmd.Start(); err != nil {
		return nil, -1, "", err, "failed"
	}

	var progressLog *os.File
	if req.SessionLogDir != "" {
		sid := req.SessionID
		if sid == "" {
			sid = fmt.Sprintf("codex-%d", time.Now().UnixNano())
		}
		if f, err := sessionlog.OpenAppend(req.SessionLogDir, sid); err == nil {
			progressLog = f
			defer progressLog.Close()
		}
	}

	parserReader, parserWriter := io.Pipe()
	parseDone := make(chan struct{})
	var parseAgg *streamAggregate
	var parseErr error
	go func() {
		defer close(parseDone)
		mirrored := mirroredEvents(out, progressLog, ctx)
		parseAgg, parseErr = parseCodexStream(runCtx, parserReader, mirrored, req.Metadata, seq)
	}()

	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		_, _ = io.Copy(parserWriter, stdoutPipe)
		_ = parserWriter.Close()
	}()

	var stderrBuf strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&stringBuilderWriter{&stderrBuf}, stderrPipe)
	}()

	var timedOut bool
	if req.Timeout > 0 {
		stop := make(chan struct{})
		go func() {
			select {
			case <-stop:
			case <-time.After(req.Timeout):
				timedOut = true
				cancel()
			}
		}()
		defer close(stop)
	}

	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-stdoutDone:
		}
	}()

	<-stdoutDone
	<-stderrDone
	<-parseDone
	runErr = cmd.Wait()
	<-cancelDone
	stderr = stderrBuf.String()

	switch {
	case timedOut:
		return parseAgg, -1, stderr, context.DeadlineExceeded, "timed_out"
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled):
		return parseAgg, -1, stderr, ctx.Err(), "cancelled"
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return parseAgg, -1, stderr, ctx.Err(), "timed_out"
	case runErr != nil:
		ec := -1
		var exitErr *osexec.ExitError
		if errors.As(runErr, &exitErr) {
			ec = exitErr.ExitCode()
		}
		return parseAgg, ec, stderr, runErr, "failed"
	}
	if parseErr != nil && !errors.Is(parseErr, context.Canceled) {
		return parseAgg, 0, stderr, parseErr, "failed"
	}
	return parseAgg, 0, stderr, nil, "success"
}

type stringBuilderWriter struct {
	sb *strings.Builder
}

func (w *stringBuilderWriter) Write(p []byte) (int, error) {
	return w.sb.Write(p)
}

func mirroredEvents(dst chan<- harnesses.Event, log *os.File, ctx context.Context) chan<- harnesses.Event {
	if log == nil {
		return dst
	}
	mid := make(chan harnesses.Event, cap(dst))
	go func() {
		for ev := range mid {
			if data, err := json.Marshal(ev); err == nil {
				_, _ = log.Write(data)
				_, _ = log.Write([]byte("\n"))
			}
			select {
			case dst <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return mid
}

func trimErrorBlob(s string) string {
	const max = 2048
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "...(truncated)"
	}
	return s
}

func setProcessGroup(cmd *osexec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	setProcessGroupAttr(cmd.SysProcAttr)
}
