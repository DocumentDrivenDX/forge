//go:build integration && !windows

package ptytest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
	"github.com/DocumentDrivenDX/agent/internal/pty/terminal"
	"github.com/stretchr/testify/require"
)

const (
	dockerConformanceBaseImage = "debian:bookworm-slim@sha256:4724b8cc51e33e398f0e2e15e18d5ec2851ff0c2280647e1310bc1642182655d"
	dockerConformanceImage     = "agent-pty-conformance:bookworm-slim-4724b8cc"
	dockerFixturesMount        = "/fixtures"
)

type dockerConformanceCase struct {
	name        string
	script      string
	scenario    string
	size        session.Size
	flow        func(*dockerConformanceRun) error
	exitTimeout time.Duration
}

type dockerConformanceRecord struct {
	dir      string
	manifest cassette.Manifest
	timings  map[string]int64
	text     map[string]string
}

func TestDockerTUIConformanceSuite(t *testing.T) {
	if os.Getenv("AGENT_PTY_INTEGRATION") != "1" {
		t.Skip("set AGENT_PTY_INTEGRATION=1 to run Docker-backed PTY conformance")
	}
	fixtures := dockerConformanceFixtureDir(t)
	buildDockerConformanceImage(t)

	cases := []dockerConformanceCase{
		{
			name:     "top",
			script:   "top.sh",
			scenario: "top.yaml",
			size:     session.Size{Rows: 24, Cols: 80},
			flow: func(run *dockerConformanceRun) error {
				initial, _, err := run.waitForAnyText(8*time.Second, "top -")
				if err != nil {
					return err
				}
				run.timings["initial"] = initial.TMS
				refresh, _, err := run.waitForAnyTextAfter(initial.TMS+300, 8*time.Second, "top -")
				if err != nil {
					return err
				}
				run.timings["refresh"] = refresh.TMS
				if err := run.sendBytes([]byte("h")); err != nil {
					return err
				}
				help, marker, err := run.waitForAnyText(8*time.Second, "Help", "Interactive", "Commands")
				if err != nil {
					return err
				}
				run.timings["help"] = help.TMS
				run.text["help_marker"] = marker
				resizeTMS, err := run.resize(session.Size{Rows: 30, Cols: 100})
				if err != nil {
					return err
				}
				run.timings["resize"] = resizeTMS
				if err := run.sendBytes([]byte("q")); err != nil {
					return err
				}
				if _, _, err := run.waitForAnyTextAfter(help.TMS, 8*time.Second, "top -"); err != nil {
					return err
				}
				if err := run.sendBytes([]byte("q")); err != nil {
					return err
				}
				return nil
			},
			exitTimeout: 8 * time.Second,
		},
		{
			name:     "less",
			script:   "less.sh",
			scenario: "less.yaml",
			size:     session.Size{Rows: 20, Cols: 72},
			flow: func(run *dockerConformanceRun) error {
				initial, _, err := run.waitForAnyText(8*time.Second, "less-line-001")
				if err != nil {
					return err
				}
				run.timings["initial"] = initial.TMS
				if err := run.sendBytes([]byte(" ")); err != nil {
					return err
				}
				later, marker, err := run.waitForAnyText(8*time.Second, "less-line-030", "less-line-035", "less-line-040")
				if err != nil {
					return err
				}
				run.timings["page"] = later.TMS
				run.text["page_marker"] = marker
				if err := run.sendBytes([]byte("q")); err != nil {
					return err
				}
				return nil
			},
			exitTimeout: 5 * time.Second,
		},
		{
			name:     "vim",
			script:   "vim.sh",
			scenario: "vim.yaml",
			size:     session.Size{Rows: 22, Cols: 76},
			flow: func(run *dockerConformanceRun) error {
				initial, _, err := run.waitForAnyText(8*time.Second, "alpha vim conformance")
				if err != nil {
					return err
				}
				run.timings["initial"] = initial.TMS
				if err := run.sendBytes([]byte("GODDX-typed-line")); err != nil {
					return err
				}
				inserted, _, err := run.waitForAnyText(8*time.Second, "DDX-typed-line")
				if err != nil {
					return err
				}
				run.timings["inserted"] = inserted.TMS
				if err := run.sendKey(session.KeyEscape); err != nil {
					return err
				}
				if err := run.sendBytes([]byte(":q!\r")); err != nil {
					return err
				}
				return nil
			},
			exitTimeout: 5 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := recordDockerConformanceCase(t, fixtures, tc)
			scenario := loadDockerConformanceScenario(t, fixtures, tc, record)
			result, err := RunScenario(context.Background(), scenario)
			require.NoError(t, err)
			requireConformanceEvidence(t, result, record.dir)
			if tc.name == "top" {
				requireFrameSpread(t, result.Events, "top -", 300)
			}
		})
	}
}

