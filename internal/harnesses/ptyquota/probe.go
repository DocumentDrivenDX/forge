package ptyquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
	"github.com/DocumentDrivenDX/agent/internal/pty/terminal"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

type Status string

const (
	StatusOK              Status = "ok"
	StatusUnavailable     Status = "unavailable"
	StatusUnauthenticated Status = "unauthenticated"
	StatusError           Status = "error"
)

type ProbeError struct {
	Status Status
	Reason string
	Err    error
}

func (e *ProbeError) Error() string {
	if e == nil {
		return ""
	}
	reason := e.Reason
	if reason == "" {
		reason = string(e.Status)
	}
	if e.Err == nil {
		return reason
	}
	return reason + ": " + e.Err.Error()
}

func (e *ProbeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ErrorStatus(err error) Status {
	if err == nil {
		return StatusOK
	}
	var probeErr *ProbeError
	if errors.As(err, &probeErr) {
		return probeErr.Status
	}
	return StatusError
}

type Config struct {
	HarnessName string
	Binary      string
	Args        []string
	Workdir     string
	Env         []string

	Command            string
	ReadyMarkers       []string
	DoneMarkers        []string
	DoneAnyMarkers     []string
	DoneWhen           func(string) bool
	ResetBeforeCommand bool
	Timeout            time.Duration
	Size               session.Size

	CassetteDir string
	Quota       func(string) (cassette.QuotaRecord, error)
}

type Result struct {
	Text        string
	CassetteDir string
	Status      Status
}

func Run(ctx context.Context, cfg Config) (Result, error) {
	cfg = defaultConfig(cfg)
	if cfg.Binary == "" {
		return Result{}, &ProbeError{Status: StatusUnavailable, Reason: "quota probe binary is empty"}
	}
	binaryPath, err := exec.LookPath(cfg.Binary)
	if err != nil {
		return Result{}, &ProbeError{Status: StatusUnavailable, Reason: cfg.Binary + " not found in PATH", Err: err}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	recordDir, commitDir, err := prepareRecordDir(cfg.CassetteDir)
	if err != nil {
		return Result{}, &ProbeError{Status: StatusError, Reason: "prepare quota cassette", Err: err}
	}
	var rec *cassette.Recorder
	if recordDir != "" {
		rec, err = cassette.Create(recordDir, manifestFor(cfg))
		if err != nil {
			cleanupRecordDir(recordDir, commitDir)
			return Result{}, &ProbeError{Status: StatusError, Reason: "create quota cassette", Err: err}
		}
	}

	emu := terminal.New(terminal.Size{Rows: int(cfg.Size.Rows), Cols: int(cfg.Size.Cols)})
	s, err := session.Start(ctx, binaryPath, cfg.Args, cfg.Workdir, probeEnv(cfg.Env), cfg.Size, session.WithTimeout(cfg.Timeout), session.WithBufferSize(4096))
	if err != nil {
		closeDiscard(rec)
		cleanupRecordDir(recordDir, commitDir)
		return Result{}, classifyFailure(cfg.HarnessName, "", err)
	}

	run := &runState{
		session:  s,
		rec:      rec,
		emu:      emu,
		size:     terminal.Size{Rows: int(cfg.Size.Rows), Cols: int(cfg.Size.Cols)},
		scrubber: scrubberFor(cfg),
		done:     make(chan struct{}),
	}
	go run.recordOutput()

	result, err := run.drive(ctx, cfg)
	status := session.ExitStatus{}
	if err != nil {
		_ = s.Kill()
		status = s.Wait()
		select {
		case <-run.done:
		case <-time.After(5 * time.Second):
			_ = s.Close()
		}
		closeDiscard(rec)
		cleanupRecordDir(recordDir, commitDir)
		return Result{}, classifyFailure(cfg.HarnessName, run.screen(), err)
	}
	status = stopSession(s, 5*time.Second)
	finishErr := run.finish(status, cfg)
	if finishErr != nil {
		closeDiscard(rec)
		cleanupRecordDir(recordDir, commitDir)
		return Result{}, &ProbeError{Status: StatusError, Reason: "finish quota probe", Err: finishErr}
	}
	if rec != nil {
		if err := commitRecordDir(recordDir, commitDir); err != nil {
			cleanupRecordDir(recordDir, commitDir)
			return Result{}, &ProbeError{Status: StatusError, Reason: "commit quota cassette", Err: err}
		}
		result.CassetteDir = commitDir
	}
	result.Status = StatusOK
	return result, nil
}

func defaultConfig(cfg Config) Config {
	if cfg.HarnessName == "" {
		cfg.HarnessName = cfg.Binary
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Size.Rows == 0 {
		cfg.Size.Rows = 50
	}
	if cfg.Size.Cols == 0 {
		cfg.Size.Cols = 220
	}
	return cfg
}

type runState struct {
	mu        sync.Mutex
	session   *session.Session
	rec       *cassette.Recorder
	emu       *terminal.VT10x
	latest    terminal.Frame
	frameSeen bool
	size      terminal.Size
	outputErr error
	frames    int
	rawBytes  int64
	raw       bytes.Buffer
	scrubber  *cassette.Scrubber
	report    cassette.ScrubReport
	done      chan struct{}
}

func (r *runState) drive(ctx context.Context, cfg Config) (Result, error) {
	if len(cfg.ReadyMarkers) > 0 {
		if err := r.waitForAnyText(ctx, cfg.ReadyMarkers...); err != nil {
			return Result{}, err
		}
	}
	if cfg.ResetBeforeCommand {
		r.resetTerminal()
	}
	if cfg.Command != "" {
		if err := r.sendBytes([]byte(cfg.Command)); err != nil {
			return Result{}, err
		}
	}
	if len(cfg.DoneMarkers) > 0 || cfg.DoneWhen != nil {
		if err := r.waitForText(ctx, cfg.DoneMarkers, cfg.DoneAnyMarkers, cfg.DoneWhen); err != nil {
			return Result{}, err
		}
	}
	text := r.screen()
	if cfg.Quota != nil {
		quota, err := cfg.Quota(text)
		if err != nil {
			return Result{}, err
		}
		if r.rec != nil {
			if err := r.rec.WriteQuota(quota); err != nil {
				return Result{}, err
			}
		}
	}
	return Result{Text: text}, nil
}

func (r *runState) recordOutput() {
	defer close(r.done)
	for chunk := range r.session.Output() {
		if chunk.ReadError != nil && !errors.Is(chunk.ReadError, io.EOF) {
			r.mu.Lock()
			r.outputErr = chunk.ReadError
			r.mu.Unlock()
			return
		}
		if len(chunk.Bytes) == 0 {
			continue
		}
		r.mu.Lock()
		_, _ = r.raw.Write(chunk.Bytes)
		frame, err := r.emu.Feed(chunk.Bytes)
		if err != nil {
			r.outputErr = err
			r.mu.Unlock()
			return
		}
		r.latest = frame
		r.frameSeen = true
		r.frames++
		r.rawBytes += int64(len(chunk.Bytes))
		r.mu.Unlock()
	}
}

func (r *runState) waitForAnyText(ctx context.Context, markers ...string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		screen := r.screenLocked()
		ready := r.frameSeen
		outputErr := r.outputErr
		r.mu.Unlock()
		if outputErr != nil {
			return outputErr
		}
		if authFailureText(screen) {
			return &ProbeError{Status: StatusUnauthenticated, Reason: "quota probe is not authenticated"}
		}
		if unsupportedAccountText(screen) {
			return &ProbeError{Status: StatusUnavailable, Reason: "quota probe account state is unsupported"}
		}
		for _, marker := range markers {
			if ready && strings.Contains(screen, marker) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return &ProbeError{Status: StatusError, Reason: "quota probe timed out", Err: ctx.Err()}
		case <-r.done:
			return r.exitedBeforeMarkersError(markers, nil, nil)
		case <-ticker.C:
		}
	}
}

func (r *runState) waitForAllText(ctx context.Context, markers ...string) error {
	return r.waitForText(ctx, markers, nil, nil)
}

func (r *runState) waitForText(ctx context.Context, allMarkers, anyMarkers []string, doneWhen func(string) bool) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		screen := r.screenLocked()
		ready := r.frameSeen
		outputErr := r.outputErr
		r.mu.Unlock()
		if outputErr != nil {
			return outputErr
		}
		if authFailureText(screen) {
			return &ProbeError{Status: StatusUnauthenticated, Reason: "quota probe is not authenticated"}
		}
		if unsupportedAccountText(screen) {
			return &ProbeError{Status: StatusUnavailable, Reason: "quota probe account state is unsupported"}
		}
		if ready && containsAllAndAny(screen, allMarkers, anyMarkers) && (doneWhen == nil || doneWhen(screen)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return &ProbeError{Status: StatusError, Reason: "quota probe timed out", Err: ctx.Err()}
		case <-r.done:
			return r.exitedBeforeMarkersError(allMarkers, anyMarkers, doneWhen)
		case <-ticker.C:
		}
	}
}

