package agent

import (
	"testing"
)

func TestResolveThinkingBudget(t *testing.T) {
	tests := []struct {
		level ThinkingLevel
		want  int
	}{
		{ThinkingLevelOff, 0},
		{ThinkingLevelLow, 2048},
		{ThinkingLevelMedium, 8192},
		{ThinkingLevelHigh, 32768},
		{"unknown", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := ResolveThinkingBudget(tt.level)
		if got != tt.want {
			t.Errorf("ResolveThinkingBudget(%q) = %d, want %d", tt.level, got, tt.want)
		}
	}
}
