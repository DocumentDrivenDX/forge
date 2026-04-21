package ptytest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
	"github.com/DocumentDrivenDX/agent/internal/pty/terminal"
	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	now     time.Time
	elapsed time.Duration
	step    time.Duration
}

func (f *fakeClock) Now() time.Time { return f.now }

func (f *fakeClock) Since(time.Time) time.Duration {
	f.elapsed += f.step
	return f.elapsed
}

type terminalClock struct{}

func (terminalClock) Now() time.Time { return time.Unix(100, 0) }

func TestScenarioLoadRunAssertionsAndFailureContext(t *testing.T) {
	dir, manifest := writeWeirdCassette(t)
	specRoot := t.TempDir()
	home := filepath.Join(specRoot, "home")
	configRoot := filepath.Join(specRoot, "config")
	specPath := filepath.Join(specRoot, "scenario.yaml")
	spec := []byte(`name: weird
cassette_path: ` + dir + `
expected_manifest_id: weird-terminal
expected_content_digest: ` + manifest.ContentDigest.SHA256 + `
required_emulator:
  name: vt10x
  version: test
replay_mode: collapsed
terminal_size:
  rows: 6
  cols: 40
environment:
  home: ` + home + `
  config_root: ` + configRoot + `
  allowlist: [PATH]
  variables:
    PATH: /bin
    SECRET: ignored
expected_artifacts:
  - manifest.json
  - input.jsonl
  - output.raw
  - output.jsonl
  - frames.jsonl
  - service-events.jsonl
  - final.json
  - scrub-report.json
resolution_ms: 100
groups:
  frame:
    - {type: frame_contains, text: "after-clear"}
    - {type: frame_never_contains, text: "forbidden"}
    - {type: frame_eventually_contains, text: "wide:", within_ms: 1400}
    - {type: frame_stable_for, text: "stable", at_ms: 700, stable_for_ms: 200}
    - {type: cursor, at_ms: 5200, cursor_row: 2, cursor_col: 16, cursor_visible: true}
    - {type: size, at_ms: 5200, rows: 6, cols: 40}
    - {type: size_eventually, at_ms: 1000, within_ms: 4500, rows: 6, cols: 40}
    - {type: style, at_ms: 600, text: "red", fg: 1}
  streams:
    - {type: raw_output_order}
    - {type: input_bytes, bytes_hex: "7061737465640d"}
    - {type: paste_boundary, bytes_hex: "1b5b3230307e7061737465641b5b3230317e"}
    - {type: resize_order, before_kind: output, after_kind: frame}
    - {type: timing_gap, kind: frame, min_ms: 0, max_ms: 1500}
  service:
    - {type: service_json, json_path: "$.type", equals: "typed-drain"}
    - {type: final_metadata, metadata_key: "status", equals: "timeout", text: "timed out"}
    - {type: timeout_cancel_status, exit_code: -1, signaled: true}
`)
	require.NoError(t, os.WriteFile(specPath, spec, 0o600))

	scenario, err := LoadScenario(specPath)
	require.NoError(t, err)
	require.Equal(t, dir, scenario.CassettePath)
	result, err := RunScenario(context.Background(), scenario)
	require.NoError(t, err)
	require.NotEmpty(t, result.Events)
	require.Greater(t, result.Clock.NowMS(), int64(0))
	require.NotEmpty(t, result.Artifacts)
	require.Equal(t, home, result.Environment["HOME"])
	require.Equal(t, configRoot, result.Environment["XDG_CONFIG_HOME"])
	require.Equal(t, "/bin", result.Environment["PATH"])
	require.NotContains(t, result.Environment, "SECRET")

	scenario.Groups["frame"] = append(scenario.Groups["frame"], AssertionSpec{Name: "missing text", Type: "frame_contains", Text: "not-present"})
	result, err = RunScenario(context.Background(), scenario)
	require.Error(t, err)
	require.NotEmpty(t, result.Failures)
	require.Contains(t, err.Error(), "missing text")
	require.Contains(t, err.Error(), "screen:")
	require.Contains(t, err.Error(), "service:")
}