func (r *runState) exitedBeforeMarkersError(allMarkers, anyMarkers []string, doneWhen func(string) bool) error {
	r.mu.Lock()
	screen := r.screenLocked()
	outputErr := r.outputErr
	ready := r.frameSeen
	r.mu.Unlock()
	if outputErr != nil {
		return outputErr
	}
	if authFailureText(screen) {
		return &ProbeError{Status: StatusUnauthenticated, Reason: "quota probe is not authenticated"}
	}
	if unsupportedAccountText(screen) {
		return &ProbeError{Status: StatusUnavailable, Reason: "quota probe account state is unsupported"}
	}
	if ready && containsAllAndAny(screen, allMarkers, anyMarkers) && (doneWhen == nil || doneWhen(screen)) {
		return nil
	}
	return &ProbeError{Status: StatusError, Reason: "quota probe exited before expected output"}
}

func containsAllAndAny(screen string, allMarkers, anyMarkers []string) bool {
	for _, marker := range allMarkers {
		if !strings.Contains(screen, marker) {
			return false
		}
	}
	if len(anyMarkers) == 0 {
		return true
	}
	for _, marker := range anyMarkers {
		if strings.Contains(screen, marker) {
			return true
		}
	}
	return false
}

func (r *runState) sendBytes(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.session.SendBytes(b); err != nil {
		return err
	}
	if r.rec == nil {
		return nil
	}
	_, err := r.rec.RecordInput(session.EventInput, b, "", nil, "")
	return err
}