func recordDockerConformanceCase(t *testing.T, fixtures string, tc dockerConformanceCase) dockerConformanceRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), tc.name)
	run, err := startDockerConformanceRun(ctx, dir, fixtures, tc)
	require.NoError(t, err)
	defer run.close()

	require.NoError(t, tc.flow(run))
	status, err := run.waitExit(tc.exitTimeout)
	require.NoError(t, err, "last screen:\n%s", run.screen())
	require.Equal(t, 0, status.Code, "last screen:\n%s", run.screen())
	require.NoError(t, run.finish(status))

	reader, err := cassette.Open(dir)
	require.NoError(t, err)
	return dockerConformanceRecord{dir: dir, manifest: reader.Manifest(), timings: run.timings, text: run.text}
}

func startDockerConformanceRun(ctx context.Context, dir, fixtures string, tc dockerConformanceCase) (*dockerConformanceRun, error) {
	clock := newDockerConformanceClock()
	args := []string{
		"run", "--rm", "-i", "-t",
		"--env", "TERM=xterm-256color",
		"--env", "LANG=C.UTF-8",
		"--env", "LC_ALL=C.UTF-8",
		"--volume", fixtures + ":" + dockerFixturesMount + ":ro",
		dockerConformanceImage,
		"sh", filepath.Join(dockerFixturesMount, tc.script),
	}
	rec, err := cassette.Create(dir, cassette.Manifest{
		ID:      "docker-" + tc.name,
		Harness: cassette.Harness{Name: "docker-pty-conformance", BinaryVersion: dockerConformanceImage},
		Command: cassette.Command{
			Argv:           append([]string{"docker"}, args...),
			WorkdirPolicy:  "docker-image",
			EnvAllowlist:   []string{"TERM", "LANG", "LC_ALL"},
			TimeoutMS:      30000,
			PermissionMode: "docker-integration",
		},
		Terminal: cassette.Terminal{
			InitialRows: int(tc.size.Rows),
			InitialCols: int(tc.size.Cols),
			Locale:      "C.UTF-8",
			Term:        "xterm-256color",
			PTYMode:     map[string]any{"outer": "creack/pty", "docker_tty": true},
			Emulator:    cassette.Emulator{Name: "vt10x", Version: "v0.0.0-20220301184237-5011da428d02"},
		},
		Timing: cassette.Timing{ResolutionMS: 50, ClockPolicy: "monotonic-elapsed", ReplayDefault: cassette.ReplayCollapsed},
		Provenance: cassette.Provenance{
			GitSHA:          currentGitSHA(),
			ContractVersion: "CONTRACT-003",
			OS:              runtime.GOOS,
			Arch:            runtime.GOARCH,
			RecorderVersion: "docker-conformance-v1",
		},
	}, cassette.WithClock(clock))
	if err != nil {
		return nil, err
	}
	emu := terminal.New(terminal.Size{Rows: int(tc.size.Rows), Cols: int(tc.size.Cols)}, terminal.WithClock(clock))
	clock.Start()

	s, err := session.Start(ctx, "docker", args, "", []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}, tc.size, session.WithTimeout(30*time.Second), session.WithBufferSize(4096))
	if err != nil {
		_ = rec.Close()
		return nil, err
	}
	run := &dockerConformanceRun{
		session: s,
		rec:     rec,
		emu:     emu,
		name:    tc.name,
		script:  tc.script,
		timings: map[string]int64{},
		text:    map[string]string{},
		done:    make(chan struct{}),
	}
	if err := run.recordServiceLocked(map[string]any{"type": "tui-conformance", "case": tc.name, "image": dockerConformanceBaseImage, "script": tc.script, "stage": "start"}); err != nil {
		_ = s.Close()
		_ = rec.Close()
		return nil, err
	}
	go run.recordOutput()
	return run, nil
}

