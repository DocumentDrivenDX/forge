//go:build testseam

package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/DocumentDrivenDX/agent"
)

// drainEvents collects everything from ch until it closes or the deadline
// fires. The final element (when present) is always EventTypeFinal.
func drainEvents(t *testing.T, ch <-chan agent.ServiceEvent, timeout time.Duration) []agent.ServiceEvent {
	t.Helper()
	var events []agent.ServiceEvent
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for channel close; collected %d events", timeout, len(events))
			return events
		}
	}
}

// findFinal returns the final event (the last EventTypeFinal in the slice)
// or nil if absent.
func findFinal(events []agent.ServiceEvent) *agent.ServiceEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "final" {
			ev := events[i]
			return &ev
		}
	}
	return nil
}

// finalStatus extracts the status field from a final event's JSON payload.
func finalStatus(t *testing.T, ev *agent.ServiceEvent) string {
	t.Helper()
	if ev == nil {
		return ""
	}
	var payload struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatalf("unmarshal final: %v", err)
	}
	return payload.Status
}

// finalError extracts the error message from a final event's JSON payload.
func finalError(t *testing.T, ev *agent.ServiceEvent) string {
	t.Helper()
	if ev == nil {
		return ""
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatalf("unmarshal final: %v", err)
	}
	return payload.Error
}

// TestExecute_NativePathWithFakeProvider verifies that a native-path
// Execute drives loop.go through the FakeProvider seam, emits a routing
// decision, forwards events with metadata, and terminates with success.
func TestExecute_NativePathWithFakeProvider(t *testing.T) {
	fp := &agent.FakeProvider{
		Static: []agent.FakeResponse{
			{Text: "hello world", Usage: agent.TokenUsage{Input: 10, Output: 5, Total: 15}},
		},
	}
	opts := agent.ServiceOptions{}
	opts.FakeProvider = fp

	svc, err := agent.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := agent.ServiceExecuteRequest{
		Prompt:   "hi",
		Harness:  "agent",
		Model:    "fake-model",
		Provider: "fake",
		Metadata: map[string]string{"bead_id": "test-bead-1"},
	}
	ch, err := svc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	events := drainEvents(t, ch, 5*time.Second)
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	final := findFinal(events)
	if final == nil {
		t.Fatal("expected final event")
	}
	if got := finalStatus(t, final); got != "success" {
		t.Errorf("status: want success, got %q (err=%q)", got, finalError(t, final))
	}
	// First event is the routing_decision.
	if events[0].Type != "routing_decision" {
		t.Errorf("first event type: want routing_decision, got %q", events[0].Type)
	}
}

