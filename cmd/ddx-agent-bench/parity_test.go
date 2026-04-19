package main

import (
	"math"
	"testing"

	"github.com/DocumentDrivenDX/agent/internal/comparison"
)

// ---------------------------------------------------------------------------
// normalizeInput
// ---------------------------------------------------------------------------

func TestNormalizeInput_JSONRoundtrip(t *testing.T) {
	// Key order and whitespace should be stripped.
	a := `{"b": 2, "a": 1}`
	b := `{"a":1,"b":2}`
	if normalizeInput(a) != normalizeInput(b) {
		t.Errorf("expected normalised forms to be equal: %q vs %q",
			normalizeInput(a), normalizeInput(b))
	}
}

func TestNormalizeInput_PlainString(t *testing.T) {
	s := "  hello world  "
	if got := normalizeInput(s); got != "hello world" {
		t.Errorf("expected trimmed string, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// ToolCallSeqEqual
// ---------------------------------------------------------------------------

func TestToolCallSeqEqual_Equal(t *testing.T) {
	a := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{
			{Tool: "read_file", Input: `{"path":"a.txt"}`},
			{Tool: "write_file", Input: `{"path":"b.txt","content":"hi"}`},
		},
	}
	b := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{
			{Tool: "read_file", Input: `{"path":"a.txt"}`},
			{Tool: "write_file", Input: `{"path":"b.txt","content":"hi"}`},
		},
	}
	if !ToolCallSeqEqual(a, b) {
		t.Error("expected equal sequences to be equal")
	}
}

func TestToolCallSeqEqual_DiffTool(t *testing.T) {
	a := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{{Tool: "read_file", Input: `{}`}},
	}
	b := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{{Tool: "list_files", Input: `{}`}},
	}
	if ToolCallSeqEqual(a, b) {
		t.Error("expected different tool names to be not equal")
	}
}

func TestToolCallSeqEqual_DiffLength(t *testing.T) {
	a := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{
			{Tool: "read_file", Input: `{}`},
			{Tool: "write_file", Input: `{}`},
		},
	}
	b := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{
			{Tool: "read_file", Input: `{}`},
		},
	}
	if ToolCallSeqEqual(a, b) {
		t.Error("expected different-length sequences to be not equal")
	}
}

func TestToolCallSeqEqual_Empty(t *testing.T) {
	a := comparison.RunResult{}
	b := comparison.RunResult{}
	if !ToolCallSeqEqual(a, b) {
		t.Error("expected two empty sequences to be equal")
	}
}

func TestToolCallSeqEqual_NormalisedJSONEqual(t *testing.T) {
	// Same logical JSON, different whitespace/key order — should be equal.
	a := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{
			{Tool: "bash", Input: `{"command": "ls", "cwd": "/tmp"}`},
		},
	}
	b := comparison.RunResult{
		ToolCalls: []comparison.ToolCallEntry{
			{Tool: "bash", Input: `{"cwd":"/tmp","command":"ls"}`},
		},
	}
	if !ToolCallSeqEqual(a, b) {
		t.Error("expected normalised-JSON sequences to be equal")
	}
}

// ---------------------------------------------------------------------------
// LevenshteinRatio
// ---------------------------------------------------------------------------

func TestLevenshteinRatio_Identical(t *testing.T) {
	if r := LevenshteinRatio("hello", "hello"); r != 1.0 {
		t.Errorf("expected 1.0 for identical strings, got %f", r)
	}
}

func TestLevenshteinRatio_Empty(t *testing.T) {
	if r := LevenshteinRatio("", ""); r != 1.0 {
		t.Errorf("expected 1.0 for two empty strings, got %f", r)
	}
}

func TestLevenshteinRatio_OneEmpty(t *testing.T) {
	r := LevenshteinRatio("abc", "")
	if r >= 1.0 || r < 0.0 {
		t.Errorf("unexpected ratio for (abc, ''): %f", r)
	}
}

func TestLevenshteinRatio_HighSimilarity(t *testing.T) {
	// One character difference in a 10-char string → high similarity.
	r := LevenshteinRatio("hello world", "hello warld")
	if r < 0.8 {
		t.Errorf("expected high similarity, got %f", r)
	}
}

func TestLevenshteinRatio_LowSimilarity(t *testing.T) {
	r := LevenshteinRatio("abcdef", "xyz")
	if r > 0.5 {
		t.Errorf("expected low similarity, got %f", r)
	}
}

