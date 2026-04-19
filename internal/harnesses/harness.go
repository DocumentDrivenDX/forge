package harnesses

import "os/exec"

// HarnessConfig defines a known agent harness's invocation metadata.
// This is a configuration struct (not an interface) that captures binary,
// args, flags, and capability metadata for each builtin harness.
type HarnessConfig struct {
	Name               string              // e.g. "codex", "claude", "gemini"
	Binary             string              // binary name to exec
	BaseArgs           []string            // args always included regardless of permission level
	PermissionArgs     map[string][]string // extra args keyed by permission level: "safe", "supervised", "unrestricted"
	PromptMode         string              // "arg" (final arg), "stdin" (pipe)
	DefaultModel       string              // built-in model choice when no config override exists
	Models             []string            // known valid models for this harness
	ReasoningLevels    []string            // supported reasoning levels in preference order
	MaxReasoningTokens int                 // numeric reasoning budget max; 0 = unsupported/unknown
	ModelFlag          string              // flag for model override (e.g. "-m", "--model"), empty if unsupported
	WorkDirFlag        string              // flag for working directory (e.g. "-C", "--cwd"), empty if unsupported
	ReasoningFlag      string              // adapter flag for reasoning control, empty if unsupported
	ReasoningFormat    string              // format string for adapter reasoning value, empty = use value directly
	TokenPattern       string              // regex to extract token count from output, must have one capture group
	Surface            string              // catalog surface identifier: "codex", "claude", "embedded-openai", "embedded-anthropic"
	CostClass          string              // local, cheap, medium, expensive
	IsLocal            bool                // true for embedded/local harnesses (no cloud cost)
	ExactPinSupport    bool                // true if harness can accept an exact concrete model pin
	QuotaCommand       string              // CLI args for non-interactive quota introspection; empty = skip probe
	TUIQuotaCommand    string              // Slash command to send as a prompt when native quota signal is unavailable
	IsHTTPProvider     bool                // true for API-only providers (openrouter, lmstudio) that have no CLI binary
	IsSubscription     bool                // true for fixed-subscription harnesses (codex, claude)
	TestOnly           bool                // true for sentinel/test harnesses that must never be selected by production tier routing
}

// LookPathFunc abstracts binary discovery for testability.
type LookPathFunc func(file string) (string, error)

// DefaultLookPath is the production implementation.
var DefaultLookPath LookPathFunc = exec.LookPath

// HarnessStatus reports availability of a harness.
type HarnessStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Binary    string `json:"binary"`
	Path      string `json:"path,omitempty"` // resolved binary path
	Error     string `json:"error,omitempty"`
}

// HarnessState captures the runtime routing-relevant state of a harness.
type HarnessState struct {
	Installed           bool                        `json:"installed"`
	Reachable           bool                        `json:"reachable"`
	Authenticated       bool                        `json:"authenticated"`
	QuotaOK             bool                        `json:"quota_ok"`
	QuotaState          string                      `json:"quota_state,omitempty"` // ok, blocked, unknown
	Degraded            bool                        `json:"degraded"`
	PolicyOK            bool                        `json:"policy_ok"`
	LastCheckedUnix     int64                       `json:"last_checked_unix,omitempty"`
	Error               string                      `json:"error,omitempty"`
	Quota               *QuotaInfo                  `json:"quota,omitempty"`
	ClaudeQuotaDecision *ClaudeQuotaRoutingDecision `json:"claude_quota_decision,omitempty"`
}

// QuotaInfo holds parsed quota data from CLI introspection.
type QuotaInfo struct {
	PercentUsed int    `json:"percent_used"`
	LimitWindow string `json:"limit_window,omitempty"` // e.g. "5h", "7 day"
	ResetDate   string `json:"reset_date,omitempty"`   // e.g. "April 12"
}

// ClaudeQuotaRoutingDecision is the result of evaluating the durable Claude
// quota cache for foreground routing decisions.
type ClaudeQuotaRoutingDecision struct {
	Fresh        bool   `json:"fresh"`
	PreferClaude bool   `json:"prefer_claude"`
	Reason       string `json:"reason,omitempty"`
}
