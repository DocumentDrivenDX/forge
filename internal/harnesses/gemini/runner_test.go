package gemini

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
	if info.Name != "gemini" {
		t.Errorf("expected name=gemini, got %q", info.Name)
	}
	if info.Type != "subprocess" {
		t.Errorf("expected type=subprocess, got %q", info.Type)
	}
}

func TestRunner_HealthCheck_NoBinary(t *testing.T) {
	r := &Runner{Binary: "/nonexistent/gemini-binary-xyz"}
	err := r.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// TestRunner_Execute_HappyPath runs a fake script that emits gemini-style output.
func TestRunner_Execute_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// Gemini emits plain text (possibly with a JSON stats line at the end).
	script := `#!/bin/sh
cat <<'EOF'
Hello from gemini
{"stats":{"models":{"gemini-2.0-flash":{"tokens":{"input":12,"total":25}}}}}
EOF
`
	f, err := os.CreateTemp("", "fake-gemini-*")
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
	} else if !strings.Contains(textDeltas[0], "Hello from gemini") {
		t.Errorf("unexpected text delta: %q", textDeltas[0])
	}

	if finalEv == nil {
		t.Fatal("no final event received")
	}
	if finalEv.Status != "success" {
		t.Errorf("expected status=success, got %q (error: %s)", finalEv.Status, finalEv.Error)
	}
	// Usage from JSON stats line.
	if finalEv.Usage == nil {
		t.Error("expected usage in final event from JSON stats block")
	} else {
		if finalEv.Usage.InputTokens != 12 {
			t.Errorf("expected InputTokens=12, got %d", finalEv.Usage.InputTokens)
		}
		if finalEv.Usage.OutputTokens != 13 { // total(25) - input(12)
			t.Errorf("expected OutputTokens=13, got %d", finalEv.Usage.OutputTokens)
		}
	}
}

func TestParseGeminiUsage_Stats(t *testing.T) {
	output := `Hello world
{"stats":{"models":{"gemini-flash":{"tokens":{"input":5,"total":20}}}}}`
	agg := parseGeminiUsage(output)
	if agg.InputTokens != 5 {
		t.Errorf("expected InputTokens=5, got %d", agg.InputTokens)
	}
	if agg.OutputTokens != 15 { // 20-5
		t.Errorf("expected OutputTokens=15, got %d", agg.OutputTokens)
	}
	if agg.FinalText != output {
		t.Errorf("expected FinalText to be full output")
	}
}

func TestParseGeminiUsage_NoStats(t *testing.T) {
	output := "plain text response"
	agg := parseGeminiUsage(output)
	if agg.InputTokens != 0 || agg.OutputTokens != 0 {
		t.Errorf("expected zero tokens for plain text output")
	}
	if agg.FinalText != output {
		t.Errorf("expected FinalText=%q, got %q", output, agg.FinalText)
	}
}