func TestLevenshteinRatio_Bounds(t *testing.T) {
	pairs := [][2]string{
		{"", "hello"},
		{"hello", "world"},
		{"identical", "identical"},
		{"short", "a very long different string indeed"},
	}
	for _, p := range pairs {
		r := LevenshteinRatio(p[0], p[1])
		if r < 0.0 || r > 1.0 {
			t.Errorf("ratio out of [0,1] for (%q, %q): %f", p[0], p[1], r)
		}
		if math.IsNaN(r) || math.IsInf(r, 0) {
			t.Errorf("ratio is NaN/Inf for (%q, %q)", p[0], p[1])
		}
	}
}

// ---------------------------------------------------------------------------
// BuildParityMatrix
// ---------------------------------------------------------------------------

func TestBuildParityMatrix_TwoArms(t *testing.T) {
	record := comparison.ComparisonRecord{
		ID: "task-1",
		Arms: []comparison.ComparisonArm{
			{
				Harness: "claude",
				Output:  "done",
				ToolCalls: []comparison.ToolCallEntry{
					{Tool: "read_file", Input: `{"path":"x"}`},
				},
			},
			{
				Harness: "codex",
				Output:  "done",
				ToolCalls: []comparison.ToolCallEntry{
					{Tool: "read_file", Input: `{"path":"x"}`},
				},
			},
		},
	}
	cells := BuildParityMatrix(record, []string{"claude", "codex"})
	if len(cells) != 1 {
		t.Fatalf("expected 1 cell for 2 arms, got %d", len(cells))
	}
	if !cells[0].ToolSeqEqual {
		t.Error("expected tool sequences to be equal")
	}
	if cells[0].OutputSimilarity < 0.9 {
		t.Errorf("expected high output similarity, got %f", cells[0].OutputSimilarity)
	}
}

func TestBuildParityMatrix_ThreeArms(t *testing.T) {
	record := comparison.ComparisonRecord{
		ID: "task-2",
		Arms: []comparison.ComparisonArm{
			{Harness: "a", Output: "x"},
			{Harness: "b", Output: "y"},
			{Harness: "c", Output: "z"},
		},
	}
	cells := BuildParityMatrix(record, nil)
	// 3 arms → C(3,2) = 3 pairs
	if len(cells) != 3 {
		t.Fatalf("expected 3 cells for 3 arms, got %d", len(cells))
	}
}

// ---------------------------------------------------------------------------
// Cost cap: buildRunFunc honours the cap
// ---------------------------------------------------------------------------

func TestCostCapSkip(t *testing.T) {
	// A runFunc that always returns a $1.00 cost result.
	calls := 0
	expensive := func(harness, model, prompt string) comparison.RunResult {
		calls++
		return comparison.RunResult{
			Harness:  harness,
			Model:    model,
			ExitCode: 0,
			CostUSD:  1.00,
		}
	}

	// Simulate cap enforcement the same way the real RunFunc does it.
	// We replicate the cap logic here as a white-box test since buildRunFunc
	// requires a live agent config.  The cap is at $0.50.
	cap := 0.50
	var accumulated float64
	wrapped := func(harness, model, prompt string) comparison.RunResult {
		if cap > 0 && accumulated >= cap {
			return comparison.RunResult{
				Harness:  harness,
				Model:    model,
				Error:    CostCapSkipReason,
				ExitCode: -1,
			}
		}
		r := expensive(harness, model, prompt)
		if r.CostUSD > 0 {
			accumulated += r.CostUSD
		}
		return r
	}

	// First call should run (accumulated=0 < cap=0.50).
	r1 := wrapped("claude", "", "task1")
	if r1.Error == CostCapSkipReason {
		t.Fatal("first call should not be skipped")
	}
	if calls != 1 {
		t.Fatalf("expected 1 underlying call, got %d", calls)
	}

	// After first call accumulated=$1.00 > cap=$0.50; second should be skipped.
	r2 := wrapped("claude", "", "task2")
	if r2.Error != CostCapSkipReason {
		t.Errorf("expected second call to be skipped, got error=%q", r2.Error)
	}
	// Underlying expensive fn should NOT have been called again.
	if calls != 1 {
		t.Errorf("expected no additional underlying calls after cap, got %d total", calls)
	}
}
