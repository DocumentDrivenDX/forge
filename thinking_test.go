package agent

import "testing"

func TestReasoningTokens(t *testing.T) {
	if got := ReasoningTokens(1234); got != Reasoning("1234") {
		t.Fatalf("ReasoningTokens(1234) = %q, want 1234", got)
	}
}