type dockerConformanceRun struct {
	mu        sync.Mutex
	session   *session.Session
	rec       *cassette.Recorder
	emu       *terminal.VT10x
	name      string
	script    string
	latest    terminal.Frame
	frameSeen bool
	outputErr error
	frames    int
	rawBytes  int64
	timings   map[string]int64
	text      map[string]string
	done      chan struct{}
}

type dockerConformanceClock struct {
	mu        sync.Mutex
	base      time.Time
	live      bool
	liveStart time.Time
}

func newDockerConformanceClock() *dockerConformanceClock {
	return &dockerConformanceClock{base: time.Unix(1700000000, 0)}
}

func (c *dockerConformanceClock) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live = true
	c.liveStart = time.Now()
}

func (c *dockerConformanceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.live {
		return c.base
	}
	return c.base.Add(time.Since(c.liveStart))
}

func (c *dockerConformanceClock) Since(t time.Time) time.Duration {
	return c.Now().Sub(t)
}

func (run *dockerConformanceRun) recordOutput() {
	defer close(run.done)
	for chunk := range run.session.Output() {
		if chunk.ReadError != nil && !errors.Is(chunk.ReadError, io.EOF) {
			run.mu.Lock()
			run.outputErr = chunk.ReadError
			run.mu.Unlock()
			return
		}
		if len(chunk.Bytes) == 0 {
			continue
		}
		run.mu.Lock()
		if _, err := run.rec.RecordOutput(chunk); err != nil {
			run.outputErr = err
			run.mu.Unlock()
			return
		}
		frame, err := run.emu.Feed(chunk.Bytes)
		if err != nil {
			run.outputErr = err
			run.mu.Unlock()
			return
		}
		frameRec, err := run.rec.RecordFrame(frame)
		if err != nil {
			run.outputErr = err
			run.mu.Unlock()
			return
		}
		frame.TMS = frameRec.TMS
		run.latest = frame
		run.frameSeen = true
		run.frames++
		run.rawBytes += int64(len(chunk.Bytes))
		run.mu.Unlock()
	}
}

func (run *dockerConformanceRun) waitForAnyText(timeout time.Duration, markers ...string) (terminal.Frame, string, error) {
	return run.waitForAnyTextAfter(-1, timeout, markers...)
}

func (run *dockerConformanceRun) waitForAnyTextAfter(afterMS int64, timeout time.Duration, markers ...string) (terminal.Frame, string, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		run.mu.Lock()
		frame := run.latest
		screen := strings.Join(frame.Text, "\n")
		ready := run.frameSeen && frame.TMS > afterMS
		for _, marker := range markers {
			if ready && strings.Contains(screen, marker) {
				run.mu.Unlock()
				return frame, marker, nil
			}
		}
		done := run.outputErr
		run.mu.Unlock()
		if done != nil {
			return terminal.Frame{}, "", done
		}
		if time.Now().After(deadline) {
			return terminal.Frame{}, "", fmt.Errorf("timed out waiting for %q after %dms; last screen:\n%s", markers, afterMS, run.screen())
		}
		<-ticker.C
	}
}

