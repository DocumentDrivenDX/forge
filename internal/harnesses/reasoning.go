package harnesses

import "github.com/DocumentDrivenDX/agent/internal/reasoning"

// AdapterReasoningValue resolves the public reasoning scalar into the value
// subprocess harnesses should pass to their native CLI flag. Empty, auto, off,
// and numeric 0 intentionally emit no flag.
func AdapterReasoningValue(req ExecuteRequest) string {
	value := req.Reasoning
	policy, err := reasoning.ParseString(value)
	if err != nil {
		return value
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
		return value
	}
}
