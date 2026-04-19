package agent

import "github.com/DocumentDrivenDX/agent/internal/reasoning"

func effectiveReasoning(value Reasoning) Reasoning {
	if value != "" {
		normalized, err := reasoning.Normalize(string(value))
		if err == nil {
			return normalized
		}
		return value
	}
	return ""
}

func effectiveReasoningString(value Reasoning) string {
	return string(effectiveReasoning(value))
}

func adapterReasoning(value Reasoning) string {
	policy, err := reasoning.ParseString(effectiveReasoningString(value))
	if err != nil {
		return effectiveReasoningString(value)
	}
	switch policy.Kind {
	case reasoning.KindUnset, reasoning.KindAuto, reasoning.KindOff:
		return ""
	case reasoning.KindTokens:
		if policy.Tokens == 0 {
			return ""
		}
		return string(policy.Value)
	case reasoning.KindNamed:
		return string(policy.Value)
	default:
		return effectiveReasoningString(value)
	}
}
