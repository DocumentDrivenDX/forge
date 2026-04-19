package reasoning

import "testing"

func TestParseReasoningNormalizesScalarValues(t *testing.T) {
	tests := []struct {
		name  string
		input any
		kind  Kind
		value Reasoning
		tok   int
	}{
		{name: "empty", input: "", kind: KindUnset},
		{name: "auto", input: "auto", kind: KindAuto, value: ReasoningAuto},
		{name: "off", input: "off", kind: KindOff, value: ReasoningOff},
		{name: "none", input: "none", kind: KindOff, value: ReasoningOff},
		{name: "false", input: "false", kind: KindOff, value: ReasoningOff},
		{name: "zero string", input: "0", kind: KindOff, value: ReasoningOff},
		{name: "zero int", input: 0, kind: KindOff, value: ReasoningOff},
		{name: "low", input: "low", kind: KindNamed, value: ReasoningLow},
		{name: "medium", input: "medium", kind: KindNamed, value: ReasoningMedium},
		{name: "high", input: "high", kind: KindNamed, value: ReasoningHigh},
		{name: "minimal", input: "minimal", kind: KindNamed, value: ReasoningMinimal},
		{name: "x-high", input: "x-high", kind: KindNamed, value: ReasoningXHigh},
		{name: "max", input: "max", kind: KindNamed, value: ReasoningMax},
		{name: "tokens string", input: "1234", kind: KindTokens, value: Reasoning("1234"), tok: 1234},
		{name: "tokens int", input: 1234, kind: KindTokens, value: Reasoning("1234"), tok: 1234},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%v) returned error: %v", tt.input, err)
			}
			if got.Kind != tt.kind || got.Value != tt.value || got.Tokens != tt.tok {
				t.Fatalf("Parse(%v) = %#v, want kind=%s value=%q tokens=%d", tt.input, got, tt.kind, tt.value, tt.tok)
			}
		})
	}
}

func TestBudgetForPortableDefaults(t *testing.T) {
	tests := []struct {
		value Reasoning
		want  int
	}{
		{ReasoningLow, 2048},
		{ReasoningMedium, 8192},
		{ReasoningHigh, 32768},
		{ReasoningTokens(4096), 4096},
	}
	for _, tt := range tests {
		policy, err := Parse(tt.value)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.value, err)
		}
		got, err := BudgetFor(policy, nil, 0)
		if err != nil {
			t.Fatalf("BudgetFor(%q): %v", tt.value, err)
		}
		if got != tt.want {
			t.Fatalf("BudgetFor(%q) = %d, want %d", tt.value, got, tt.want)
		}
	}
}

func TestBudgetForRejectsOverLimitAndUnsupportedNamedValues(t *testing.T) {
	policy, err := Parse(ReasoningTokens(4096))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BudgetFor(policy, nil, 2048); err == nil {
		t.Fatal("expected over-limit numeric budget to fail")
	}

	policy, err = Parse(ReasoningXHigh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BudgetFor(policy, nil, 0); err == nil {
		t.Fatal("expected unsupported extended named value to fail without provider map")
	}
}
