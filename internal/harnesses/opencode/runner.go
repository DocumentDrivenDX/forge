package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

const defaultEventBuffer = 64

// Runner is the subprocess-backed opencode harness. It launches opencode in
// run --format json mode, parses the JSON output into harness Events, and
// emits a final Event when the subprocess exits.
//
// opencode run auto-approves all tool permissions; no extra flags are needed
// for any permission level.
type Runner struct {
	// Binary is the absolute path to the opencode executable. When empty the
	// runner resolves "opencode" via PATH at Execute time.
	Binary string

	// BaseArgs is prepended to the per-request argument list.
	// opencode default: ["run", "--format", "json"]
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
		Name:                 "opencode",
		Type:                 "subprocess",
		IsLocal:              false,
		IsSubscription:       false,
		ExactPinSupport:      true,
		SupportedPermissions: []string{"safe", "supervised", "unrestricted"},
		SupportedEfforts:     []string{"minimal", "low", "medium", "high", "max"},
		CostClass:            "medium",
	}
	path := r.Binary
	if path == "" {
		if resolved, err := osexec.LookPath("opencode"); err == nil {
			path = resolved
		}
	}
	if path != "" {
		info.Path = path
		info.Available = true
	} else {
		info.Error = "opencode binary not found in PATH"
	}
	return info
}

// HealthCheck verifies the opencode binary is present.
func (r *Runner) HealthCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := r.Binary
	if path == "" {
		resolved, err := osexec.LookPath("opencode")
		if err != nil {
			return fmt.Errorf("opencode binary not found: %w", err)
		}
		path = resolved
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat opencode binary: %w", err)
	}
	if st.IsDir() {
		return fmt.Errorf("opencode binary path is a directory: %s", path)
	}
	return nil
}

// Execute runs one resolved request through the opencode CLI and emits
// JSON-derived events on the returned channel.
func (r *Runner) Execute(ctx context.Context, req harnesses.ExecuteRequest) (<-chan harnesses.Event, error) {
	binary := r.Binary
	if binary == "" {
		resolved, err := osexec.LookPath("opencode")
		if err != nil {
			return nil, fmt.Errorf("opencode binary not found: %w", err)
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
		if agg.InputTokens > 0 || agg.OutputTokens > 0 {
			final.Usage = &harnesses.FinalUsage{
				InputTokens:  agg.InputTokens,
				OutputTokens: agg.OutputTokens,
				TotalTokens:  agg.InputTokens + agg.OutputTokens,
			}
		}
		if agg.CostUSD > 0 {
			final.CostUSD = agg.CostUSD
		}
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

func (r *Runner) runStreaming(ctx context.Context, binary string, req harnesses.ExecuteRequest, out chan<- harnesses.Event, seq *int64) (agg *streamAggregate, exitCode int, stderr string, runErr error, status string) {
	base := r.BaseArgs
	if base == nil {
		base = []string{"run", "--format", "json"}
	}
	args := append([]string{}, base...)

	// opencode run auto-approves all tool permissions; no extra flags per level.

	// WorkDir flag: --dir <dir>
	if req.WorkDir != "" {
		args = append(args, "--dir", req.WorkDir)
	}

	// Model flag: -m <model>
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}

	// Effort flag: --variant <effort>
	if req.Effort != "" {
		args = append(args, "--variant", req.Effort)
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

	cmd := osexec.CommandContext(runCtx, binary, args...)
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
		if err := os.MkdirAll(req.SessionLogDir, 0o755); err == nil {
			sid := req.SessionID
			if sid == "" {
				sid = fmt.Sprintf("opencode-%d", time.Now().UnixNano())
			}
			logPath := filepath.Join(req.SessionLogDir, "agent-"+sid+".jsonl")
			if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				progressLog = f
				defer progressLog.Close()
			}
		}
	}

	parserReader, parserWriter := io.Pipe()
	parseDone := make(chan struct{})
	var parseAgg *streamAggregate
	var parseErr error
	go func() {
		defer close(parseDone)
		mirrored := mirroredEvents(out, progressLog, ctx)
		parseAgg, parseErr = parseOpencodeStream(runCtx, parserReader, mirrored, req.Metadata, seq)
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