func TestScenarioBindingAndMetadataFailures(t *testing.T) {
	dir, manifest := writeWeirdCassette(t)
	_, err := RunScenario(context.Background(), Scenario{CassettePath: dir, ExpectedManifestID: "wrong", ExpectedContentDigest: manifest.ContentDigest.SHA256})
	require.ErrorContains(t, err, "manifest id mismatch")
	_, err = RunScenario(context.Background(), Scenario{CassettePath: dir, ExpectedManifestID: manifest.ID, ExpectedContentDigest: strings.Repeat("0", 64)})
	require.ErrorContains(t, err, "manifest digest mismatch")
	_, err = RunScenario(context.Background(), Scenario{CassettePath: dir, ExpectedManifestID: manifest.ID, ExpectedContentDigest: manifest.ContentDigest.SHA256, TerminalSize: &TerminalSize{Rows: 99, Cols: 99}})
	require.ErrorContains(t, err, "terminal size mismatch")
	_, err = RunScenario(context.Background(), Scenario{CassettePath: dir, ExpectedManifestID: manifest.ID, ExpectedContentDigest: manifest.ContentDigest.SHA256, ResolutionMS: 1})
	require.ErrorContains(t, err, "resolution mismatch")
}

func TestRealtimeScaledAndParallelReplay(t *testing.T) {
	dir, manifest := writeWeirdCassette(t)
	for _, mode := range []ReplayMode{ReplayRealtime, ReplayScaled, ReplayCollapsed} {
		scenario := Scenario{
			CassettePath:          dir,
			ExpectedManifestID:    manifest.ID,
			ExpectedContentDigest: manifest.ContentDigest.SHA256,
			ReplayMode:            mode,
			ReplayScale:           0.5,
			Groups:                map[string][]AssertionSpec{"final": {{Type: "exit_status", ExitCode: ptr(-1), Signaled: boolPtr(true)}}},
		}
		result, err := RunScenario(context.Background(), scenario)
		require.NoError(t, err)
		require.NotEmpty(t, result.Events)
	}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			home := filepath.Join(t.TempDir(), "home")
			scenario := Scenario{
				Name:                  "parallel",
				CassettePath:          dir,
				ExpectedManifestID:    manifest.ID,
				ExpectedContentDigest: manifest.ContentDigest.SHA256,
				ReplayMode:            ReplayCollapsed,
				Environment:           EnvironmentPolicy{Home: home, ConfigRoot: filepath.Join(home, ".config"), Allowlist: []string{"PATH"}},
				Groups:                map[string][]AssertionSpec{"service": {{Type: "service_json", JSONPath: "$.type", Equals: "typed-drain"}}},
			}
			_, err := RunScenario(context.Background(), scenario)
			if err != nil {
				t.Errorf("parallel scenario %d: %v", i, err)
			}
			if _, statErr := os.Stat(home); statErr != nil {
				t.Errorf("parallel scenario %d home not prepared: %v", i, statErr)
			}
		}(i)
	}
	wg.Wait()
}

