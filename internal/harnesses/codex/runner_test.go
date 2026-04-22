package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

func TestRunner_Info(t *testing.T) {
	r := &Runner{}
	info := r.Info()
	if info.Name != "codex" {
		t.Errorf("expected name=codex, got %q", info.Name)
	}
	if info.Type != "subprocess" {
		t.Errorf("expected type=subprocess, got %q", info.Type)
	}
}

func TestRunner_HealthCheck_NoBinary(t *testing.T) {
	r := &Runner{Binary: "/nonexistent/codex-binary-xyz"}
	err := r.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestRunner_Execute_AppliesRequestControls(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "capture.txt")
	workDir := filepath.Join(dir, "work")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
{
  pwd
  i=0
  for arg in "$@"; do
    printf 'ARG[%%s]=%%s\n' "$i" "$arg"
    i=$((i + 1))
  done
} > %q
cat <<'EOF'
{"type":"output","item":{"type":"agent_message","text":"ok"}}
{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2}}
EOF
`, capture)
	binary := filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{Binary: binary}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt:      "hello prompt",
		Model:       "gpt-5.4",
		Reasoning:   "high",
		WorkDir:     workDir,
		Permissions: "unrestricted",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range ch {
	}

	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		workDir,
		"ARG[0]=exec",
		"ARG[1]=--json",
		"ARG[2]=--dangerously-bypass-approvals-and-sandbox",
		"ARG[3]=-C",
		"ARG[4]=" + workDir,
		"ARG[5]=-m",
		"ARG[6]=gpt-5.4",
		"ARG[7]=-c",
		"ARG[8]=reasoning.effort=high",
		"ARG[9]=hello prompt",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("capture missing %q:\n%s", want, got)
		}
	}
}

// TestRunner_Execute_HappyPath runs a fake script that emits codex-style JSONL.
func TestRunner_Execute_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// Write a fake "codex" script.
	script := `#!/bin/sh
