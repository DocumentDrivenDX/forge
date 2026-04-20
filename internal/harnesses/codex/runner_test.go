package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
		if finalEv.Usage.InputTokens != 10 {
			t.Errorf("expected InputTokens=10, got %d", finalEv.Usage.InputTokens)
		}
		if finalEv.Usage.OutputTokens != 5 {
			t.Errorf("expected OutputTokens=5, got %d", finalEv.Usage.OutputTokens)
		}
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
	if agg.InputTokens != 20 {
		t.Errorf("expected InputTokens=20, got %d", agg.InputTokens)
	}
	if agg.OutputTokens != 7 {
		t.Errorf("expected OutputTokens=7, got %d", agg.OutputTokens)
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
	if agg.InputTokens != 12 || agg.OutputTokens != 5 {
		t.Fatalf("usage: got input=%d output=%d", agg.InputTokens, agg.OutputTokens)
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

func reviewerVerdictFromFinalText(text string) string {
	if strings.Contains(text, "APPROVE") {
		return "APPROVE"
	}
	if strings.Contains(text, "BLOCK") {
		return "BLOCK"
	}
	return "BLOCK"
}
