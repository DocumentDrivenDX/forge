package opencode

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
printf 'controlled response\n'
`, capture)
	binary := filepath.Join(dir, "fake-opencode")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{Binary: binary}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt:      "hello prompt",
		Model:       "opencode/gpt-5.4",
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
		"ARG[0]=run",
		"ARG[1]=--format",
		"ARG[2]=json",
		"ARG[3]=--dir",
		"ARG[4]=" + workDir,
		"ARG[5]=-m",
		"ARG[6]=opencode/gpt-5.4",
		"ARG[7]=--variant",
		"ARG[8]=high",
		"ARG[9]=hello prompt",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("capture missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "permission") || strings.Contains(got, "approval") {
		t.Fatalf("opencode permissions should not emit adapter flags:\n%s", got)
	}
}

func TestRunner_Execute_StdinPromptMode(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "stdin.txt")
	script := fmt.Sprintf(`#!/bin/sh
cat > %q
printf 'stdin response\n'
`, capture)
	binary := filepath.Join(dir, "fake-opencode")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{Binary: binary, BaseArgs: []string{}, PromptMode: "stdin"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{Prompt: "prompt over stdin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range ch {
	}

	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != "prompt over stdin" {
		t.Fatalf("stdin prompt = %q, want %q", got, "prompt over stdin")
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
	if finalEv.FinalText != "opencode response text" {
		t.Errorf("expected FinalText=%q, got %q", "opencode response text", finalEv.FinalText)
	}
}

func TestRunner_Execute_FinalUsageTotals(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	script := `#!/bin/sh
cat <<'EOF'
{"usage":{"input_tokens":15,"output_tokens":8},"total_cost_usd":0.003}
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

	r := &Runner{Binary: f.Name(), BaseArgs: []string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{Prompt: "count tokens"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var finalEv *harnesses.FinalData
	for ev := range ch {
		if ev.Type != harnesses.EventTypeFinal {
			continue
		}
		var fd harnesses.FinalData
		if err := json.Unmarshal(ev.Data, &fd); err != nil {
			t.Errorf("unmarshal final: %v", err)
		}
		finalEv = &fd
	}
	if finalEv == nil {
		t.Fatal("no final event received")
	}
	if finalEv.Usage == nil {
		t.Fatal("expected usage in final event")
	}
	if finalEv.Usage.InputTokens == nil || *finalEv.Usage.InputTokens != 15 {
		t.Errorf("expected InputTokens=15, got %#v", finalEv.Usage.InputTokens)
	}
	if finalEv.Usage.OutputTokens == nil || *finalEv.Usage.OutputTokens != 8 {
		t.Errorf("expected OutputTokens=8, got %#v", finalEv.Usage.OutputTokens)
	}
	if finalEv.Usage.TotalTokens == nil || *finalEv.Usage.TotalTokens != 23 {
		t.Errorf("expected TotalTokens=23, got %#v", finalEv.Usage.TotalTokens)
	}
}

func TestRunner_Execute_ExitErrorIncludesStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	script := `#!/bin/sh
printf 'bad model\n' >&2
exit 7
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

	r := &Runner{Binary: f.Name(), BaseArgs: []string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{Prompt: "fail"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	finalEv := readFinalEvent(t, ch)
	if finalEv.Status != "failed" {
		t.Fatalf("status = %q, want failed", finalEv.Status)
	}
	if finalEv.ExitCode != 7 {
		t.Fatalf("exit_code = %d, want 7", finalEv.ExitCode)
	}
	if !strings.Contains(finalEv.Error, "exit status 7") && !strings.Contains(finalEv.Error, "bad model") {
		t.Fatalf("error should include exit status or stderr, got %q", finalEv.Error)
	}
}

func TestRunner_Execute_RequestTimeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	script := `#!/bin/sh
sleep 5
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

	r := &Runner{Binary: f.Name(), BaseArgs: []string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt:  "timeout",
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	finalEv := readFinalEvent(t, ch)
	if finalEv.Status != "timed_out" {
		t.Fatalf("status = %q, want timed_out (error: %s)", finalEv.Status, finalEv.Error)
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

func TestParseOpencodeStream_ErrorEnvelope(t *testing.T) {
	input := `{"type":"error","error":{"name":"APIError","data":{"message":"Invalid model identifier \"*\"."}}}`
	out := make(chan harnesses.Event, 16)
	var seq int64
	_, err := parseOpencodeStream(context.Background(), strings.NewReader(input), out, nil, &seq)
	close(out)
	if err == nil {
		t.Fatal("expected opencode error envelope to fail parsing")
	}
	if !strings.Contains(err.Error(), `Invalid model identifier "*"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected no emitted events for error envelope, got %d", len(out))
	}
}

func readFinalEvent(t *testing.T, ch <-chan harnesses.Event) harnesses.FinalData {
	t.Helper()
	var finalEv *harnesses.FinalData
	for ev := range ch {
		if ev.Type != harnesses.EventTypeFinal {
			continue
		}
		var fd harnesses.FinalData
		if err := json.Unmarshal(ev.Data, &fd); err != nil {
			t.Errorf("unmarshal final: %v", err)
		}
		finalEv = &fd
	}
	if finalEv == nil {
		t.Fatal("no final event received")
	}
	return *finalEv
}