func TestSyntheticFixtureCoversWeirdTerminalFamilies(t *testing.T) {
	dir, manifest := writeWeirdCassette(t)
	raw, err := os.ReadFile(filepath.Join(dir, cassette.OutputRawFile))
	require.NoError(t, err)
	for _, marker := range []string{
		"\x1b[?1049h",                  // alternate screen
		"\x1b[2J",                      // screen clear
		"\x1b]0;title",                 // OSC title
		"\x1b]8;;https://example.test", // OSC 8 hyperlink
		"\x1b]52;c;",                   // OSC 52 clipboard
		"\a",                           // bell
		"\x1b[?25$p",                   // DECRQM mode query
		"\x1b(0",                       // line drawing alternate charset
		"\x1b[?1006h",                  // SGR mouse mode
		"\x1b[I",                       // focus in
		"\x1b[O",                       // focus out
		"one-byte",
		"delayed-output",
		"eof-redraw",
	} {
		require.Contains(t, string(raw), marker)
	}
	result, err := RunScenario(context.Background(), Scenario{
		CassettePath:          dir,
		ExpectedManifestID:    manifest.ID,
		ExpectedContentDigest: manifest.ContentDigest.SHA256,
		ReplayMode:            ReplayCollapsed,
		Groups:                map[string][]AssertionSpec{"raw": {{Type: "raw_output_order"}}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Events)
}

func writeWeirdCassette(t *testing.T) (string, cassette.Manifest) {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{now: time.Unix(100, 0), step: 100 * time.Millisecond}
	rec, err := cassette.Create(dir, cassette.Manifest{
		ID:         "weird-terminal",
		Harness:    cassette.Harness{Name: "synthetic"},
		Command:    cassette.Command{Argv: []string{"synthetic"}, WorkdirPolicy: "tempdir", EnvAllowlist: []string{"PATH"}, TimeoutMS: 1000},
		Terminal:   cassette.Terminal{InitialRows: 6, InitialCols: 40, Emulator: cassette.Emulator{Name: "vt10x", Version: "test"}},
		Timing:     cassette.Timing{ResolutionMS: 100, ClockPolicy: "monotonic-elapsed", ReplayDefault: cassette.ReplayCollapsed},
		Provenance: cassette.Provenance{GitSHA: "test", ContractVersion: "CONTRACT-003"},
	}, cassette.WithClock(clock))
	require.NoError(t, err)

	emu := terminal.New(terminal.Size{Rows: 6, Cols: 40}, terminal.WithClock(terminalClock{}))
	recordInput(t, rec, []byte("pasted\r"), "", nil)
	recordInput(t, rec, []byte("\x1b[200~pasted\x1b[201~"), "", nil)

	feed := func(parts ...string) {
		t.Helper()
		for _, part := range parts {
			out, err := rec.RecordOutput(session.OutputChunk{Bytes: []byte(part)})
			require.NoError(t, err)
			require.NotZero(t, out.Seq)
			frame, err := emu.Feed([]byte(part))
			require.NoError(t, err)
			_, err = rec.RecordFrame(frame)
			require.NoError(t, err)
		}
	}

	feed(
		"\x1b[?1049h\x1b[2J\x1b[Hafter-",
		"clear\n\x1b[31mred\x1b[0m\nstable\n",
		"\x1b]0;title\x07\x1b]8;;https://example.test\x07link\x1b]8;;\x07",
		"\x1b]52;c;Y2xpcA==\x07\a\x1b[?25$p\x1b(0lqk\x1b(B\n",
		"\x1b[?1006h\x1b[I\x1b[Owide: 界e\u0301\n",
	)
	feed("\x1b[")
	size := session.Size{Rows: 6, Cols: 40}
	_, err = rec.RecordInput(session.EventResize, nil, "", &size, "")
	require.NoError(t, err)
	frame := emu.Resize(terminal.Size{Rows: 6, Cols: 40})
	_, err = rec.RecordFrame(frame)
	require.NoError(t, err)
	feed("2Jafter-clear\n")
	feedBytes(t, rec, emu, "one-byte\n")
	clock.step = 700 * time.Millisecond
	feed("delayed-output\n")
	clock.step = 100 * time.Millisecond
	feed("\x1b[3;11Hstable", "\x1b[?1049lno-newline prompt", strings.Repeat("x", 512), "eof-redraw", "final burst")

	_, err = rec.RecordServiceEvent(json.RawMessage(`{"type":"typed-drain","tool":{"name":"synthetic"},"status":"ok"}`))
	require.NoError(t, err)
	require.NoError(t, rec.RecordFinal(cassette.FinalRecord{
		Exit:      &session.ExitStatus{Code: -1, Signal: "killed", Signaled: true},
		Metadata:  map[string]any{"status": "timeout"},
		FinalText: "timed out during redraw",
	}))
	require.NoError(t, rec.WriteScrubReport(cassette.ScrubReport{Status: "clean", Rules: []string{"synthetic"}, HitCounts: map[string]int{}}))
	require.NoError(t, rec.Close())

	reader, err := cassette.Open(dir)
	require.NoError(t, err)
	return dir, reader.Manifest()
}

func recordInput(t *testing.T, rec *cassette.Recorder, b []byte, key session.Key, size *session.Size) {
	t.Helper()
	_, err := rec.RecordInput(session.EventInput, b, key, size, "")
	require.NoError(t, err)
}

func feedBytes(t *testing.T, rec *cassette.Recorder, emu *terminal.VT10x, text string) {
	t.Helper()
	for _, b := range []byte(text) {
		_, err := rec.RecordOutput(session.OutputChunk{Bytes: []byte{b}})
		require.NoError(t, err)
		frame, err := emu.Feed([]byte{b})
		require.NoError(t, err)
		_, err = rec.RecordFrame(frame)
		require.NoError(t, err)
	}
}

func ptr(v int) *int { return &v }

func boolPtr(v bool) *bool { return &v }