cat <<'EOF'
{"type":"output","item":{"type":"agent_message","text":"hello from codex"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}
EOF
`
	f, err := os.CreateTemp("", "fake-codex-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{
		Binary:   f.Name(),
		BaseArgs: []string{}, // skip "exec --json" so the script runs cleanly
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt: "test prompt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var textDeltas []string
	var finalEv *harnesses.FinalData
	for ev := range ch {
		switch ev.Type {
		case harnesses.EventTypeTextDelta:
			var d harnesses.TextDeltaData
			if err := json.Unmarshal(ev.Data, &d); err != nil {
				t.Errorf("unmarshal text_delta: %v", err)
			}
			textDeltas = append(textDeltas, d.Text)
		case harnesses.EventTypeFinal:
			var fd harnesses.FinalData
			if err := json.Unmarshal(ev.Data, &fd); err != nil {
				t.Errorf("unmarshal final: %v", err)
			}
			finalEv = &fd
		}
	}

	if len(textDeltas) == 0 {
		t.Error("expected at least one text_delta event")
	} else if !strings.Contains(textDeltas[0], "hello from codex") {
		t.Errorf("unexpected text delta: %q", textDeltas[0])
	}

	if finalEv == nil {
		t.Fatal("no final event received")
	}
	if finalEv.Status != "success" {
		t.Errorf("expected status=success, got %q (error: %s)", finalEv.Status, finalEv.Error)
	}
	if finalEv.FinalText != "hello from codex" {
		t.Errorf("expected FinalText=%q, got %q", "hello from codex", finalEv.FinalText)
	}
	if finalEv.Usage == nil {
		t.Error("expected usage in final event")
	} else {
		if finalEv.Usage.InputTokens == nil || *finalEv.Usage.InputTokens != 10 {
			t.Errorf("expected InputTokens=10, got %#v", finalEv.Usage.InputTokens)
		}
		if finalEv.Usage.OutputTokens == nil || *finalEv.Usage.OutputTokens != 5 {
			t.Errorf("expected OutputTokens=5, got %#v", finalEv.Usage.OutputTokens)
		}
		if finalEv.Usage.TotalTokens == nil || *finalEv.Usage.TotalTokens != 15 {
			t.Errorf("expected TotalTokens=15, got %#v", finalEv.Usage.TotalTokens)
		}
	}
}

func TestRunner_Execute_UpdatesQuotaCacheFromTokenCountRateLimits(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	t.Setenv(codexQuotaCacheEnv, filepath.Join(dir, "codex-quota.json"))
	t.Setenv(codexAuthPathEnv, filepath.Join(dir, "missing-auth.json"))

	script := `#!/bin/sh
cat <<'EOF'
{"type":"event_msg","timestamp":"2026-04-22T02:00:00Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11},"rate_limits":{"plan_type":"pro","primary":{"used_percent":6,"window_minutes":300,"resets_at":1776840333,"limit_id":"codex","limit_name":"primary credits"}}}}}
{"type":"output","item":{"type":"agent_message","text":"ok"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}
EOF
`
	binary := filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{Binary: binary, BaseArgs: []string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range ch {
	}

	snap, ok := ReadCodexQuota()
	if !ok {
		t.Fatal("expected token_count rate_limits to update quota cache")
	}
	if snap.Source != "codex_exec_token_count" {
		t.Fatalf("Source: got %q", snap.Source)
	}
	if !snap.CapturedAt.Equal(time.Date(2026, 4, 22, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("CapturedAt: got %s", snap.CapturedAt.Format(time.RFC3339Nano))
	}
	if snap.Account == nil || snap.Account.PlanType != "ChatGPT Pro" {
		t.Fatalf("Account: got %#v", snap.Account)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].LimitName != "primary credits" || snap.Windows[0].UsedPercent != 6 {
		t.Fatalf("Windows: got %#v", snap.Windows)
	}
}

func TestParseCodexStream_EventTypes(t *testing.T) {
	input := `{"type":"output","item":{"type":"agent_message","text":"the answer"}}
{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":7}}
{"type":"other_event","foo":"bar"}
`
	out := make(chan harnesses.Event, 16)
	var seq int64
	agg, err := parseCodexStream(context.Background(), strings.NewReader(input), out, nil, &seq)
	close(out)
	if err != nil {
		t.Fatalf("parseCodexStream: %v", err)
	}
	if agg.FinalText != "the answer" {
		t.Errorf("expected FinalText=%q, got %q", "the answer", agg.FinalText)
	}
	usage, warnings := harnesses.ResolveFinalUsage(agg.UsageSources)
	if len(warnings) != 0 {
		t.Fatalf("usage warnings: %#v", warnings)
	}
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 20 {
		t.Errorf("expected InputTokens=20, got %#v", usage)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 7 {
		t.Errorf("expected OutputTokens=7, got %#v", usage)
	}

	var events []harnesses.Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != harnesses.EventTypeTextDelta {
		t.Errorf("expected text_delta, got %q", events[0].Type)
	}
}

func TestParseCodexStream_ItemCompletedFinalText(t *testing.T) {
	input := `{"type":"item.completed","item":{"type":"message","content":[{"type":"output_text","text":"APPROVE\nLooks good."}]}}
{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":3}}
`
	out := make(chan harnesses.Event, 16)
	var seq int64
	agg, err := parseCodexStream(context.Background(), strings.NewReader(input), out, nil, &seq)
	close(out)
	if err != nil {
		t.Fatalf("parseCodexStream: %v", err)
	}
	if agg.FinalText != "APPROVE\nLooks good." {
		t.Fatalf("FinalText: got %q", agg.FinalText)
	}
	if strings.Contains(agg.FinalText, "item.completed") || strings.Contains(agg.FinalText, `"content"`) {
		t.Fatalf("FinalText leaked raw codex frame: %q", agg.FinalText)
	}
	if got := reviewerVerdictFromFinalText(agg.FinalText); got != "APPROVE" {
		t.Fatalf("verdict from FinalText: got %q, want APPROVE", got)
	}
}

func TestParseCodexStream_CommandExecutionToolEvents(t *testing.T) {
	input, err := os.ReadFile("testdata/tool_events.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	out := make(chan harnesses.Event, 16)
	var seq int64
	agg, err := parseCodexStream(context.Background(), strings.NewReader(string(input)), out, nil, &seq)
	close(out)
	if err != nil {
		t.Fatalf("parseCodexStream: %v", err)
	}
	if agg.FinalText != "codex final" {
		t.Fatalf("FinalText: got %q", agg.FinalText)
	}
	usage, warnings := harnesses.ResolveFinalUsage(agg.UsageSources)
	if len(warnings) != 0 || usage == nil || usage.InputTokens == nil || usage.OutputTokens == nil || *usage.InputTokens != 12 || *usage.OutputTokens != 5 {
		t.Fatalf("usage: got usage=%#v warnings=%#v", usage, warnings)
	}

	var events []harnesses.Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) != 3 {
		t.Fatalf("events: got %d, want 3", len(events))
	}
	if events[0].Type != harnesses.EventTypeToolCall {
		t.Fatalf("event[0] type: got %q", events[0].Type)
	}
	var call harnesses.ToolCallData
	if err := json.Unmarshal(events[0].Data, &call); err != nil {
		t.Fatalf("unmarshal tool_call: %v", err)
	}
	if call.ID != "item_1" || call.Name != "command_execution" {
		t.Fatalf("tool_call: got %+v", call)
	}
	if !strings.Contains(string(call.Input), "/bin/sh -lc") {
		t.Fatalf("tool_call input: %s", string(call.Input))
	}

	if events[1].Type != harnesses.EventTypeToolResult {
		t.Fatalf("event[1] type: got %q", events[1].Type)
	}
	var result harnesses.ToolResultData
	if err := json.Unmarshal(events[1].Data, &result); err != nil {
		t.Fatalf("unmarshal tool_result: %v", err)
	}
	if result.ID != call.ID {
		t.Fatalf("tool_result ID: got %q, want %q", result.ID, call.ID)
	}
	if result.Output != "codex-tool" || result.Error != "" {
		t.Fatalf("tool_result: got %+v", result)
	}

	if events[2].Type != harnesses.EventTypeTextDelta {
		t.Fatalf("event[2] type: got %q", events[2].Type)
	}
}

func TestParseCodexStream_CommandExecutionFailure(t *testing.T) {
	input := `{"type":"item.started","item":{"id":"item_fail","type":"command_execution","command":"false","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_fail","type":"command_execution","command":"false","aggregated_output":"boom","exit_code":2,"status":"completed"}}
`
	out := make(chan harnesses.Event, 16)
	var seq int64
	if _, err := parseCodexStream(context.Background(), strings.NewReader(input), out, nil, &seq); err != nil {
		t.Fatalf("parseCodexStream: %v", err)
	}
	close(out)
	var events []harnesses.Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("events: got %d, want 2", len(events))
	}
	var result harnesses.ToolResultData
	if err := json.Unmarshal(events[1].Data, &result); err != nil {
		t.Fatalf("unmarshal tool_result: %v", err)
	}
	if result.Error != "exit status 2" {
		t.Fatalf("tool_result error: got %q", result.Error)
	}
	if result.Output != "boom" {
		t.Fatalf("tool_result output: got %q", result.Output)
	}
}

func TestParseCodexStream_UsageCassettes(t *testing.T) {
	cases := []struct {
		name             string
		wantUsage        bool
		wantInput        int
		wantOutput       int
		wantCache        int
		wantReasoning    int
		wantSource       string
		wantRateLimits   int
		wantMalformed    bool
		wantDisagreement bool
	}{
		{name: "present", wantUsage: true, wantInput: 12, wantOutput: 4, wantCache: 5, wantReasoning: 2, wantSource: harnesses.UsageSourceNativeStream},
		{name: "absent"},
		{name: "malformed", wantMalformed: true},
		{name: "disagree", wantUsage: true, wantInput: 30, wantOutput: 6, wantSource: harnesses.UsageSourceNativeStream, wantDisagreement: true},
		{name: "token_count_only", wantUsage: true, wantInput: 11, wantOutput: 3, wantCache: 8, wantReasoning: 1, wantSource: harnesses.UsageSourceNativeTokenCount, wantRateLimits: 1},
		{name: "token_count_with_turn", wantUsage: true, wantInput: 20, wantOutput: 4, wantSource: harnesses.UsageSourceNativeStream, wantRateLimits: 1, wantDisagreement: true},
		{name: "token_count_malformed", wantMalformed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "usage_cassettes", tc.name+".jsonl"))
			if err != nil {
				t.Fatalf("read cassette: %v", err)
			}
			out := make(chan harnesses.Event, 16)
			var seq int64
			agg, err := parseCodexStream(context.Background(), strings.NewReader(string(data)), out, nil, &seq)
			close(out)
			if err != nil {
				t.Fatalf("parseCodexStream: %v", err)
			}
			usage, warnings := harnesses.ResolveFinalUsage(agg.UsageSources)
			if !tc.wantUsage {
				if usage != nil {
					t.Fatalf("usage: got %#v, want nil", usage)
				}
			} else {
				if usage == nil || usage.InputTokens == nil || *usage.InputTokens != tc.wantInput {
					t.Fatalf("input usage: got %#v", usage)
				}
				if usage.OutputTokens == nil || *usage.OutputTokens != tc.wantOutput {
					t.Fatalf("output usage: got %#v", usage)
				}
				if tc.wantSource != "" && usage.Source != tc.wantSource {
					t.Fatalf("usage source: got %q, want %q", usage.Source, tc.wantSource)
				}
				if tc.wantCache > 0 && (usage.CacheTokens == nil || *usage.CacheTokens != tc.wantCache) {
					t.Fatalf("cache usage: got %#v", usage)
				}
				if tc.wantReasoning > 0 && (usage.ReasoningTokens == nil || *usage.ReasoningTokens != tc.wantReasoning) {
					t.Fatalf("reasoning usage: got %#v", usage)
				}
			}
			if len(agg.TokenCountRateLimits) != tc.wantRateLimits {
				t.Fatalf("token_count rate limits: got %d, want %d", len(agg.TokenCountRateLimits), tc.wantRateLimits)
			}
			if hasFinalWarning(warnings, harnesses.UsageWarningMalformed) != tc.wantMalformed {
				t.Fatalf("malformed warnings: got %#v", warnings)
			}
			if hasFinalWarning(warnings, harnesses.UsageWarningDisagreement) != tc.wantDisagreement {
				t.Fatalf("disagreement warnings: got %#v", warnings)
			}
			for _, warning := range warnings {
				if strings.Contains(warning.Message, "secret prompt") || strings.Contains(warning.Message, "last_token_usage") {
					t.Fatalf("warning leaked raw token_count content: %#v", warning)
				}
			}
		})
	}
}

func hasFinalWarning(warnings []harnesses.FinalWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func reviewerVerdictFromFinalText(text string) string {
	if strings.Contains(text, "APPROVE") {
		return "APPROVE"
	}
	if strings.Contains(text, "BLOCK") {
		return "BLOCK"
	}
	return "BLOCK"
}
