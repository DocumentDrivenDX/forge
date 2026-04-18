package pi

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
	if info.Name != "pi" {
		t.Errorf("expected name=pi, got %q", info.Name)
	}
	if info.Type != "subprocess" {
		t.Errorf("expected type=subprocess, got %q", info.Type)
	}
}

func TestRunner_HealthCheck_NoBinary(t *testing.T) {
	r := &Runner{Binary: "/nonexistent/pi-binary-xyz"}
	err := r.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// TestRunner_Execute_HappyPath runs a fake script that emits pi-style JSONL.
func TestRunner_Execute_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// Pi emits JSONL where the last line contains a "response" field and usage.
	script := `#!/bin/sh
cat <<'EOF'
{"type":"text_delta","partial":{"usage":{"input":8,"output":3,"cost":{"total":0.001}}}}
{"type":"text_end","message":{"usage":{"input":8,"output":3,"cost":{"total":0.001}}},"response":"hello from pi"}
EOF
`
	f, err := os.CreateTemp("", "fake-pi-*")
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
		BaseArgs: []string{},
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
	} else if !strings.Contains(textDeltas[0], "hello from pi") {
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
		if finalEv.Usage.InputTokens != 8 {
			t.Errorf("expected InputTokens=8, got %d", finalEv.Usage.InputTokens)
		}
		if finalEv.Usage.OutputTokens != 3 {
			t.Errorf("expected OutputTokens=3, got %d", finalEv.Usage.OutputTokens)
		}
	}
}

func TestParsePiStream_EventTypes(t *testing.T) {
	input := `{"type":"text_delta","partial":{"usage":{"input":10,"output":4,"cost":{"total":0.002}}}}
{"type":"text_end","message":{"usage":{"input":10,"output":4,"cost":{"total":0.002}}},"response":"pi response text"}
`
	out := make(chan harnesses.Event, 16)
	var seq int64
	agg, err := parsePiStream(context.Background(), strings.NewReader(input), out, nil, &seq)
	close(out)
	if err != nil {
		t.Fatalf("parsePiStream: %v", err)
	}

	if agg.FinalText != "pi response text" {
		t.Errorf("expected FinalText=%q, got %q", "pi response text", agg.FinalText)
	}
	if agg.InputTokens != 10 {
		t.Errorf("expected InputTokens=10, got %d", agg.InputTokens)
	}
	if agg.OutputTokens != 4 {
		t.Errorf("expected OutputTokens=4, got %d", agg.OutputTokens)
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
