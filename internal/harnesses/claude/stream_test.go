package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainEvents reads from out until the channel closes or ctx fires. It
// returns the recorded sequence so tests can assert on event types/data.
func drainEvents(t *testing.T, ctx context.Context, out <-chan harnesses.Event) []harnesses.Event {
	t.Helper()
	var got []harnesses.Event
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for events; collected %d so far", len(got))
		}
	}
}

// runParser feeds input through parseClaudeStream and returns the emitted
// events plus the aggregate. It runs the parser in a goroutine so the
// channel writes don't block on a sync receiver.
func runParser(t *testing.T, input string) ([]harnesses.Event, *streamAggregate) {
	t.Helper()
	out := make(chan harnesses.Event, 64)
	var seq int64
	type result struct {
		agg *streamAggregate
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		agg, err := parseClaudeStream(context.Background(), strings.NewReader(input), out, map[string]string{"bead_id": "ddx-12345678"}, &seq)
		close(out)
		resCh <- result{agg: agg, err: err}
	}()

	var events []harnesses.Event
	for ev := range out {
		events = append(events, ev)
	}
	r := <-resCh
	require.NoError(t, r.err)
	return events, r.agg
}

// TestParseClaudeStream_BehavioralParity feeds the same fixtures the DDx
// claude_stream_test.go uses through the agent-side parser and asserts
// that the emitted Event sequence matches the semantic shape callers
// expect per CONTRACT-003 §"Event JSON shapes".
func TestParseClaudeStream_BehavioralParity(t *testing.T) {
	cases := []struct {
		name             string
		input            string
		wantEventTypes   []harnesses.EventType
		wantToolCalls    int
		wantTurnCount    int
		wantInputTokens  int
		wantOutputTokens int
		wantCostUSD      float64
		wantFinalText    string
		wantSessionID    string
		wantModel        string
	}{
		{
			name: "full stream with tool use, tool result, and final result",
			input: strings.Join([]string{
				`{"type":"system","subtype":"init","session_id":"sess-abc","model":"claude-sonnet-4-6","tools":["Bash","Read"]}`,
				`{"type":"assistant","message":{"id":"m-1","model":"claude-sonnet-4-6","content":[{"type":"text","text":"Starting"},{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":120,"output_tokens":42}}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-1","content":"README.md\nfoo.go"}]}}`,
				`{"type":"assistant","message":{"id":"m-2","model":"claude-sonnet-4-6","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":260,"output_tokens":88}}}`,
				`{"type":"result","subtype":"success","is_error":false,"duration_ms":1200,"result":"All done.","usage":{"input_tokens":260,"output_tokens":88},"total_cost_usd":0.0123,"session_id":"sess-abc"}`,
			}, "\n"),
			// One text + one tool_call from m-1, one tool_result from user,
			// one text from m-2. result event emits no parser events (final
			// is built by the runner from the aggregate).
			wantEventTypes: []harnesses.EventType{
				harnesses.EventTypeTextDelta,
				harnesses.EventTypeToolCall,
				harnesses.EventTypeToolResult,
				harnesses.EventTypeTextDelta,
			},
			wantToolCalls:    1,
			wantTurnCount:    2,
			wantInputTokens:  260,
			wantOutputTokens: 88,
			wantCostUSD:      0.0123,
			wantFinalText:    "All done.",
			wantSessionID:    "sess-abc",
			wantModel:        "claude-sonnet-4-6",
		},
		{
			name: "garbage lines are skipped",
			input: strings.Join([]string{
				`not json`,
				`{"type":"system","subtype":"init","session_id":"sess-xyz","model":"claude-sonnet-4-6"}`,
				`{garbage`,
				`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu-2","name":"Read","input":{"path":"/tmp/x"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
				`{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":10,"output_tokens":5},"total_cost_usd":0.001,"session_id":"sess-xyz"}`,
			}, "\n"),
			wantEventTypes: []harnesses.EventType{
				harnesses.EventTypeToolCall,
			},
			wantToolCalls:    1,
			wantTurnCount:    1,
			wantInputTokens:  10,
			wantOutputTokens: 5,
			wantCostUSD:      0.001,
			wantFinalText:    "ok",
			wantSessionID:    "sess-xyz",
			wantModel:        "claude-sonnet-4-6",
		},
		{
			name: "text-only assistant emits a text_delta event",
			input: strings.Join([]string{
				`{"type":"system","subtype":"init","session_id":"sess-t","model":"claude-sonnet-4-6"}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":3,"output_tokens":2}}}`,
				`{"type":"result","subtype":"success","result":"hello","usage":{"input_tokens":3,"output_tokens":2},"total_cost_usd":0.0001,"session_id":"sess-t"}`,
			}, "\n"),
			wantEventTypes: []harnesses.EventType{
				harnesses.EventTypeTextDelta,
			},
			wantToolCalls:    0,
			wantTurnCount:    1,
			wantInputTokens:  3,
			wantOutputTokens: 2,
			wantCostUSD:      0.0001,
			wantFinalText:    "hello",
			wantSessionID:    "sess-t",
			wantModel:        "claude-sonnet-4-6",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			events, agg := runParser(t, tc.input)

			// Check the emitted Event sequence types.
			gotTypes := make([]harnesses.EventType, len(events))
			for i, ev := range events {
				gotTypes[i] = ev.Type
			}
			assert.Equal(t, tc.wantEventTypes, gotTypes, "event type sequence")

			// Aggregate state.
			assert.Equal(t, tc.wantToolCalls, agg.ToolCalls, "tool call count")
			assert.Equal(t, tc.wantTurnCount, agg.TurnCount, "turn count")
			assert.Equal(t, tc.wantInputTokens, agg.InputTokens, "input tokens")
			assert.Equal(t, tc.wantOutputTokens, agg.OutputTokens, "output tokens")
			assert.InDelta(t, tc.wantCostUSD, agg.CostUSD, 1e-9, "cost usd")
			assert.Equal(t, tc.wantFinalText, agg.FinalText, "final text")
			assert.Equal(t, tc.wantSessionID, agg.SessionID, "session id")
			assert.Equal(t, tc.wantModel, agg.Model, "model")
			assert.False(t, agg.IsError)

			// Sequence numbers should be monotonically increasing.
			for i := 1; i < len(events); i++ {
				assert.Greater(t, events[i].Sequence, events[i-1].Sequence, "sequence must increase")
			}
			// Metadata should be propagated onto every event.
			for _, ev := range events {
				assert.Equal(t, "ddx-12345678", ev.Metadata["bead_id"], "metadata propagated")
			}

			// Spot-check tool_call payload shape.
			for _, ev := range events {
				if ev.Type != harnesses.EventTypeToolCall {
					continue
				}
				var data harnesses.ToolCallData
				require.NoError(t, json.Unmarshal(ev.Data, &data))
				assert.NotEmpty(t, data.Name, "tool call has a name")
			}

			// Spot-check tool_result payload shape carries the tool_use id.
			for _, ev := range events {
				if ev.Type != harnesses.EventTypeToolResult {
					continue
				}
				var data harnesses.ToolResultData
				require.NoError(t, json.Unmarshal(ev.Data, &data))
				assert.Equal(t, "tu-1", data.ID, "tool_result preserves tool_use_id")
				assert.Contains(t, data.Output, "README.md", "tool_result preserves output content")
			}
		})
	}
}

