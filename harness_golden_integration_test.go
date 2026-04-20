//go:build integration

package agent

import (
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

func TestHarnessGoldenReplay_ServiceExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replay cassette scripts are POSIX shell fixtures")
	}

	binDir := t.TempDir()
	writeGoldenHarnessScript(t, binDir, "claude", `#!/bin/sh
cat <<'EOF'
{"type":"assistant","message":{"content":[{"type":"text","text":"claude cassette final"}],"usage":{"input_tokens":11,"output_tokens":4}}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"result":"claude cassette final","usage":{"input_tokens":11,"output_tokens":4},"total_cost_usd":0.0001,"session_id":"cassette-claude"}
EOF
`)
	writeGoldenHarnessScript(t, binDir, "codex", `#!/bin/sh
cat <<'EOF'
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
		harness string
		text    string
		usage   bool
	}{
		{"claude", "claude cassette final", true},
		{"codex", "codex cassette final", true},
		{"pi", "pi cassette final", true},
		{"opencode", "opencode cassette final", false},
		{"gemini", "gemini cassette final", true},
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
			result, err := DrainExecute(ctx, events)
			if err != nil {
				t.Fatalf("DrainExecute: %v", err)
			}
			if result.FinalStatus != "success" {
				t.Fatalf("record mode %s failed before cassette write: status=%q error=%q", harness, result.FinalStatus, result.TerminalError)
			}
			cassette := map[string]any{
				"version":     1,
				"harness":     harness,
				"recorded_at": time.Now().UTC().Format(time.RFC3339),
				"result":      result,
			}
			data, err := json.MarshalIndent(cassette, "", "  ")
			if err != nil {
				t.Fatalf("marshal cassette: %v", err)
			}
			path := filepath.Join(cassetteDir, harness+".json")
			if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
				t.Fatalf("write cassette %s: %v", path, err)
			}
		})
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
