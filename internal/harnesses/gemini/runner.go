package gemini

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

// Runner is the subprocess-backed gemini harness. It launches gemini with
// the prompt on stdin (PromptMode=stdin), buffers stdout, and emits a
// single text_delta + final event. Gemini does not have a stream-json mode,
// so this is an emit-on-EOF implementation.
//
// When the output is valid JSON with a stats.models token block, usage is
// extracted per the DDx ExtractUsage("gemini", ...) shape.
type Runner struct {
	// Binary is the absolute path to the gemini executable. When empty the
	// runner resolves "gemini" via PATH at Execute time.
	Binary string

	// BaseArgs is prepended to the per-request argument list.
	// Gemini default: [] (no base args)
	BaseArgs []string

	// PromptMode controls how the prompt is delivered.
	// Gemini uses "stdin" (default).
	PromptMode string

	// EventBuffer overrides the per-Execute channel buffer size.
	EventBuffer int
}

// Info returns identity + capability metadata for this harness.
func (r *Runner) Info() harnesses.HarnessInfo {
	info := harnesses.HarnessInfo{
		Name:                 "gemini",
		Type:                 "subprocess",
		IsLocal:              false,
		IsSubscription:       false,
		ExactPinSupport:      true,
		SupportedPermissions: []string{"safe"},
		SupportedEfforts:     []string{"low", "medium", "high"},
		CostClass:            "medium",
	}
	path := r.Binary
	if path == "" {
		if resolved, err := osexec.LookPath("gemini"); err == nil {
			path = resolved
		}
	}
	if path != "" {
		info.Path = path
		info.Available = true
	} else {
		info.Error = "gemini binary not found in PATH"
	}
	return info
}

// HealthCheck verifies the gemini binary is present.
func (r *Runner) HealthCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := r.Binary
	if path == "" {
		resolved, err := osexec.LookPath("gemini")
		if err != nil {
			return fmt.Errorf("gemini binary not found: %w", err)
		}
		path = resolved
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat gemini binary: %w", err)
	}
	if st.IsDir() {
		return fmt.Errorf("gemini binary path is a directory: %s", path)
	}
	return nil
}

// Execute runs one resolved request through the gemini CLI and emits events
// on the returned channel. Since gemini has no stream-json mode, events are
// emitted after the process exits (emit-on-EOF pattern).
func (r *Runner) Execute(ctx context.Context, req harnesses.ExecuteRequest) (<-chan harnesses.Event, error) {
	binary := r.Binary
	if binary == "" {
		resolved, err := osexec.LookPath("gemini")
		if err != nil {
			return nil, fmt.Errorf("gemini binary not found: %w", err)
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

	agg, exitCode, stderr, runErr, status := r.runBuffered(ctx, binary, req, out, &seq)

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

func (r *Runner) runBuffered(ctx context.Context, binary string, req harnesses.ExecuteRequest, out chan<- harnesses.Event, seq *int64) (agg *streamAggregate, exitCode int, stderr string, runErr error, status string) {
	base := r.BaseArgs
	if base == nil {
		base = []string{}
	}
	args := append([]string{}, base...)

	// Model flag: -m <model>
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}

	// Gemini has no permission flags or effort flags.

	// PromptMode is always "stdin" for gemini.

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := osexec.CommandContext(runCtx, binary, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	cmd.Stdin = strings.NewReader(req.Prompt)
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
				sid = fmt.Sprintf("gemini-%d", time.Now().UnixNano())
			}
			logPath := filepath.Join(req.SessionLogDir, "agent-"+sid+".jsonl")
			if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				progressLog = f
				defer progressLog.Close()
			}
		}
	}

	// Buffer stdout; emit after process exits (no streaming parser for gemini).
	var stdoutBytes []byte
	stdoutReady := make(chan struct{})
	go func() {
		defer close(stdoutReady)
		stdoutBytes, _ = io.ReadAll(stdoutPipe)
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
		case <-stdoutReady:
		}
	}()

	<-stdoutReady
	<-stderrDone
	runErr = cmd.Wait()
	<-cancelDone
	stderr = stderrBuf.String()

	switch {
	case timedOut:
		return nil, -1, stderr, context.DeadlineExceeded, "timed_out"
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled):
		return nil, -1, stderr, ctx.Err(), "cancelled"
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, -1, stderr, ctx.Err(), "timed_out"
	case runErr != nil:
		ec := -1
		var exitErr *osexec.ExitError
		if errors.As(runErr, &exitErr) {
			ec = exitErr.ExitCode()
		}
		return nil, ec, stderr, runErr, "failed"
	}

	// Parse buffered output and emit events.
	output := strings.TrimSpace(string(stdoutBytes))
	parseAgg, parseErr := emitGeminiOutput(ctx, output, out, req.Metadata, seq, progressLog)
	if parseErr != nil && !errors.Is(parseErr, context.Canceled) {
		return parseAgg, 0, stderr, parseErr, "failed"
	}
	return parseAgg, 0, stderr, nil, "success"
}

// emitGeminiOutput parses buffered gemini output, emits a text_delta, and
// extracts token usage from the JSON stats block if present.
func emitGeminiOutput(ctx context.Context, output string, out chan<- harnesses.Event, metadata map[string]string, seq *int64, progressLog *os.File) (*streamAggregate, error) {
	agg := &streamAggregate{}
	if output == "" {
		return agg, nil
	}

	// Extract usage from JSON stats if present (per DDx ExtractUsage("gemini")).
	agg = parseGeminiUsage(output)

	// Emit text_delta.
	raw, err := json.Marshal(harnesses.TextDeltaData{Text: output})
	if err != nil {
		return agg, err
	}
	ev := harnesses.Event{
		Type:     harnesses.EventTypeTextDelta,
		Sequence: *seq,
		Time:     time.Now().UTC(),
		Metadata: metadata,
		Data:     raw,
	}
	*seq++

	if progressLog != nil {
		if data, err := json.Marshal(ev); err == nil {
			_, _ = progressLog.Write(data)
			_, _ = progressLog.Write([]byte("\n"))
		}
	}

	select {
	case out <- ev:
	case <-ctx.Done():
		return agg, ctx.Err()
	}
	return agg, nil
}

type stringBuilderWriter struct {
	sb *strings.Builder
}

func (w *stringBuilderWriter) Write(p []byte) (int, error) {
	return w.sb.Write(p)
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
