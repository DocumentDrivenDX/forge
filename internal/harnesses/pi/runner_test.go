package pi

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
	if info.Name != "pi" {
		t.Errorf("expected name=pi, got %q", info.Name)
	}
	if info.Type != "subprocess" {
		t.Errorf("expected type=subprocess, got %q", info.Type)
	}
	if info.SupportedPermissions != nil {
		t.Errorf("expected no pi permission modes, got %#v", info.SupportedPermissions)
	}
	if !containsString(info.SupportedReasoning, "minimal") || !containsString(info.SupportedReasoning, "xhigh") {
		t.Errorf("expected pi thinking levels in metadata, got %#v", info.SupportedReasoning)
	}
	if containsString(info.SupportedReasoning, "off") {
		t.Errorf("off disables adapter reasoning and should not be advertised as an enabled level: %#v", info.SupportedReasoning)
	}
}

func TestRunner_HealthCheck_NoBinary(t *testing.T) {
	r := &Runner{Binary: "/nonexistent/pi-binary-xyz"}
	err := r.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestRunner_Execute_AppliesRequestControlsArgPrompt(t *testing.T) {
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
  if read stdin_value; then
    printf 'STDIN=%%s\n' "$stdin_value"
  fi
} > %q
cat <<'EOF'
{"type":"text_end","message":{"usage":{"input":3,"output":2}},"response":"ok"}
EOF
`, capture)
	binary := filepath.Join(dir, "fake-pi")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{Binary: binary}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt:      "hello prompt",
		Model:       "gemini-2.5-flash",
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
		"ARG[0]=--mode",
		"ARG[1]=json",
		"ARG[2]=--print",
		"ARG[3]=--model",
		"ARG[4]=gemini-2.5-flash",
		"ARG[5]=--thinking",
		"ARG[6]=high",
		"ARG[7]=hello prompt",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("capture missing %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{"--permission-mode", "dangerously"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("pi should not emit permission flags; found %q in:\n%s", notWant, got)
		}
	}
}

func TestRunner_Execute_AppliesRequestControlsStdinPrompt(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	capture := filepath.Join(dir, "capture.txt")
	script := fmt.Sprintf(`#!/bin/sh
{
  i=0
  for arg in "$@"; do
    printf 'ARG[%%s]=%%s\n' "$i" "$arg"
    i=$((i + 1))
  done
  printf 'STDIN='
  cat
  printf '\n'
} > %q
cat <<'EOF'
{"type":"text_end","message":{"usage":{"input":1,"output":1}},"response":"ok"}
EOF
`, capture)
	binary := filepath.Join(dir, "fake-pi")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{Binary: binary, PromptMode: "stdin"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := r.Execute(ctx, harnesses.ExecuteRequest{
		Prompt:    "prompt over stdin",
		Model:     "gemini-2.5-pro",
		Reasoning: "off",
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
	if !strings.Contains(got, "STDIN=prompt over stdin") {
		t.Fatalf("stdin prompt missing:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "ARG[") && (strings.Contains(line, "--thinking") || strings.Contains(line, "prompt over stdin")) {
			t.Fatalf("off reasoning or stdin prompt leaked into argv:\n%s", got)
		}
	}
}

func TestRunner_Execute_ExitErrorIncludesStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	script := `#!/bin/sh
printf 'pi failed\n' >&2
exit 9
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
	if finalEv.ExitCode != 9 {
		t.Fatalf("exit_code = %d, want 9", finalEv.ExitCode)
	}
	if !strings.Contains(finalEv.Error, "exit status 9") && !strings.Contains(finalEv.Error, "pi failed") {
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
	if finalEv.FinalText != "hello from pi" {
		t.Errorf("expected FinalText=%q, got %q", "hello from pi", finalEv.FinalText)
	}
	if finalEv.Usage == nil {
		t.Error("expected usage in final event")
	} else {
		if finalEv.Usage.InputTokens == nil || *finalEv.Usage.InputTokens != 8 {
			t.Errorf("expected InputTokens=8, got %#v", finalEv.Usage.InputTokens)
		}
		if finalEv.Usage.OutputTokens == nil || *finalEv.Usage.OutputTokens != 3 {
			t.Errorf("expected OutputTokens=3, got %#v", finalEv.Usage.OutputTokens)
		}
		if finalEv.Usage.TotalTokens == nil || *finalEv.Usage.TotalTokens != 11 {
			t.Errorf("expected TotalTokens=11, got %#v", finalEv.Usage.TotalTokens)
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

func TestParsePiStream_CurrentTextEndShape(t *testing.T) {
	input := `{"type":"message_update","assistantMessageEvent":{"type":"text_end","content":"\n\nharness golden record ok"},"message":{"usage":{"input":1589,"output":70,"cost":{"total":0}}}}`
	out := make(chan harnesses.Event, 16)
	var seq int64
	agg, err := parsePiStream(context.Background(), strings.NewReader(input), out, nil, &seq)
	close(out)
	if err != nil {
		t.Fatalf("parsePiStream: %v", err)
	}
	if agg.FinalText != "harness golden record ok" {
		t.Fatalf("FinalText: got %q", agg.FinalText)
	}
	if agg.InputTokens != 1589 || agg.OutputTokens != 70 {
		t.Fatalf("usage: got input=%d output=%d", agg.InputTokens, agg.OutputTokens)
	}
	var delta harnesses.TextDeltaData
	ev := <-out
	if err := json.Unmarshal(ev.Data, &delta); err != nil {
		t.Fatalf("text delta: %v", err)
	}
	if delta.Text != "harness golden record ok" {
		t.Fatalf("text delta: got %q", delta.Text)
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