func (r *runState) finish(status session.ExitStatus, cfg Config) error {
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		_ = r.session.Kill()
		return fmt.Errorf("timed out waiting for output drain")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.outputErr != nil {
		return r.outputErr
	}
	if r.rec == nil {
		return nil
	}
	finalText, err := r.recordSanitizedOutputLocked()
	if err != nil {
		return err
	}
	exit := &session.ExitStatus{Code: status.Code, Signal: status.Signal, Exited: status.Exited, Signaled: status.Signaled}
	if _, err := r.rec.RecordServiceEvent(map[string]any{
		"type":      "quota-probe",
		"harness":   cfg.HarnessName,
		"status":    string(StatusOK),
		"frames":    r.frames,
		"raw_bytes": r.rawBytes,
	}); err != nil {
		return err
	}
	if err := r.rec.RecordFinal(cassette.FinalRecord{
		Exit:      exit,
		Metadata:  map[string]any{"harness": cfg.HarnessName, "frames": r.frames, "raw_bytes": r.rawBytes},
		FinalText: finalText,
	}); err != nil {
		return err
	}
	report := r.report
	if report.Status == "" {
		report = cassette.ScrubReport{Status: "clean", Rules: []string{}, HitCounts: map[string]int{}}
	}
	report.EnvAllowlist = []string{"TERM", "LANG", "LC_ALL"}
	report.IntentionallyPreserved = appendUnique(report.IntentionallyPreserved, "quota-status-text")
	if err := r.rec.WriteScrubReport(report); err != nil {
		return err
	}
	return r.rec.Close()
}