func (run *dockerConformanceRun) sendBytes(b []byte) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.session.SendBytes(b); err != nil {
		return err
	}
	_, err := run.rec.RecordInput(session.EventInput, b, "", nil, "")
	return err
}

func (run *dockerConformanceRun) sendKey(key session.Key) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.session.SendKey(key); err != nil {
		return err
	}
	_, err := run.rec.RecordInput(session.EventInput, nil, key, nil, "")
	return err
}

func (run *dockerConformanceRun) resize(size session.Size) (int64, error) {
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.session.Resize(size); err != nil {
		return 0, err
	}
	_, err := run.rec.RecordInput(session.EventResize, nil, "", &size, "")
	if err != nil {
		return 0, err
	}
	frame := run.emu.Resize(terminal.Size{Rows: int(size.Rows), Cols: int(size.Cols)})
	frameRec, err := run.rec.RecordFrame(frame)
	if err != nil {
		return 0, err
	}
	frame.TMS = frameRec.TMS
	run.latest = frame
	run.frameSeen = true
	run.frames++
	if err := run.recordServiceLocked(map[string]any{"type": "tui-conformance", "case": run.name, "interaction": "resize", "rows": size.Rows, "cols": size.Cols}); err != nil {
		return 0, err
	}
	return frame.TMS, nil
}

func (run *dockerConformanceRun) waitExit(timeout time.Duration) (session.ExitStatus, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ch := make(chan session.ExitStatus, 1)
	go func() { ch <- run.session.Wait() }()
	select {
	case status := <-ch:
		return status, nil
	case <-time.After(timeout):
		_ = run.session.Kill()
		status := <-ch
		return status, fmt.Errorf("timed out waiting for %s to exit", run.name)
	}
}

func (run *dockerConformanceRun) finish(status session.ExitStatus) error {
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		_ = run.session.Kill()
		return fmt.Errorf("timed out waiting for output drain")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.outputErr != nil {
		return run.outputErr
	}
	if err := run.recordServiceLocked(map[string]any{"type": "tui-conformance", "case": run.name, "stage": "final", "frames": run.frames, "raw_bytes": run.rawBytes}); err != nil {
		return err
	}
	exit := &session.ExitStatus{Code: status.Code, Signal: status.Signal, Exited: status.Exited, Signaled: status.Signaled}
	if err := run.rec.RecordFinal(cassette.FinalRecord{
		Exit:      exit,
		Metadata:  map[string]any{"case": run.name, "script": run.script, "image": dockerConformanceBaseImage, "frames": run.frames, "raw_bytes": run.rawBytes},
		FinalText: run.screenLocked(),
	}); err != nil {
		return err
	}
	if err := run.rec.WriteScrubReport(cassette.ScrubReport{
		Status:                 "clean",
		Rules:                  []string{"env-allowlist"},
		HitCounts:              map[string]int{},
		EnvAllowlist:           []string{"TERM", "LANG", "LC_ALL"},
		IntentionallyPreserved: []string{"docker-image-digest", "tui-fixture-text"},
	}); err != nil {
		return err
	}
	return run.rec.Close()
}

func (run *dockerConformanceRun) recordServiceLocked(payload any) error {
	_, err := run.rec.RecordServiceEvent(payload)
	return err
}

func (run *dockerConformanceRun) screen() string {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.screenLocked()
}

func (run *dockerConformanceRun) screenLocked() string {
	if !run.frameSeen {
		return "<no frame>"
	}
	return strings.Join(run.latest.Text, "\n")
}

func (run *dockerConformanceRun) close() {
	_ = run.session.Close()
}

