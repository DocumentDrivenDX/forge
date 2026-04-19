package claude

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

// Default size for the per-Execute event channel buffer. Large enough that
// fast tool-call bursts (claude can emit dozens of blocks per turn) do not
// stall the parser, small enough to bound memory in pathological streams.
const defaultEventBuffer = 64

// Default idle/stream timeout when ExecuteRequest leaves IdleTimeout unset.
// Mirrors the wall-clock cap the DDx-side runner used historically; an
// explicit ExecuteRequest.Timeout still wins.
const defaultIdleTimeout = 0 // 0 = no idle cap; rely on Timeout / ctx.

// Runner is the subprocess-backed claude harness. It launches the claude
// CLI in stream-json mode, parses each line into harness Events, and emits
// a final Event when the subprocess exits. On ctx.Done(), the subprocess
// (and any forked children belonging to its process group) is signalled
// SIGTERM and reaped so PTY/tool children don't outlive the request.
type Runner struct {
	// Binary is the absolute path to the claude executable. When empty the
	// runner resolves "claude" via PATH at Execute time.
	Binary string

	// BaseArgs is prepended to the per-request argument list; callers use
	// it to pin a consistent invocation profile (e.g. ["--print", "-p",
	// "--output-format", "stream-json", "--verbose"]).
	BaseArgs []string

	// PromptMode controls how the prompt is delivered to claude:
	//   "stdin" (default) — prompt is piped on stdin
	//   "arg"             — prompt is appended as the final positional argument
	PromptMode string

	// EventBuffer overrides the per-Execute channel buffer size. Zero
	// selects defaultEventBuffer.
	EventBuffer int
}

// Info returns identity + capability metadata for this harness.
//
// Path is best-effort: the runner reports Binary if set, otherwise looks
// up "claude" on PATH. Available tracks whether the lookup succeeded so
// callers can show a useful error in `ddx agent list` without invoking
// HealthCheck synchronously.
func (r *Runner) Info() harnesses.HarnessInfo {
	info := harnesses.HarnessInfo{
		Name:                 "claude",
		Type:                 "subprocess",
		IsLocal:              false,
		IsSubscription:       true,
		ExactPinSupport:      false,
		SupportedPermissions: []string{"safe", "supervised", "unrestricted"},
		SupportedReasoning:   []string{"low", "medium", "high", "xhigh", "max"},
		CostClass:            "expensive",
	}
	path := r.Binary
	if path == "" {
		if resolved, err := osexec.LookPath("claude"); err == nil {
			path = resolved
		}
	}
	if path != "" {
		info.Path = path
		info.Available = true
	} else {
		info.Error = "claude binary not found in PATH"
	}
	return info
}

// HealthCheck verifies the claude binary resolves on PATH (or at the
// configured Binary). It does NOT invoke the binary so it stays cheap and
// safe to call from request hot paths. A future extension can probe quota
// state via the cache layer.
func (r *Runner) HealthCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := r.Binary
	if path == "" {
		resolved, err := osexec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude binary not found: %w", err)
		}
		path = resolved
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat claude binary: %w", err)
	}
	if st.IsDir() {
		return fmt.Errorf("claude binary path is a directory: %s", path)
	}
	return nil
}

// Execute runs one resolved request through the claude CLI and emits
// stream-derived events on the returned channel. The channel is closed
// once a final event has been emitted. PTY/orphan children are reaped on
// ctx.Done(). If the CLI rejects stream-json flags (older build), the
// runner falls back to a buffered legacy invocation and emits a single
// text_delta + final from the buffered output.
func (r *Runner) Execute(ctx context.Context, req harnesses.ExecuteRequest) (<-chan harnesses.Event, error) {
	binary := r.Binary
	if binary == "" {
		resolved, err := osexec.LookPath("claude")
		if err != nil {
			return nil, fmt.Errorf("claude binary not found: %w", err)
		}
		binary = resolved
	}

	bufSize := r.EventBuffer
	if bufSize <= 0 {
		bufSize = defaultEventBuffer
	}

	// Channel returned to the caller; the goroutine below owns closing it.
	out := make(chan harnesses.Event, bufSize)

	go r.run(ctx, binary, req, out)
	return out, nil
}

// run is the per-Execute goroutine: starts claude, streams events, and
// guarantees a final event + channel close on every termination path.
func (r *Runner) run(ctx context.Context, binary string, req harnesses.ExecuteRequest, out chan<- harnesses.Event) {
	defer close(out)

	start := time.Now()
	var seq int64

	// First attempt: stream-json invocation.
	agg, exitCode, stderr, runErr, status := r.runStreaming(ctx, binary, req, out, &seq)

	// Fallback path: claude rejected the stream-json flags. Retry with the
	// legacy buffered --print/-p/--output-format=json invocation. We surface
	// the legacy output as a single text_delta so consumers still receive
	// the model's final text.
	if status == "failed" && exitCode == 2 && claudeStreamArgsUnsupported(stderr) {
		// Reset the aggregate so legacy output drives final event.
		agg, exitCode, stderr, runErr, status = r.runLegacy(ctx, binary, req, out, &seq)
	}

	// Emit the final event regardless of outcome so downstream consumers
	// always see a terminator. Errors during emit are non-fatal — the
	// channel close still signals end-of-stream.
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
		// Defensive: marshal can only fail on programmer error here.
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
		// Caller has stopped consuming; drop and close.
	}
}