func (r *runState) screen() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.screenLocked()
}

func (r *runState) screenLocked() string {
	if !r.frameSeen {
		return ""
	}
	return strings.Join(r.latest.Text, "\n")
}

func (r *runState) resetTerminal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emu = terminal.New(r.size)
	r.latest = terminal.Frame{}
	r.frameSeen = false
	r.raw.Reset()
}

func (r *runState) recordSanitizedOutputLocked() (string, error) {
	raw := r.raw.String()
	if r.scrubber != nil {
		var report cassette.ScrubReport
		raw, report = r.scrubber.ScrubString(raw)
		r.report = mergeScrubReports(r.report, report)
	}
	chunk := session.OutputChunk{Bytes: []byte(raw)}
	if _, err := r.rec.RecordOutput(chunk); err != nil {
		return "", err
	}
	emu := terminal.New(r.size)
	frame, err := emu.Feed(chunk.Bytes)
	if err != nil {
		return "", err
	}
	frameRec, err := r.rec.RecordFrame(frame)
	if err != nil {
		return "", err
	}
	frame.TMS = frameRec.TMS
	finalText := strings.Join(frame.Text, "\n")
	r.rawBytes = int64(len(chunk.Bytes))
	return finalText, nil
}

func stopSession(s *session.Session, timeout time.Duration) session.ExitStatus {
	_ = s.SendBytes([]byte{0x03})
	_ = s.Kill()
	ch := make(chan session.ExitStatus, 1)
	go func() { ch <- s.Wait() }()
	select {
	case status := <-ch:
		return status
	case <-time.After(timeout):
		return session.ExitStatus{Code: -1, Err: fmt.Errorf("timed out waiting for session exit")}
	}
}

func manifestFor(cfg Config) cassette.Manifest {
	return cassette.Manifest{
		ID:      cfg.HarnessName + "-quota",
		Harness: cassette.Harness{Name: cfg.HarnessName},
		Command: cassette.Command{
			Argv:          append([]string{manifestBinaryName(cfg.Binary)}, cfg.Args...),
			WorkdirPolicy: workdirPolicy(cfg.Workdir),
			EnvAllowlist:  []string{"TERM", "LANG", "LC_ALL"},
			TimeoutMS:     cfg.Timeout.Milliseconds(),
		},
		Terminal: cassette.Terminal{
			InitialRows: int(cfg.Size.Rows),
			InitialCols: int(cfg.Size.Cols),
			Locale:      "C.UTF-8",
			Term:        "xterm-256color",
			PTYMode:     map[string]any{"outer": "creack/pty"},
			Emulator:    cassette.Emulator{Name: "vt10x", Version: "v0.0.0-20220301184237-5011da428d02"},
		},
		Timing: cassette.Timing{ResolutionMS: 50, ClockPolicy: "monotonic-elapsed", ReplayDefault: cassette.ReplayCollapsed},
		Provenance: cassette.Provenance{
			OS:              runtime.GOOS,
			Arch:            runtime.GOARCH,
			RecordedAt:      time.Now().UTC().Format(time.RFC3339),
			RecorderVersion: "quota-pty-v1",
		},
	}
}

func manifestBinaryName(binary string) string {
	if binary == "" {
		return ""
	}
	return filepath.Base(binary)
}

func workdirPolicy(workdir string) string {
	if workdir == "" {
		return "caller"
	}
	return "explicit"
}

