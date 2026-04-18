package opencode

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
	if info.Name != "opencode" {
		t.Errorf("expected name=opencode, got %q", info.Name)
	}
	if info.Type != "subprocess" {
		t.Errorf("expected type=subprocess, got %q", info.Type)
	}
}

func TestRunner_HealthCheck_NoBinary(t *testing.T) {
	r := &Runner{Binary: "/nonexistent/opencode-binary-xyz"}
	err := r.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// TestRunner_Execute_HappyPath runs a fake script that emits opencode-style JSON output.
func TestRunner_Execute_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// Fake opencode emits a JSON object with response text and usage.
	script := `#!/bin/sh
cat <<'EOF'
opencode response text
EOF
`
	f, err := os.CreateTemp("", "fake-opencode-*")
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
	} else if !strings.Contains(textDeltas[0], "opencode response text") {
		t.Errorf("unexpected text delta: %q", textDeltas[0])
	}

	if finalEv == nil {
		t.Fatal("no final event received")
	}
	if finalEv.Status != "success" {
		t.Errorf("expected status=success, got %q (error: %s)", finalEv.Status, finalEv.Error)
	}
}

func TestParseOpencodeStream_WithUsage(t *testing.T) {
	// Simulate opencode JSON output with usage envelope.
	input := `{"usage":{"input_tokens":15,"output_tokens":8},"total_cost_usd":0.003}`
	out := make(chan harnesses.Event, 16)
	var seq int64
	agg, err := parseOpencodeStream(context.Background(), strings.NewReader(input), out, nil, &seq)
	close(out)
	if err != nil {
		t.Fatalf("parseOpencodeStream: %v", err)
	}

	if agg.InputTokens != 15 {
		t.Errorf("expected InputTokens=15, got %d", agg.InputTokens)
	}
	if agg.OutputTokens != 8 {
		t.Errorf("expected OutputTokens=8, got %d", agg.OutputTokens)
	}
	if agg.CostUSD != 0.003 {
		t.Errorf("expected CostUSD=0.003, got %f", agg.CostUSD)
	}

	var events []harnesses.Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 text_delta event, got %d", len(events))
	}
	if events[0].Type != harnesses.EventTypeTextDelta {
		t.Errorf("expected text_delta, got %q", events[0].Type)
	}
}
