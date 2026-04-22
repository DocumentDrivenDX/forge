package routing

import (
	"fmt"
	"strings"

	"github.com/DocumentDrivenDX/agent/internal/reasoning"
)

// Capabilities describes what a (harness, provider, model) tuple can do.
// Populated from harness config + catalog metadata + provider discovery.
type Capabilities struct {
	ContextWindow      int      // resolved tokens; 0 = unknown
	SupportsTools      bool     // supports tool/function calling
	SupportsStreaming  bool     // supports streaming responses
	SupportedReasoning []string // supported public reasoning values
	MaxReasoningTokens int      // 0 means numeric reasoning is unsupported/unknown
	SupportedPerms     []string // {"safe","supervised","unrestricted"} subset
	ExactPinSupport    bool     // accepts exact concrete model pins
	SupportedModels    []string // nil = no static allow-list
}

// HasReasoning returns true if the candidate supports the requested reasoning
// value. Empty, auto, off, and numeric 0 impose no requirement.
func (c Capabilities) HasReasoning(value string) bool {
	policy, err := reasoning.ParseString(value)
	if err != nil {
		return false
	}
	switch policy.Kind {
	case reasoning.KindUnset, reasoning.KindAuto, reasoning.KindOff:
		return true
	case reasoning.KindTokens:
		return c.MaxReasoningTokens > 0 && policy.Tokens <= c.MaxReasoningTokens
	case reasoning.KindNamed:
		for _, supported := range c.SupportedReasoning {
			normalized, err := reasoning.Normalize(supported)
			if err == nil && normalized == policy.Value {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// HasPermissions returns true if the candidate supports the requested level.
// An empty permission level always returns true.
func (c Capabilities) HasPermissions(perm string) bool {
	if perm == "" {
		return true
	}
	for _, p := range c.SupportedPerms {
		if strings.EqualFold(p, perm) {
			return true
		}
	}
	return false
}

// HasModel returns true when the exact model pin is within the static
// harness allow-list. A nil allow-list means the harness delegates validation
// to provider-side model checks.
func (c Capabilities) HasModel(model string) bool {
	if c.SupportedModels == nil || model == "" {
		return true
	}
	for _, supported := range c.SupportedModels {
		if supported == model {
			return true
		}
	}
	return false
}

// CheckGating applies all capability gates against a request and returns
// the first failure reason, or "" if all gates pass.
//
// Fixes ddx-4817edfd subtree: pre-dispatch capability check covering
// context window, tool support, effort, permissions.
func CheckGating(cap Capabilities, req Request) string {
	// Context window gating: if the request declares prompt size or an effort
	// that implies a minimum context, reject candidates that can't fit.
	minCtx := req.MinContextWindow()
	if minCtx > 0 && cap.ContextWindow > 0 && cap.ContextWindow < minCtx {
		return fmt.Sprintf("context window %d < required %d", cap.ContextWindow, minCtx)
	}

	// Tool-calling support gating.
	if req.RequiresTools && !cap.SupportsTools {
		return "tool calling not supported"
	}

	if !cap.HasReasoning(req.Reasoning) {
		return fmt.Sprintf("reasoning %q not supported", req.Reasoning)
	}

	// Permissions support gating.
	if !cap.HasPermissions(req.Permissions) {
		return fmt.Sprintf("permissions %q not supported", req.Permissions)
	}

	if req.Model != "" && !cap.HasModel(req.Model) {
		return "model not in harness allow-list"
	}

	// Exact-pin gating: an explicit Model field requires ExactPinSupport.
	if req.Model != "" && req.ModelRef == "" && !cap.ExactPinSupport {
		return "exact model pin not supported"
	}

	return ""
}