func probeEnv(extra []string) []string {
	env := []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	env = append(env, extra...)
	return env
}

func scrubberFor(cfg Config) *cassette.Scrubber {
	home, _ := os.UserHomeDir()
	worktree := cfg.Workdir
	if worktree == "" {
		worktree, _ = os.Getwd()
	}
	return cassette.NewScrubber(cassette.Scrubber{
		Home:         home,
		Worktree:     worktree,
		EnvAllowlist: []string{"TERM", "LANG", "LC_ALL"},
	})
}

func mergeScrubReports(a, b cassette.ScrubReport) cassette.ScrubReport {
	if a.HitCounts == nil {
		a.HitCounts = map[string]int{}
	}
	if b.Status == "redacted" {
		a.Status = "redacted"
	}
	if a.Status == "" {
		a.Status = b.Status
	}
	a.Rules = appendUnique(a.Rules, b.Rules...)
	a.EnvAllowlist = appendUnique(a.EnvAllowlist, b.EnvAllowlist...)
	a.IntentionallyPreserved = appendUnique(a.IntentionallyPreserved, b.IntentionallyPreserved...)
	for key, count := range b.HitCounts {
		a.HitCounts[key] += count
	}
	if a.Status == "" {
		a.Status = "clean"
	}
	return a
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values)+len(additions))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, value := range additions {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func prepareRecordDir(target string) (string, string, error) {
	if target == "" {
		return "", "", nil
	}
	target = filepath.Clean(target)
	if err := refuseNewerTargetVersion(target); err != nil {
		return "", "", err
	}
	parent := filepath.Dir(target)
	if err := safefs.MkdirAll(parent, 0o750); err != nil {
		return "", "", err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return "", "", err
	}
	return tmp, target, nil
}

func commitRecordDir(tmp, target string) error {
	if tmp == "" || target == "" {
		return nil
	}
	if err := refuseNewerTargetVersion(target); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func refuseNewerTargetVersion(target string) error {
	data, err := safefs.ReadFile(filepath.Join(target, cassette.ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var existing struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &existing); err != nil {
		return err
	}
	if existing.Version > cassette.Version {
		return fmt.Errorf("cassette: refuse to overwrite newer schema version %d", existing.Version)
	}
	return nil
}

func cleanupRecordDir(tmp, target string) {
	if tmp != "" {
		_ = os.RemoveAll(tmp)
	}
	if target != "" {
		_ = os.RemoveAll(target + ".tmp")
	}
}

func closeDiscard(rec *cassette.Recorder) {
	if rec == nil {
		return
	}
	_ = rec.RecordFinal(cassette.FinalRecord{Metadata: map[string]any{"status": "discarded"}})
	_ = rec.Close()
}

func classifyFailure(harness, text string, err error) error {
	if err == nil {
		return nil
	}
	var probeErr *ProbeError
	if errors.As(err, &probeErr) {
		return probeErr
	}
	if authFailureText(text) {
		return &ProbeError{Status: StatusUnauthenticated, Reason: harness + " quota probe is unauthenticated", Err: err}
	}
	if unsupportedAccountText(text) {
		return &ProbeError{Status: StatusUnavailable, Reason: harness + " quota probe account state is unsupported", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &ProbeError{Status: StatusError, Reason: harness + " quota probe timed out", Err: err}
	}
	return &ProbeError{Status: StatusError, Reason: harness + " quota probe failed", Err: err}
}

func authFailureText(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"not logged in",
		"not authenticated",
		"unauthenticated",
		"authentication failed",
		"auth expired",
		"expired auth",
		"session expired",
		"please log in",
		"please login",
		"please sign in",
		"sign in to",
		"sign-in required",
		"login required",
		"oauth expired",
		"oauth failed",
		"oauth error",
		"oauth login required",
		"oauth authentication failed",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func unsupportedAccountText(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"unsupported account",
		"unsupported plan",
		"quota unavailable for this account",
		"usage is unavailable",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