// TestParseClaudeStream_Empty verifies the parser tolerates an empty stream
// (e.g. claude crashed before producing any events) and returns an empty but
// non-nil aggregate.
func TestParseClaudeStream_Empty(t *testing.T) {
	events, agg := runParser(t, "")
	assert.Empty(t, events)
	require.NotNil(t, agg)
	assert.Equal(t, 0, agg.TurnCount)
	assert.Equal(t, 0, agg.InputTokens)
	assert.Empty(t, agg.FinalText)
}

// TestParseClaudeStream_ToolResultBlocks handles the variant where claude
// encodes tool_result.content as an array of content blocks (rather than a
// plain string).
func TestParseClaudeStream_ToolResultBlocks(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s","model":"claude-sonnet-4-6"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{}}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-1","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]}]}}`,
		`{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":1,"output_tokens":1},"total_cost_usd":0.0,"session_id":"s"}`,
	}, "\n")
	events, _ := runParser(t, input)

	var found *harnesses.ToolResultData
	for _, ev := range events {
		if ev.Type != harnesses.EventTypeToolResult {
			continue
		}
		var data harnesses.ToolResultData
		require.NoError(t, json.Unmarshal(ev.Data, &data))
		found = &data
	}
	require.NotNil(t, found, "expected a tool_result event")
	assert.Contains(t, found.Output, "line1")
	assert.Contains(t, found.Output, "line2")
}

