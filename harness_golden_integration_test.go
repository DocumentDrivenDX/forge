//go:build integration

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
)

const harnessCassetteRoot = "testdata/harness-cassettes"

func TestHarnessGoldenReplay_ServiceExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replay cassette scripts are POSIX shell fixtures")
	}

	binDir := t.TempDir()
	writeGoldenHarnessScript(t, binDir, "claude", `#!/bin/sh
cat <<'EOF'
{"type":"system","subtype":"init","session_id":"cassette-claude","model":"claude-sonnet-4-6","tools":["Bash","Read"]}
{"type":"assistant","message":{"id":"m-1","model":"claude-sonnet-4-6","content":[{"type":"text","text":"claude starting"},{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":11,"output_tokens":4}}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-1","content":"README.md\nservice.go"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"claude cassette final"}],"usage":{"input_tokens":13,"output_tokens":6}}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"result":"claude cassette final","usage":{"input_tokens":13,"output_tokens":6},"total_cost_usd":0.0001,"session_id":"cassette-claude"}
EOF
`)
	writeGoldenHarnessScript(t, binDir, "codex", `#!/bin/sh
cat <<'EOF'
{"type":"item.started","item":{"id":"codex_cmd_1","type":"command_execution","command":"/bin/sh -lc \"printf codex-tool\"","status":"in_progress"}}
{"type":"item.completed","item":{"id":"codex_cmd_1","type":"command_execution","command":"/bin/sh -lc \"printf codex-tool\"","aggregated_output":"codex-tool","exit_code":0,"status":"completed"}}
{"type":"output","item":{"type":"agent_message","text":"codex cassette final"}}
{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":5}}
EOF
`)
	writeGoldenHarnessScript(t, binDir, "pi", `#!/bin/sh
cat <<'EOF'
{"type":"text_delta","partial":{"usage":{"input":3,"output":2,"cost":{"total":0.001}}}}
{"type":"text_end","message":{"usage":{"input":3,"output":2,"cost":{"total":0.001}}},"response":"pi cassette final"}
EOF
`)
	writeGoldenHarnessScript(t, binDir, "opencode", `#!/bin/sh
cat <<'EOF'
opencode cassette final
EOF
`)
	writeGoldenHarnessScript(t, binDir, "gemini", `#!/bin/sh
cat <<'EOF'
gemini cassette final
{"stats":{"models":{"gemini":{"tokens":{"input":2,"total":5}}}}}
EOF
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	claudeCache := filepath.Join(stateDir, "claude-quota.json")
	codexCache := filepath.Join(stateDir, "codex-quota.json")
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", claudeCache)
	t.Setenv("DDX_AGENT_CODEX_QUOTA_CACHE", codexCache)
	writeGoldenQuotaCaches(t, claudeCache, codexCache)

	svc, err := New(ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	assertGoldenCapabilities(t, svc)

	cases := []struct {
		harness  string
		text     string
		usage    bool
		toolName string
	}{
		{"claude", "claude cassette final", true, "Bash"},
		{"codex", "codex cassette final", true, "command_execution"},
		{"pi", "pi cassette final", true, ""},
		{"opencode", "opencode cassette final", false, ""},
		{"gemini", "gemini cassette final", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			events, err := svc.Execute(ctx, ServiceExecuteRequest{
				Prompt:      "golden replay prompt",
				Harness:     tc.harness,
				Model:       "cassette-model",
				WorkDir:     t.TempDir(),
				Permissions: "safe",
				Reasoning:   ReasoningLow,
				Metadata: map[string]string{
					"mode":       "replay",
					"cassette":   tc.harness,
					"bead_id":    "agent-5d5c2d13",
					"test_suite": "harness-golden",
				},
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			result, err := DrainExecute(ctx, events)
			if err != nil {
				t.Fatalf("DrainExecute: %v", err)
			}
			if result.FinalStatus != "success" {
				t.Fatalf("FinalStatus: got %q (err=%q)", result.FinalStatus, result.TerminalError)
			}
			if !strings.Contains(result.FinalText, tc.text) {
				t.Fatalf("FinalText: got %q, want to contain %q", result.FinalText, tc.text)
			}
			if result.RoutingActual == nil || result.RoutingActual.Harness != tc.harness {
				t.Fatalf("RoutingActual: got %#v, want harness %q", result.RoutingActual, tc.harness)
			}
			if result.RoutingDecision == nil || result.RoutingDecision.SessionID == "" {
				t.Fatalf("RoutingDecision missing session_id: %#v", result.RoutingDecision)
			}
			if len(result.TextDeltas) == 0 {
				t.Fatal("expected replay progress text_delta events")
			}
			if tc.usage && result.Usage == nil {
				t.Fatal("expected usage from replay cassette")
			}
			for _, ev := range result.Events {
				if ev.Metadata["mode"] != "replay" || ev.Metadata["cassette"] != tc.harness || ev.Metadata["test_suite"] != "harness-golden" {
					t.Fatalf("event metadata not echoed for %s event: %#v", ev.Type, ev.Metadata)
				}
			}
			if tc.toolName != "" {
				if len(result.ToolCalls) != 1 || len(result.ToolResults) != 1 {
					t.Fatalf("expected one tool_call and one tool_result, got calls=%v results=%v", result.ToolCalls, result.ToolResults)
				}
				if result.ToolCalls[0].Name != tc.toolName {
					t.Fatalf("tool call name: got %q, want %q", result.ToolCalls[0].Name, tc.toolName)
				}
				if result.ToolResults[0].ID != result.ToolCalls[0].ID {
					t.Fatalf("tool result ID %q does not match call ID %q", result.ToolResults[0].ID, result.ToolCalls[0].ID)
				}
				wantOutput := "README.md"
				if tc.harness == "codex" {
					wantOutput = "codex-tool"
				}
				if !strings.Contains(result.ToolResults[0].Output, wantOutput) {
					t.Fatalf("tool result output: got %q, want to contain %q", result.ToolResults[0].Output, wantOutput)
				}
			}
		})
	}
}

func TestHarnessGoldenReplay_ServiceExecuteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replay cassette scripts are POSIX shell fixtures")
	}

	binDir := t.TempDir()
	writeGoldenHarnessScript(t, binDir, "codex", `#!/bin/sh
echo "synthetic codex failure" >&2
exit 7
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	svc, err := New(ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := svc.Execute(ctx, ServiceExecuteRequest{
		Prompt:      "golden failure prompt",
		Harness:     "codex",
		Model:       "cassette-model",
		WorkDir:     t.TempDir(),
		Permissions: "safe",
		Metadata: map[string]string{
			"mode":       "replay",
			"cassette":   "codex-failure",
			"test_suite": "harness-golden",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	result, err := DrainExecute(ctx, events)
	if err != nil {
		t.Fatalf("DrainExecute: %v", err)
	}
	if result.FinalStatus != "failed" {
		t.Fatalf("FinalStatus: got %q, want failed", result.FinalStatus)
	}
	if !strings.Contains(result.TerminalError, "exit status 7") {
		t.Fatalf("TerminalError: got %q", result.TerminalError)
	}
	if result.RoutingActual == nil || result.RoutingActual.Harness != "codex" {
		t.Fatalf("RoutingActual: got %#v, want codex", result.RoutingActual)
	}
	for _, ev := range result.Events {
		if ev.Metadata["cassette"] != "codex-failure" {
			t.Fatalf("event metadata not echoed for %s event: %#v", ev.Type, ev.Metadata)
		}
	}
}

func TestHarnessGoldenCassetteReplay_ServiceEvents(t *testing.T) {
	for _, tc := range []struct {
		harness  string
		toolName string
		text     string
	}{
		{harness: "codex", toolName: "command_execution", text: "codex cassette final"},
		{harness: "claude", toolName: "Bash", text: "claude cassette final"},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			dir := filepath.Join(harnessCassetteRoot, tc.harness)
			requireCassetteArtifacts(t, dir)
			events := readCassetteServiceEvents(t, filepath.Join(dir, "service-events.jsonl"))
			ch := make(chan ServiceEvent, len(events))
			for _, ev := range events {
				ch <- ev
			}
			close(ch)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := DrainExecute(ctx, ch)
			if err != nil {
				t.Fatalf("DrainExecute: %v", err)
			}
			if result.FinalStatus != "success" {
				t.Fatalf("FinalStatus: got %q (err=%q)", result.FinalStatus, result.TerminalError)
			}
			if !strings.Contains(result.FinalText, tc.text) {
				t.Fatalf("FinalText: got %q, want %q", result.FinalText, tc.text)
			}
			if result.Usage == nil {
				t.Fatal("expected usage in cassette final")
			}
			if len(result.ToolCalls) != 1 || len(result.ToolResults) != 1 {
				t.Fatalf("expected one tool call/result, got calls=%v results=%v", result.ToolCalls, result.ToolResults)
			}
			if result.ToolCalls[0].Name != tc.toolName {
				t.Fatalf("tool name: got %q, want %q", result.ToolCalls[0].Name, tc.toolName)
			}
			if result.ToolResults[0].ID != result.ToolCalls[0].ID {
				t.Fatalf("tool result ID %q does not match call ID %q", result.ToolResults[0].ID, result.ToolCalls[0].ID)
			}
			for _, ev := range result.Events {
				if ev.Metadata["cassette"] != tc.harness || ev.Metadata["test_suite"] != "harness-golden" {
					t.Fatalf("event metadata not echoed for %s: %#v", ev.Type, ev.Metadata)
				}
			}
		})
	}
}

func TestHarnessGoldenRecordModePreflight(t *testing.T) {
	if os.Getenv("AGENT_HARNESS_RECORD") != "1" {
		t.Skip("set AGENT_HARNESS_RECORD=1 to run live harness record-mode preflight")
	}
	preflightLiveHarnessRecordMode(t)
}

func TestHarnessGoldenRecordModeLive(t *testing.T) {
	if os.Getenv("AGENT_HARNESS_RECORD") != "1" {
		t.Skip("set AGENT_HARNESS_RECORD=1 to run live harness record mode")
	}
	preflightLiveHarnessRecordMode(t)

	cassetteDir := os.Getenv("AGENT_HARNESS_CASSETTE_DIR")
	if cassetteDir == "" {
		cassetteDir = t.TempDir()
	}
	if err := os.MkdirAll(cassetteDir, 0o750); err != nil {
		t.Fatalf("create cassette dir: %v", err)
	}
	svc, err := New(ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			events, err := svc.Execute(ctx, ServiceExecuteRequest{
				Prompt:      "Reply with exactly: harness golden record ok",
				Harness:     harness,
				WorkDir:     t.TempDir(),
				Permissions: "safe",
				Reasoning:   ReasoningLow,
				Timeout:     90 * time.Second,
				Metadata: map[string]string{
					"mode":       "record",
					"cassette":   harness,
					"bead_id":    "agent-5d5c2d13",
					"test_suite": "harness-golden",
				},
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			rawEvents, result, err := drainRawExecute(ctx, events)
			if err != nil {
				t.Fatalf("DrainExecute: %v", err)
			}
			if result.FinalStatus != "success" {
				t.Fatalf("record mode %s failed before cassette write: status=%q error=%q", harness, result.FinalStatus, result.TerminalError)
			}
			writeVersionedHarnessCassette(t, cassetteDir, harness, ServiceExecuteRequest{
				Prompt:      "Reply with exactly: harness golden record ok",
				Harness:     harness,
				WorkDir:     "<tempdir>",
				Permissions: "safe",
				Reasoning:   ReasoningLow,
				Timeout:     90 * time.Second,
				Metadata: map[string]string{
					"mode":       "record",
					"cassette":   harness,
					"test_suite": "harness-golden",
				},
			}, rawEvents, result)
		})
	}
}

func drainRawExecute(ctx context.Context, events <-chan ServiceEvent) ([]ServiceEvent, *DrainExecuteResult, error) {
	result := &DrainExecuteResult{}
	var raw []ServiceEvent
	for {
		select {
		case <-ctx.Done():
			return raw, result, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				if result.Final == nil {
					return raw, result, context.Canceled
				}
				return raw, result, nil
			}
			raw = append(raw, ev)
			decoded, err := DecodeServiceEvent(ev)
			if err != nil {
				return raw, result, err
			}
			result.append(decoded)
		}
	}
}

func requireCassetteArtifacts(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"manifest.json", "input.json", "frames.jsonl", "service-events.jsonl", "final.json", "quota.json", "scrub-report.json"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("cassette artifact %s: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("cassette artifact %s is empty", path)
		}
	}
}

