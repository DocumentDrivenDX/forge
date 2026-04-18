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