// runStreaming drives the stream-json path: launches claude with the
// configured BaseArgs, pipes stdout through the parser, and returns the
// aggregated stream state plus exit metadata.
func (r *Runner) runStreaming(ctx context.Context, binary string, req harnesses.ExecuteRequest, out chan<- harnesses.Event, seq *int64) (agg *streamAggregate, exitCode int, stderr string, runErr error, status string) {
	args := append([]string{}, r.BaseArgs...)
	if r.PromptMode == "arg" && req.Prompt != "" {
		args = append(args, req.Prompt)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := osexec.CommandContext(runCtx, binary, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if r.PromptMode != "arg" {
		cmd.Stdin = strings.NewReader(req.Prompt)
	}
	// Put the child in its own process group so we can signal the entire
	// tree (PTY/tool children included) on ctx.Done().
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

	// Open the per-run progress trace, when SessionLogDir is set, so the
	// agent loop's TailSessionLog endpoint can mirror events to disk.
	var progressLog *os.File
	if req.SessionLogDir != "" {
		if err := os.MkdirAll(req.SessionLogDir, 0o755); err == nil {
			sid := req.SessionID
			if sid == "" {
				sid = fmt.Sprintf("claude-%d", time.Now().UnixNano())
			}
			logPath := filepath.Join(req.SessionLogDir, "agent-"+sid+".jsonl")
			if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				progressLog = f
				defer progressLog.Close()
			}
		}
	}

	// Tee stdout into the parser and (optionally) into the progress log.
	parserReader, parserWriter := io.Pipe()
	parseDone := make(chan struct{})
	var parseAgg *streamAggregate
	var parseErr error
	// Wrap out so we can also mirror events to disk as JSONL. mirrorDone
	// signals that the mirror goroutine has fully drained — must be awaited
	// before run() lets defer close(out) fire, otherwise we get a close vs.
	// chansend race when the mirror is mid-send to dst.
	mirrored, mirrorDone := mirroredEvents(out, progressLog, ctx)
	go func() {
		defer close(parseDone)
		defer close(mirrored) // releases the mirror goroutine's range loop
		parseAgg, parseErr = parseClaudeStream(runCtx, parserReader, mirrored, req.Metadata, seq)
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

	// Wall-clock / idle timeout: if Timeout is set, cancel after it fires.
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

	// Cancellation watchdog: when the parent ctx is done, signal the
	// process group and let cmd.Wait() pick up the exit. The defer on
	// runCtx already calls cancel; this goroutine just escalates to
	// SIGTERM/SIGKILL on the group so PTY children don't survive.
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
	<-mirrorDone
	runErr = cmd.Wait()
	<-cancelDone
	stderr = stderrBuf.String()

	// Classify exit.
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
	if parseAgg != nil && parseAgg.IsError {
		return parseAgg, 0, stderr, errors.New("claude reported is_error=true"), "failed"
	}
	return parseAgg, 0, stderr, nil, "success"
}

// runLegacy invokes the legacy buffered claude path used when the CLI
// rejected stream-json flags. It surfaces the captured stdout as a single
// text_delta event so callers still receive the model's text.
func (r *Runner) runLegacy(ctx context.Context, binary string, req harnesses.ExecuteRequest, out chan<- harnesses.Event, seq *int64) (*streamAggregate, int, string, error, string) {
	args := []string{"--print", "-p", "--output-format", "json"}
	if r.PromptMode == "arg" && req.Prompt != "" {
		args = append(args, req.Prompt)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := osexec.CommandContext(runCtx, binary, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if r.PromptMode != "arg" {
		cmd.Stdin = strings.NewReader(req.Prompt)
	}
	setProcessGroup(cmd)

	stdoutBytes, err := cmd.Output()
	stderrBytes := ""
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		stderrBytes = string(exitErr.Stderr)
	}

	if err != nil {
		ec := -1
		if exitErr != nil {
			ec = exitErr.ExitCode()
		}
		return nil, ec, stderrBytes, err, "failed"
	}

	text := strings.TrimSpace(string(stdoutBytes))
	if text != "" {
		raw, _ := json.Marshal(harnesses.TextDeltaData{Text: text})
		ev := harnesses.Event{
			Type:     harnesses.EventTypeTextDelta,
			Sequence: *seq,
			Time:     time.Now().UTC(),
			Metadata: req.Metadata,
			Data:     raw,
		}
		*seq++
		select {
		case out <- ev:
		case <-ctx.Done():
			return nil, 0, stderrBytes, ctx.Err(), "cancelled"
		}
	}
	return &streamAggregate{FinalText: text}, 0, stderrBytes, nil, "success"
}

// stringBuilderWriter adapts *strings.Builder to io.Writer.
type stringBuilderWriter struct {
	sb *strings.Builder
}

func (w *stringBuilderWriter) Write(p []byte) (int, error) {
	return w.sb.Write(p)
}

// mirroredEvents returns (in, done): callers send events on `in` and close
// it when finished; the goroutine forwards each event to dst and, when log
// is non-nil, also writes a JSONL line for TailSessionLog consumers. `done`
// closes once the goroutine has fully drained, so the caller can safely
// close dst without racing with an in-flight send.
func mirroredEvents(dst chan<- harnesses.Event, log *os.File, ctx context.Context) (chan harnesses.Event, <-chan struct{}) {
	mid := make(chan harnesses.Event, cap(dst))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range mid {
			if log != nil {
				if data, err := json.Marshal(ev); err == nil {
					_, _ = log.Write(data)
					_, _ = log.Write([]byte("\n"))
				}
			}
			select {
			case dst <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return mid, done
}

// trimErrorBlob caps stderr for inclusion in the final event so a runaway
// error log doesn't bloat the channel payload.
func trimErrorBlob(s string) string {
	const max = 2048
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "...(truncated)"
	}
	return s
}

// setProcessGroup configures cmd to start in its own process group on
// POSIX. Implemented in a build-tagged file for cross-platform support.
func setProcessGroup(cmd *osexec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	setProcessGroupAttr(cmd.SysProcAttr)
}