func readCassetteServiceEvents(t *testing.T, path string) []ServiceEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open service events: %v", err)
	}
	defer f.Close()

	var events []ServiceEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev ServiceEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("decode service event %s: %v", scanner.Text(), err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan service events: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("no service events in %s", path)
	}
	return events
}

func writeVersionedHarnessCassette(t *testing.T, cassetteRoot, harness string, req ServiceExecuteRequest, events []ServiceEvent, result *DrainExecuteResult) {
	t.Helper()
	dir := filepath.Join(cassetteRoot, harness)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("create cassette dir: %v", err)
	}
	writeCassetteJSON(t, filepath.Join(dir, "manifest.json"), map[string]any{
		"version":     1,
		"harness":     harness,
		"accepted":    true,
		"recorded_at": time.Now().UTC().Format(time.RFC3339),
		"command": map[string]any{
			"workdir_policy":  "tempdir",
			"env_allowlist":   []string{"PATH"},
			"timeout_ms":      req.Timeout.Milliseconds(),
			"permission_mode": req.Permissions,
		},
	})
	writeCassetteJSON(t, filepath.Join(dir, "input.json"), map[string]any{
		"prompt":      req.Prompt,
		"reasoning":   string(req.Reasoning),
		"permissions": req.Permissions,
		"metadata":    req.Metadata,
	})
	writeCassetteJSON(t, filepath.Join(dir, "final.json"), result.Final)
	writeCassetteJSON(t, filepath.Join(dir, "quota.json"), map[string]any{
		"source": "record-mode",
		"status": "captured-by-harness-status",
	})
	writeCassetteJSON(t, filepath.Join(dir, "scrub-report.json"), map[string]any{
		"status":     "clean",
		"redactions": []string{},
		"checked":    []string{"service-events", "metadata"},
	})
	writeCassetteJSONL(t, filepath.Join(dir, "service-events.jsonl"), events)
	writeCassetteFrames(t, filepath.Join(dir, "frames.jsonl"), events)
}

func writeCassetteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeCassetteJSONL(t *testing.T, path string, values []ServiceEvent) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, value := range values {
		if err := enc.Encode(value); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func writeCassetteFrames(t *testing.T, path string, events []ServiceEvent) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal frame: %v", err)
		}
		if err := enc.Encode(map[string]any{
			"delta_ms": i,
			"stream":   "service_event",
			"data":     string(raw) + "\n",
		}); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func preflightLiveHarnessRecordMode(t *testing.T) {
	t.Helper()
	for _, binary := range []string{"claude", "codex", "pi", "opencode"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatalf("record mode requires %s in PATH: %v", binary, err)
		}
	}
	for _, binary := range []string{"tmux"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatalf("record mode quota preflight requires %s in PATH until direct PTY quota capture lands: %v", binary, err)
		}
	}
}

func assertGoldenCapabilities(t *testing.T, svc DdxAgent) {
	t.Helper()
	list, err := svc.ListHarnesses(context.Background())
	if err != nil {
		t.Fatalf("ListHarnesses: %v", err)
	}
	byName := make(map[string]HarnessInfo, len(list))
	for _, h := range list {
		byName[h.Name] = h
	}
	for _, name := range []string{"claude", "codex", "pi", "opencode"} {
		h, ok := byName[name]
		if !ok {
			t.Fatalf("capability matrix missing %s", name)
		}
		if h.CapabilityMatrix.ExecutePrompt.Status != HarnessCapabilityRequired {
			t.Fatalf("%s ExecutePrompt capability: %#v", name, h.CapabilityMatrix.ExecutePrompt)
		}
		if h.CapabilityMatrix.ProgressEvents.Status != HarnessCapabilityRequired {
			t.Fatalf("%s ProgressEvents capability: %#v", name, h.CapabilityMatrix.ProgressEvents)
		}
	}
	for _, name := range []string{"claude", "codex"} {
		h := byName[name]
		if h.Quota == nil || h.Quota.Status != "ok" || !h.Quota.Fresh {
			t.Fatalf("%s quota status: %#v", name, h.Quota)
		}
	}
}

func writeGoldenQuotaCaches(t *testing.T, claudePath, codexPath string) {
	t.Helper()
	now := time.Now().UTC()
	if err := claudeharness.WriteClaudeQuota(claudePath, claudeharness.ClaudeQuotaSnapshot{
		CapturedAt:        now,
		FiveHourRemaining: 90,
		FiveHourLimit:     100,
		WeeklyRemaining:   90,
		WeeklyLimit:       100,
		Source:            "cassette",
	}); err != nil {
		t.Fatalf("WriteClaudeQuota: %v", err)
	}
	if err := codexharness.WriteCodexQuota(codexPath, codexharness.CodexQuotaSnapshot{
		CapturedAt: now,
		Source:     "cassette",
		Windows: []harnesses.QuotaWindow{
			{Name: "5h", LimitID: "codex", WindowMinutes: 300, UsedPercent: 10, State: "ok"},
		},
	}); err != nil {
		t.Fatalf("WriteCodexQuota: %v", err)
	}
}

func writeGoldenHarnessScript(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write harness cassette script %s: %v", name, err)
	}
}
