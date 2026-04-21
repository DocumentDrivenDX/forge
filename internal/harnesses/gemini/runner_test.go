package gemini

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	if !strings.Contains(finalEv.FinalText, "Hello from gemini") {
		t.Errorf("expected FinalText to contain gemini output, got %q", finalEv.FinalText)
	}
	// Usage from JSON stats line.
	if finalEv.Usage == nil {
		t.Error("expected usage in final event from JSON stats block")
	} else {
		if finalEv.Usage.InputTokens == nil || *finalEv.Usage.InputTokens != 12 {
			t.Errorf("expected InputTokens=12, got %#v", finalEv.Usage.InputTokens)
		}
		if finalEv.Usage.OutputTokens == nil || *finalEv.Usage.OutputTokens != 13 { // total(25) - input(12)
			t.Errorf("expected OutputTokens=13, got %#v", finalEv.Usage.OutputTokens)
		}
		if finalEv.Usage.TotalTokens == nil || *finalEv.Usage.TotalTokens != 25 {
			t.Errorf("expected TotalTokens=25, got %#v", finalEv.Usage.TotalTokens)
		}
	}
}

func TestRunner_Execute_RequestControls(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	workDir := t.TempDir()
	t.Setenv("GO_WANT_GEMINI_HELPER_PROCESS", "1")
	t.Setenv("GEMINI_HELPER_CAPTURE", capturePath)

	r := &Runner{
		Binary:   os.Args[0],
		BaseArgs: []string{"-test.run=TestGeminiHelperProcess", "--"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt:      "prompt over stdin",
		Model:       "gemini-test-model",
		WorkDir:     workDir,
		Permissions: "unrestricted",
		Reasoning:   "high",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range ch {
	}

	var captured geminiHelperCapture
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if err := json.Unmarshal(data, &captured); err != nil {
		t.Fatalf("unmarshal capture: %v", err)
	}
	if !reflect.DeepEqual(captured.Args, []string{"-m", "gemini-test-model"}) {
		t.Fatalf("args: got %v", captured.Args)
	}
	if captured.WorkDir != workDir {
		t.Fatalf("workdir: got %q, want %q", captured.WorkDir, workDir)
	}
	if captured.Stdin != "prompt over stdin" {
		t.Fatalf("stdin: got %q", captured.Stdin)
	}
}

func TestRunner_Info_UnsupportedControlsAreExplicit(t *testing.T) {
	info := (&Runner{Binary: os.Args[0]}).Info()
	if len(info.SupportedPermissions) != 0 {
		t.Fatalf("SupportedPermissions: got %v, want empty", info.SupportedPermissions)
	}
	if len(info.SupportedReasoning) != 0 {
		t.Fatalf("SupportedReasoning: got %v, want empty", info.SupportedReasoning)
	}
}

type geminiHelperCapture struct {
	Args    []string `json:"args"`
	WorkDir string   `json:"work_dir"`
	Stdin   string   `json:"stdin"`
}

func TestGeminiHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GEMINI_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	data, err := json.Marshal(geminiHelperCapture{
		Args:    args,
		WorkDir: wd,
		Stdin:   string(stdin),
	})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Getenv("GEMINI_HELPER_CAPTURE"), data, 0o600); err != nil {
		panic(err)
	}
	os.Stdout.WriteString("gemini helper response\n")
	os.Stdout.WriteString(`{"stats":{"models":{"gemini-test-model":{"tokens":{"input":1,"total":3}}}}}`)
	os.Exit(0)
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

func TestModelDiscoveryFromText(t *testing.T) {
	snapshot := ModelDiscoveryFromText("models:\r\n  \x1b[32mgemini-pro-test\x1b[0m\r\n  gemini-flash-test\r\n  gemini-pro-test", "unit-test")
	if !reflect.DeepEqual(snapshot.Models, []string{"gemini-pro-test", "gemini-flash-test"}) {
		t.Fatalf("models: got %v", snapshot.Models)
	}
	if len(snapshot.ReasoningLevels) != 0 {
		t.Fatalf("reasoning levels: got %v, want empty", snapshot.ReasoningLevels)
	}
	if snapshot.Source != "unit-test" {
		t.Fatalf("source: got %q", snapshot.Source)
	}
	if snapshot.FreshnessWindow != GeminiModelDiscoveryFreshnessWindow.String() {
		t.Fatalf("freshness window: got %q", snapshot.FreshnessWindow)
	}
}
