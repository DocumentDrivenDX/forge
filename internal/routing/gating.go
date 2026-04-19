package routing

import (
	"fmt"
	"strings"
)

// Capabilities describes what a (harness, provider, model) tuple can do.
// Populated from harness config + catalog metadata + provider discovery.
type Capabilities struct {
	ContextWindow     int      // resolved tokens; 0 = unknown
	SupportsTools     bool     // supports tool/function calling
	SupportsStreaming bool     // supports streaming responses
	SupportedEfforts  []string // {"low","medium","high"} subset
	SupportedPerms    []string // {"safe","supervised","unrestricted"} subset
	ExactPinSupport   bool     // accepts exact concrete model pins
}

// HasEffort returns true if the candidate supports the requested effort level.
// An empty effort always returns true (no requirement).
func (c Capabilities) HasEffort(effort string) bool {
	if effort == "" {
		return true
	}
	for _, e := range c.SupportedEfforts {
		if strings.EqualFold(e, effort) {
			return true
		}
	}
	return false
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

	// Effort support gating.
	if !cap.HasEffort(req.Effort) {
		return fmt.Sprintf("effort %q not supported", req.Effort)
	}

	// Permissions support gating.
	if !cap.HasPermissions(req.Permissions) {
		return fmt.Sprintf("permissions %q not supported", req.Permissions)
	}

	// Exact-pin gating: an explicit Model field requires ExactPinSupport.
	if req.Model != "" && req.ModelRef == "" && !cap.ExactPinSupport {
		return "exact model pin not supported"
	}

	return ""
}