func loadDockerConformanceScenario(t *testing.T, fixtures string, tc dockerConformanceCase, record dockerConformanceRecord) Scenario {
	t.Helper()
	scenario, err := LoadScenario(filepath.Join(fixtures, tc.scenario))
	require.NoError(t, err)
	scenario.CassettePath = record.dir
	scenario.ExpectedManifestID = record.manifest.ID
	scenario.ExpectedContentDigest = record.manifest.ContentDigest.SHA256
	emulator := record.manifest.Terminal.Emulator
	scenario.RequiredEmulator = &emulator

	switch tc.name {
	case "top":
		helpText := record.text["help_marker"]
		if helpText == "" {
			helpText = "Help"
		}
		scenario.Groups["screen"] = append(scenario.Groups["screen"],
			AssertionSpec{Name: "later-refresh", Type: "frame_eventually_contains", AtMS: int64Ptr(record.timings["refresh"]), WithinMS: 1500, Text: "top -"},
			AssertionSpec{Name: "help-screen", Type: "frame_eventually_contains", AtMS: int64Ptr(record.timings["help"]), WithinMS: 2000, Text: helpText},
			AssertionSpec{Name: "resized-frame", Type: "size_eventually", AtMS: int64Ptr(record.timings["resize"]), WithinMS: 500, Rows: 30, Cols: 100},
		)
	case "less":
		pageText := record.text["page_marker"]
		if pageText == "" {
			pageText = "less-line-030"
		}
		scenario.Groups["screen"] = append(scenario.Groups["screen"],
			AssertionSpec{Name: "page-after-input", Type: "frame_eventually_contains", AtMS: int64Ptr(record.timings["page"]), WithinMS: 1500, Text: pageText},
		)
	case "vim":
		scenario.Groups["screen"] = append(scenario.Groups["screen"],
			AssertionSpec{Name: "inserted-after-input", Type: "frame_eventually_contains", AtMS: int64Ptr(record.timings["inserted"]), WithinMS: 1500, Text: "DDX-typed-line"},
		)
	}
	return scenario
}

func requireConformanceEvidence(t *testing.T, result Result, dir string) {
	t.Helper()
	require.NotEmpty(t, result.Events)
	require.NotZero(t, result.Artifacts[cassette.OutputRawFile])
	raw, err := os.ReadFile(filepath.Join(dir, cassette.OutputRawFile))
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	var frames int
	for _, ev := range result.Events {
		if ev.Frame != nil {
			frames++
			require.NotEmpty(t, ev.Frame.Text)
			require.Greater(t, ev.Frame.Size.Rows, 0)
			require.Greater(t, ev.Frame.Size.Cols, 0)
		}
	}
	require.Greater(t, frames, 2)
}

func requireFrameSpread(t *testing.T, events []cassette.Event, text string, minSpreadMS int64) {
	t.Helper()
	var first, last *cassette.FrameRecord
	for _, ev := range events {
		if ev.Frame == nil || !strings.Contains(strings.Join(ev.Frame.Text, "\n"), text) {
			continue
		}
		frame := ev.Frame
		if first == nil {
			first = frame
		}
		last = frame
	}
	require.NotNil(t, first)
	require.NotNil(t, last)
	require.GreaterOrEqual(t, last.TMS-first.TMS, minSpreadMS)
}

func buildDockerConformanceImage(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker binary required for AGENT_PTY_INTEGRATION=1: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	dockerfile := []byte("FROM " + dockerConformanceBaseImage + "\n" +
		"ENV DEBIAN_FRONTEND=noninteractive TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8\n" +
		"RUN apt-get update && apt-get install -y --no-install-recommends procps less vim-tiny && rm -rf /var/lib/apt/lists/*\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), dockerfile, 0o600))
	cmd := exec.CommandContext(ctx, "docker", "build", "-q", "-t", dockerConformanceImage, dir) // #nosec G204 -- integration test intentionally invokes Docker.
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func dockerConformanceFixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Join(filepath.Dir(file), "testdata", "docker-conformance")
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	return abs
}

func currentGitSHA() string {
	cmd := exec.Command("git", "rev-parse", "--short=12", "HEAD") // #nosec G204 -- test provenance helper runs a fixed command.
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func int64Ptr(v int64) *int64 { return &v }