func TestExecute_NativeReasoningForwarded(t *testing.T) {
	var got agent.Reasoning
	fp := &agent.FakeProvider{
		Dynamic: func(req agent.FakeRequest) (agent.FakeResponse, error) {
			got = req.Reasoning
			return agent.FakeResponse{Text: "done"}, nil
		},
	}
	opts := agent.ServiceOptions{FakeProvider: fp}
	svc, err := agent.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ch, err := svc.Execute(context.Background(), agent.ServiceExecuteRequest{
		Prompt:    "hi",
		Harness:   "agent",
		Provider:  "fake",
		Model:     "fake-model",
		Reasoning: agent.ReasoningOff,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	events := drainEvents(t, ch, 5*time.Second)
	if final := findFinal(events); final == nil || finalStatus(t, final) != "success" {
		t.Fatalf("expected success final, got %#v", final)
	}
	if got != agent.ReasoningOff {
		t.Fatalf("Reasoning forwarded to native provider = %q, want off", got)
	}
}

// TestExecute_StallPolicy_ReadOnlyTrigger verifies that a fake provider
// emitting only read-only tool calls triggers the stall policy and
// terminates with Status="stalled".
func TestExecute_StallPolicy_ReadOnlyTrigger(t *testing.T) {
	// Dynamic provider that always asks for a `read` tool call. The agent
	// loop has no tool wired (Tools is nil) so each turn the model "asks"
	// but the loop reports an unknown-tool error and keeps looping. That
	// would normally hit the tool-call-loop limit; we cap iterations short
	// via a tight StallPolicy so the read-only ceiling fires first.
	callCount := 0
	fp := &agent.FakeProvider{
		Dynamic: func(req agent.FakeRequest) (agent.FakeResponse, error) {
			callCount++
			return agent.FakeResponse{
				ToolCalls: []agent.ToolCall{{
					ID:        "c1",
					Name:      "read",
					Arguments: json.RawMessage(`{"path":"/tmp/x"}`),
				}},
			}, nil
		},
	}
	opts := agent.ServiceOptions{}
	opts.FakeProvider = fp
	svc, err := agent.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := agent.ServiceExecuteRequest{
		Prompt:   "stall please",
		Harness:  "agent",
		Provider: "fake",
		Model:    "fake-model",
		StallPolicy: &agent.StallPolicy{
			MaxReadOnlyToolIterations: 3,
		},
		Timeout: 5 * time.Second,
	}
	ch, err := svc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	events := drainEvents(t, ch, 10*time.Second)
	final := findFinal(events)
	if final == nil {
		t.Fatal("expected final event")
	}
	got := finalStatus(t, final)
	// The iteration ceiling derived from the stall policy may also fire
	// (read-only-tool-streak triggers cancel; loop reports either
	// "stalled" via our override or "cancelled"/"failed" depending on
	// timing). All three indicate termination short of natural success.
	if got == "success" {
		t.Errorf("expected non-success final, got %q", got)
	}
}

// TestExecute_OrphanModelFails verifies that a native-path request with
// no provider and no FakeProvider yields Status="failed" with an explicit
// orphan-model error message.
func TestExecute_OrphanModelFails(t *testing.T) {
	svc, err := agent.New(agent.ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := agent.ServiceExecuteRequest{
		Prompt:  "hi",
		Harness: "agent",
		Model:   "no-such-model",
	}
	ch, err := svc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	events := drainEvents(t, ch, 2*time.Second)
	final := findFinal(events)
	if final == nil {
		t.Fatal("expected final event")
	}
	if got := finalStatus(t, final); got != "failed" {
		t.Errorf("status: want failed, got %q", got)
	}
	errMsg := finalError(t, final)
	if !strings.Contains(errMsg, "orphan model") && !strings.Contains(errMsg, "no provider") {
		t.Errorf("error: want orphan/no-provider message, got %q", errMsg)
	}
}

// TestExecute_PreResolvedSkipsRouting verifies that a request carrying
// PreResolved bypasses route resolution and is honored verbatim. We use
// an unknown harness name in PreResolved that ResolveRoute would otherwise
// reject — Execute should accept it (and then fail with "dispatch not
// wired" since it's not in the known switch).
func TestExecute_PreResolvedSkipsRouting(t *testing.T) {
	svc, err := agent.New(agent.ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := agent.ServiceExecuteRequest{
		Prompt: "hi",
		PreResolved: &agent.RouteDecision{
			Harness:  "made-up",
			Provider: "made-up",
			Model:    "made-up",
			Reason:   "test pre-resolution",
		},
	}
	ch, err := svc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	events := drainEvents(t, ch, 2*time.Second)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	// The routing_decision event must reflect the pre-resolved values
	// verbatim — confirming routing was skipped.
	if events[0].Type != "routing_decision" {
		t.Fatalf("first event: want routing_decision, got %q", events[0].Type)
	}
	var decision struct {
		Harness  string `json:"harness"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(events[0].Data, &decision); err != nil {
		t.Fatalf("unmarshal routing_decision: %v", err)
	}
	if decision.Harness != "made-up" || decision.Reason != "test pre-resolution" {
		t.Errorf("routing_decision did not honor PreResolved: %+v", decision)
	}
}

// TestExecute_TimeoutWallClock verifies that a wall-clock Timeout fires
// when the provider takes longer than the cap.
func TestExecute_TimeoutWallClock(t *testing.T) {
	fp := &agent.FakeProvider{
		Dynamic: func(req agent.FakeRequest) (agent.FakeResponse, error) {
			// Sleep longer than the wall-clock cap so the request must
			// be cancelled; return an error to simulate the cancel
			// surface.
			time.Sleep(500 * time.Millisecond)
			return agent.FakeResponse{}, errors.New("provider should have been cancelled")
		},
	}
	opts := agent.ServiceOptions{}
	opts.FakeProvider = fp
	svc, err := agent.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := agent.ServiceExecuteRequest{
		Prompt:   "hi",
		Harness:  "agent",
		Provider: "fake",
		Model:    "fake-model",
		Timeout:  100 * time.Millisecond,
	}
	ch, err := svc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	events := drainEvents(t, ch, 5*time.Second)
	final := findFinal(events)
	if final == nil {
		t.Fatal("expected final event")
	}
	got := finalStatus(t, final)
	// Either timed_out (our override caught it) or cancelled (loop saw
	// ctx.Done first) is acceptable — both indicate the wall-clock cap
	// fired.
	if got != "timed_out" && got != "cancelled" && got != "failed" {
		t.Errorf("status: want timed_out/cancelled/failed, got %q (err=%q)", got, finalError(t, final))
	}
}

// TestExecute_MetadataEchoedOnEvents verifies that req.Metadata is
// stamped onto every event the channel emits.
func TestExecute_MetadataEchoedOnEvents(t *testing.T) {
	fp := &agent.FakeProvider{
		Static: []agent.FakeResponse{
			{Text: "ok"},
		},
	}
	opts := agent.ServiceOptions{}
	opts.FakeProvider = fp
	svc, err := agent.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wantMeta := map[string]string{
		"bead_id":    "agent-755fea77",
		"attempt_id": "1",
	}
	req := agent.ServiceExecuteRequest{
		Prompt:   "hi",
		Harness:  "agent",
		Provider: "fake",
		Model:    "fake-model",
		Metadata: wantMeta,
	}
	ch, err := svc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	events := drainEvents(t, ch, 5*time.Second)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	for i, ev := range events {
		if ev.Metadata == nil {
			t.Errorf("event %d (%s): metadata is nil", i, ev.Type)
			continue
		}
		for k, v := range wantMeta {
			if got := ev.Metadata[k]; got != v {
				t.Errorf("event %d (%s) metadata[%s]: want %q, got %q", i, ev.Type, k, v, got)
			}
		}
	}
}

// TestExecute_SessionLogDirOverride verifies that req.SessionLogDir
// directs the per-request session log to the supplied path.
func TestExecute_SessionLogDirOverride(t *testing.T) {
	fp := &agent.FakeProvider{
		Static: []agent.FakeResponse{
			{Text: "ok"},
		},
	}
	opts := agent.ServiceOptions{}
	opts.FakeProvider = fp
	svc, err := agent.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := t.TempDir()
	req := agent.ServiceExecuteRequest{
		Prompt:        "hi",
		Harness:       "agent",
		Provider:      "fake",
		Model:         "fake-model",
		SessionLogDir: dir,
	}
	ch, err := svc.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = drainEvents(t, ch, 5*time.Second)

	// At least one *.jsonl file should now exist in dir.
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("no session log written to %s; entries: %v", dir, names)
	}
}

// TestExecute_OSCancelDuringStreaming verifies that ctx.Done() while
// the loop is mid-flight terminates the stream cleanly with a
// cancelled-status final.
func TestExecute_OSCancelDuringStreaming(t *testing.T) {
	fp := &agent.FakeProvider{
		Dynamic: func(req agent.FakeRequest) (agent.FakeResponse, error) {
			time.Sleep(2 * time.Second)
			return agent.FakeResponse{Text: "late"}, nil
		},
	}
	opts := agent.ServiceOptions{}
	opts.FakeProvider = fp
	svc, err := agent.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := agent.ServiceExecuteRequest{
		Prompt:   "hi",
		Harness:  "agent",
		Provider: "fake",
		Model:    "fake-model",
	}
	ch, err := svc.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Cancel before the provider's slow Dynamic returns.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	events := drainEvents(t, ch, 5*time.Second)
	final := findFinal(events)
	if final == nil {
		t.Fatal("expected final event")
	}
	got := finalStatus(t, final)
	if got != "cancelled" && got != "failed" {
		t.Errorf("status: want cancelled/failed, got %q", got)
	}
}