// TestClaudeStreamArgsUnsupported ensures the stderr-detection helper that
// drives fallback-to-legacy-args recognises the phrases we care about.
func TestClaudeStreamArgsUnsupported(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"error: unknown option '--output-format'", true},
		{"Error: unrecognized option --verbose", true},
		{"error: Invalid value for --output-format: stream-json", true},
		{"Usage: claude [options]\n\nerror: unknown argument", true},
		{"error: unknown flag: --output-format", true},
		{"rate limit exceeded", false},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, claudeStreamArgsUnsupported(tc.stderr), tc.stderr)
	}
}

// TestParseClaudeStream_CtxCancellation exercises the parser's ctx
// cancellation path: when the parent context fires mid-stream the parser
// returns ctx.Err() with the partial aggregate intact.
func TestParseClaudeStream_CtxCancellation(t *testing.T) {
	// Build a stream long enough that the parser is likely mid-loop when
	// we cancel. Drop event channel buffer to 0 so the first emit blocks
	// until we cancel.
	var lines []string
	lines = append(lines, `{"type":"system","subtype":"init","session_id":"s","model":"m"}`)
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"chunk-%d"}],"usage":{"input_tokens":1,"output_tokens":1}}}`, i))
	}
	input := strings.Join(lines, "\n")

	out := make(chan harnesses.Event) // unbuffered: first emit blocks
	var seq int64
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := parseClaudeStream(ctx, strings.NewReader(input), out, nil, &seq)
		errCh <- err
	}()

	// Wait for the first event then cancel; parser should then unblock and return ctx.Err().
	select {
	case <-out:
		// first event delivered; cancel and ensure parser exits.
	case <-time.After(2 * time.Second):
		t.Fatal("parser never produced an event")
	}
	cancel()

	// Drain remaining sends so the parser doesn't block.
	go func() {
		for range out {
		}
	}()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled), "expected ctx.Canceled, got %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("parser did not return after cancellation")
	}
}

// writeFakeClaudeBinary creates a shell script that mimics the claude CLI's
// stream-json output. The script ignores all arguments and prints a minimal
// but complete sequence of stream events so Runner.Execute has real bytes
// to parse and the progress log file ends up with content.
func writeFakeClaudeBinary(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-claude")
	script := `#!/bin/sh
