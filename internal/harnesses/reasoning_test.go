package harnesses

import "testing"

func TestAdapterReasoningValueOmitsOff(t *testing.T) {
	for _, req := range []ExecuteRequest{
		{Reasoning: "off"},
		{Reasoning: "0"},
	} {
		if got := AdapterReasoningValue(req); got != "" {
			t.Fatalf("AdapterReasoningValue(%+v) = %q, want empty", req, got)
		}
	}
}

func TestAdapterReasoningValueNormalizesReasoning(t *testing.T) {
	got := AdapterReasoningValue(ExecuteRequest{Reasoning: "off"})
	if got != "" {
		t.Fatalf("Reasoning=off should suppress adapter flag, got %q", got)
	}
	got = AdapterReasoningValue(ExecuteRequest{Reasoning: "x-high"})
	if got != "xhigh" {
		t.Fatalf("Reasoning x-high should normalize and win, got %q", got)
	}
}
