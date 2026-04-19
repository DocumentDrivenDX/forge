package reasoning

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Reasoning is the canonical scalar for model-side reasoning controls.
type Reasoning string

const (
	ReasoningAuto    Reasoning = "auto"
	ReasoningOff     Reasoning = "off"
	ReasoningLow     Reasoning = "low"
	ReasoningMedium  Reasoning = "medium"
	ReasoningHigh    Reasoning = "high"
	ReasoningMinimal Reasoning = "minimal"
	ReasoningXHigh   Reasoning = "xhigh"
	ReasoningMax     Reasoning = "max"
)

// PortableBudgets are fallback named reasoning token budgets.
var PortableBudgets = map[Reasoning]int{
	ReasoningOff:    0,
	ReasoningLow:    2048,
	ReasoningMedium: 8192,
	ReasoningHigh:   32768,
}

// ReasoningTokens returns a numeric reasoning-token request.
func ReasoningTokens(n int) Reasoning {
	return Reasoning(strconv.Itoa(n))
}

type Kind string

const (
	KindUnset  Kind = "unset"
	KindAuto   Kind = "auto"
	KindOff    Kind = "off"
	KindNamed  Kind = "named"
	KindTokens Kind = "tokens"
)

type Policy struct {
	Kind   Kind
	Value  Reasoning
	Tokens int
}

func (p Policy) IsSet() bool {
	return p.Kind != KindUnset
}

func (p Policy) IsExplicitOff() bool {
	return p.Kind == KindOff || (p.Kind == KindTokens && p.Tokens == 0)
}

func Normalize(value string) (Reasoning, error) {
	p, err := ParseString(value)
	if err != nil {
		return "", err
	}
	return p.Value, nil
}

func ParseString(value string) (Policy, error) {
	s := strings.ToLower(strings.TrimSpace(value))
	if s == "" {
		return Policy{Kind: KindUnset}, nil
	}
	switch s {
	case "auto":
		return Policy{Kind: KindAuto, Value: ReasoningAuto}, nil
	case "off", "none", "false":
		return Policy{Kind: KindOff, Value: ReasoningOff}, nil
	case "low":
		return Policy{Kind: KindNamed, Value: ReasoningLow}, nil
	case "medium":
		return Policy{Kind: KindNamed, Value: ReasoningMedium}, nil
	case "high":
		return Policy{Kind: KindNamed, Value: ReasoningHigh}, nil
	case "minimal":
		return Policy{Kind: KindNamed, Value: ReasoningMinimal}, nil
	case "x-high", "x_high", "xhigh":
		return Policy{Kind: KindNamed, Value: ReasoningXHigh}, nil
	case "max":
		return Policy{Kind: KindNamed, Value: ReasoningMax}, nil
	}
	tokens, err := strconv.Atoi(s)
	if err == nil {
		if tokens < 0 {
			return Policy{}, fmt.Errorf("reasoning: negative token budget %d is invalid", tokens)
		}
		if tokens == 0 {
			return Policy{Kind: KindOff, Value: ReasoningOff, Tokens: 0}, nil
		}
		return Policy{Kind: KindTokens, Value: ReasoningTokens(tokens), Tokens: tokens}, nil
	}
	return Policy{}, fmt.Errorf("reasoning: unsupported value %q", value)
}

func Parse(value any) (Policy, error) {
	switch v := value.(type) {
	case nil:
		return Policy{Kind: KindUnset}, nil
	case Reasoning:
		return ParseString(string(v))
	case string:
		return ParseString(v)
	case int:
		return ParseString(strconv.Itoa(v))
	case int64:
		return ParseString(strconv.FormatInt(v, 10))
	case float64:
		if v != float64(int(v)) {
			return Policy{}, fmt.Errorf("reasoning: numeric value %v must be an integer", v)
		}
		return ParseString(strconv.Itoa(int(v)))
	case json.Number:
		i, err := strconv.Atoi(v.String())
		if err != nil {
			return Policy{}, fmt.Errorf("reasoning: numeric value %q must be an integer", v.String())
		}
		return ParseString(strconv.Itoa(i))
	default:
		return Policy{}, fmt.Errorf("reasoning: unsupported scalar type %T", value)
	}
}

func BudgetFor(policy Policy, budgets map[Reasoning]int, maxTokens int) (int, error) {
	switch policy.Kind {
	case KindUnset, KindAuto:
		return 0, nil
	case KindOff:
		return 0, nil
	case KindTokens:
		if maxTokens > 0 && policy.Tokens > maxTokens {
			return 0, fmt.Errorf("reasoning: token budget %d exceeds maximum %d", policy.Tokens, maxTokens)
		}
		return policy.Tokens, nil
	case KindNamed:
		if policy.Value == ReasoningMax {
			if maxTokens <= 0 {
				return 0, fmt.Errorf("reasoning: max requires a known model/provider maximum")
			}
			return maxTokens, nil
		}
		if budgets != nil {
			if budget, ok := budgets[policy.Value]; ok {
				return budget, nil
			}
		}
		if budget, ok := PortableBudgets[policy.Value]; ok {
			if maxTokens > 0 && budget > maxTokens {
				return 0, fmt.Errorf("reasoning: %s budget %d exceeds maximum %d", policy.Value, budget, maxTokens)
			}
			return budget, nil
		}
		return 0, fmt.Errorf("reasoning: unsupported named value %q for numeric budget", policy.Value)
	default:
		return 0, fmt.Errorf("reasoning: unsupported policy kind %q", policy.Kind)
	}
}

func (r *Reasoning) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		normalized, err := Normalize(s)
		if err != nil {
			return err
		}
		*r = normalized
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		policy, err := Parse(n)
		if err != nil {
			return err
		}
		*r = policy.Value
		return nil
	}
	return fmt.Errorf("reasoning: JSON value must be a string or integer")
}

func (r *Reasoning) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		policy, err := ParseString(value.Value)
		if err != nil {
			return err
		}
		*r = policy.Value
		return nil
	default:
		return fmt.Errorf("reasoning: YAML value must be a scalar")
	}
}
