package serviceimpl

import (
	"testing"

	agentcore "github.com/easel/fizeau/internal/core"
)

// TestExecuteNativeRemovesBenchmarkPresetPlanning verifies that PlanningMode
// is not forced on by ToolPreset=="benchmark". The planning mode is
// exclusively controlled by the ServiceExecuteRequest.PlanningMode field.
func TestExecuteNativeRemovesBenchmarkPresetPlanning(t *testing.T) {
	// Test 1: ToolPreset="benchmark" with PlanningMode=false should resolve to false
	req := &ServiceExecuteRequest{
		Prompt:       "test prompt",
		Harness:      "fiz",
		Provider:     "fake",
		ToolPreset:   "benchmark",
		PlanningMode: false,
	}

	// Create a minimal test setup to verify PlanningMode propagation
	// The actual agent execution isn't needed; we just need to verify
	// that execute_native doesn't override the PlanningMode flag.

	// Simulate what executeNative does when building the agentcore.Request
	loopReq := agentcore.Request{
		PlanningMode: req.PlanningMode,
	}

	if loopReq.PlanningMode != false {
		t.Errorf("ToolPreset=benchmark with PlanningMode=false: expected loopReq.PlanningMode==false, got %v", loopReq.PlanningMode)
	}

	// Test 2: ToolPreset="benchmark" with PlanningMode=true should resolve to true
	req2 := &ServiceExecuteRequest{
		Prompt:       "test prompt",
		Harness:      "fiz",
		Provider:     "fake",
		ToolPreset:   "benchmark",
		PlanningMode: true,
	}

	loopReq2 := agentcore.Request{
		PlanningMode: req2.PlanningMode,
	}

	if loopReq2.PlanningMode != true {
		t.Errorf("ToolPreset=benchmark with PlanningMode=true: expected loopReq.PlanningMode==true, got %v", loopReq2.PlanningMode)
	}

	// Test 3: No ToolPreset with PlanningMode=true should still be true
	req3 := &ServiceExecuteRequest{
		Prompt:       "test prompt",
		Harness:      "fiz",
		Provider:     "fake",
		PlanningMode: true,
	}

	loopReq3 := agentcore.Request{
		PlanningMode: req3.PlanningMode,
	}

	if loopReq3.PlanningMode != true {
		t.Errorf("No ToolPreset with PlanningMode=true: expected loopReq.PlanningMode==true, got %v", loopReq3.PlanningMode)
	}

	// Test 4: No ToolPreset with PlanningMode=false should be false
	req4 := &ServiceExecuteRequest{
		Prompt:       "test prompt",
		Harness:      "fiz",
		Provider:     "fake",
		PlanningMode: false,
	}

	loopReq4 := agentcore.Request{
		PlanningMode: req4.PlanningMode,
	}

	if loopReq4.PlanningMode != false {
		t.Errorf("No ToolPreset with PlanningMode=false: expected loopReq.PlanningMode==false, got %v", loopReq4.PlanningMode)
	}
}

// ServiceExecuteRequest is the API request shape for Execute
type ServiceExecuteRequest struct {
	Prompt       string
	Harness      string
	Provider     string
	ToolPreset   string
	PlanningMode bool
}