cat <<'EOF'
{"type":"system","subtype":"init","session_id":"sess-fake","model":"claude-sonnet-4-6"}
{"type":"assistant","message":{"id":"m-1","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":5,"output_tokens":2}}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"result":"hello","usage":{"input_tokens":5,"output_tokens":2},"total_cost_usd":0.0001,"session_id":"sess-fake"}
EOF
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// TestRunnerExecute_HappyPath drives Runner.Execute against a fake claude
// binary and asserts the emitted events terminate in a final event with
// status=success and the parsed cost/tokens attached.
func TestRunnerExecute_HappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude binary relies on POSIX shell")
	}
	tmp := t.TempDir()
	binPath := writeFakeClaudeBinary(t, tmp)
	logDir := filepath.Join(tmp, "session-logs")

	r := &Runner{Binary: binPath, PromptMode: "stdin"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt:        "hi",
		SessionLogDir: logDir,
		SessionID:     "fake-session",
		Metadata:      map[string]string{"bead_id": "ddx-test"},
	})
	require.NoError(t, err)

	events := drainEvents(t, ctx, out)
	require.NotEmpty(t, events)

	// Last event must be the final.
	last := events[len(events)-1]
	assert.Equal(t, harnesses.EventTypeFinal, last.Type)
	var final harnesses.FinalData
	require.NoError(t, json.Unmarshal(last.Data, &final))
	assert.Equal(t, "success", final.Status)
	assert.Equal(t, 0, final.ExitCode)
	require.NotNil(t, final.Usage)
	assert.Equal(t, 5, final.Usage.InputTokens)
	assert.Equal(t, 2, final.Usage.OutputTokens)
	assert.InDelta(t, 0.0001, final.CostUSD, 1e-9)

	// Earlier events should include a text_delta.
	var sawText bool
	for _, ev := range events[:len(events)-1] {
		if ev.Type == harnesses.EventTypeTextDelta {
			sawText = true
		}
	}
	assert.True(t, sawText, "expected at least one text_delta before final")

	// Progress log should have been written.
	entries, err := os.ReadDir(logDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "session log dir should contain agent-*.jsonl")
}

// writeSlowFakeClaudeBinary emits an init event then sleeps so the test can
// cancel the context and verify the runner kills the subprocess. The script
// also installs a SIGTERM trap so we can confirm graceful termination.
func writeSlowFakeClaudeBinary(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "slow-claude")
	script := `#!/bin/sh
trap 'exit 0' TERM
echo '{"type":"system","subtype":"init","session_id":"s","model":"m"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"alive"}],"usage":{"input_tokens":1,"output_tokens":1}}}'
# Block forever waiting for a signal.
sleep 30 &
wait
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// TestRunnerExecute_CancellationReapsSubprocess verifies the PTY/orphan
// reaping path: when the parent ctx is cancelled mid-run, the runner
// signals the subprocess and Execute terminates with a final event that
// reflects cancellation rather than hanging.
func TestRunnerExecute_CancellationReapsSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude binary relies on POSIX shell")
	}
	tmp := t.TempDir()
	binPath := writeSlowFakeClaudeBinary(t, tmp)

	r := &Runner{Binary: binPath, PromptMode: "stdin"}
	ctx, cancel := context.WithCancel(context.Background())

	out, err := r.Execute(ctx, harnesses.ExecuteRequest{Prompt: "hi"})
	require.NoError(t, err)

	// Wait until at least one event arrives so we know the subprocess started.
	select {
	case <-out:
	case <-time.After(3 * time.Second):
		t.Fatal("subprocess never emitted an event")
	}

	// Cancel and confirm the channel closes within a bounded window.
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				// Channel closed — runner cleaned up.
				return
			}
			if ev.Type == harnesses.EventTypeFinal {
				var final harnesses.FinalData
				require.NoError(t, json.Unmarshal(ev.Data, &final))
				assert.Contains(t, []string{"cancelled", "timed_out", "failed"}, final.Status,
					"final status after cancel must reflect termination, got %s", final.Status)
			}
		case <-deadline:
			t.Fatal("runner did not terminate within 5s after cancel")
		}
	}
}

// TestRunnerInfo_PathResolution verifies Info reports a path when Binary
// is set, and falls back to PATH lookup otherwise.
func TestRunnerInfo_PathResolution(t *testing.T) {
	r := &Runner{Binary: "/absolutely/not/a/real/claude"}
	info := r.Info()
	assert.Equal(t, "claude", info.Name)
	assert.Equal(t, "subprocess", info.Type)
	assert.Equal(t, "/absolutely/not/a/real/claude", info.Path)
	// Available is path-only (no stat); HealthCheck would catch missing files.
	assert.True(t, info.Available)
	assert.Contains(t, info.SupportedPermissions, "safe")
}

// TestRunnerHealthCheck_MissingBinary returns an error when the configured
// Binary does not exist.
func TestRunnerHealthCheck_MissingBinary(t *testing.T) {
	r := &Runner{Binary: "/absolutely/not/a/real/claude"}
	err := r.HealthCheck(context.Background())
	require.Error(t, err)
}
