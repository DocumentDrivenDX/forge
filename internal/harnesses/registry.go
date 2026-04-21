package harnesses

import "github.com/DocumentDrivenDX/agent/internal/productinfo"

// PreferenceOrder defines the default harness preference when multiple are available.
var PreferenceOrder = []string{"codex", "claude", "opencode", "agent", "pi", "openrouter", "lmstudio", "omlx", "gemini"}

// builtinHarnesses defines known harnesses and how to invoke them.
var builtinHarnesses = map[string]HarnessConfig{
	"codex": {
		Name:     "codex",
		Binary:   "codex",
		BaseArgs: []string{"exec", "--json"},
		PermissionArgs: map[string][]string{
			"safe":         {},
			"supervised":   {},
			"unrestricted": {"--dangerously-bypass-approvals-and-sandbox"},
		},
		PromptMode:          "arg",
		DefaultModel:        "gpt-5.4",
		Models:              []string{"gpt-5.4"}, // refreshed by Codex PTY model-discovery cassettes
		ReasoningLevels:     []string{"low", "medium", "high", "xhigh", "max"},
		ModelFlag:           "-m",
		WorkDirFlag:         "-C",
		ReasoningFlag:       "-c",
		ReasoningFormat:     "reasoning.effort=%s",
		Surface:             "codex",
		CostClass:           "medium",
		IsLocal:             false,
		IsSubscription:      true,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
		TUIQuotaCommand:     "/status",
	},
	// #nosec G101 -- not credentials; this is the harness invocation config
	// for the claude CLI (binary name, flags, permission mode strings).
	"claude": {
		Name:   "claude",
		Binary: "claude",
		// stream-json emits one JSON event per stdout line while the agent runs,
		// which lets the service surface real-time progress (tool calls, turn counts,
		// elapsed) instead of blocking until completion. --verbose is required
		// by claude CLI when --output-format=stream-json is combined with --print.
		BaseArgs: []string{"--print", "-p", "--verbose", "--output-format", "stream-json"},
		PermissionArgs: map[string][]string{
			"safe":         {},
			"supervised":   {"--permission-mode", "default"},
			"unrestricted": {"--permission-mode", "bypassPermissions", "--dangerously-skip-permissions"},
		},
		PromptMode:          "arg",
		DefaultModel:        "claude-sonnet-4-6",
		Models:              []string{"sonnet", "opus", "claude-sonnet-4-6"}, // from claude --help plus PTY model-discovery cassettes
		ReasoningLevels:     []string{"low", "medium", "high", "xhigh", "max"},
		ModelFlag:           "--model",
		WorkDirFlag:         "",
		ReasoningFlag:       "--effort",
		TokenPattern:        `(?i)total tokens[:\s]+([0-9,]+)`,
		Surface:             "claude",
		CostClass:           "medium",
		IsLocal:             false,
		IsSubscription:      true,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
		TUIQuotaCommand:     "/usage",
	},
	"gemini": {
		Name:            "gemini",
		Binary:          "gemini",
		BaseArgs:        []string{},
		PromptMode:      "stdin",
		ModelFlag:       "-m",
		ReasoningLevels: nil,
		Surface:         "gemini",
		CostClass:       "experimental",
		IsLocal:         false,
		ExactPinSupport: true,
	},
	"opencode": {
		Name:     "opencode",
		Binary:   "opencode",
		BaseArgs: []string{"run", "--format", "json"},
		PermissionArgs: map[string][]string{
			// opencode run auto-approves all tool permissions;
			// no separate flags needed for any permission level.
			"safe":         {},
			"supervised":   {},
			"unrestricted": {},
		},
		PromptMode:          "arg",
		DefaultModel:        "opencode/gpt-5.4",
		Models:              []string{"opencode/gpt-5.4", "opencode/claude-sonnet-4-6"},
		ReasoningLevels:     []string{"minimal", "low", "medium", "high", "max"},
		ModelFlag:           "-m",
		WorkDirFlag:         "--dir",
		ReasoningFlag:       "--variant",
		Surface:             "embedded-openai",
		CostClass:           "medium",
		IsLocal:             false,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
	},
	"agent": {
		Name:                "agent",
		Binary:              productinfo.BinaryName, // embedded — runs in-process via the agent library, not as a subprocess
		PermissionArgs:      map[string][]string{"safe": {}, "unrestricted": {}},
		PromptMode:          "arg",
		DefaultModel:        "", // uses agent config or provider default
		ReasoningLevels:     []string{"low", "medium", "high"},
		MaxReasoningTokens:  32768,
		Surface:             "embedded-openai",
		CostClass:           "local",
		IsLocal:             true,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
	},
	"pi": {
		Name:                "pi",
		Binary:              "pi",
		BaseArgs:            []string{"--mode", "json", "--print"},
		PromptMode:          "arg",
		DefaultModel:        "gemini-2.5-flash",
		Models:              []string{"gemini-2.5-flash", "gemini-2.5-pro"},
		ModelFlag:           "--model",
		ReasoningFlag:       "--thinking",
		ReasoningLevels:     []string{"minimal", "low", "medium", "high", "xhigh"},
		Surface:             "pi",
		CostClass:           "medium",
		IsLocal:             false,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
	},
	"virtual": {
		Name:         "virtual",
		Binary:       "ddx-virtual-agent", // sentinel — never actually exec'd
		PromptMode:   "arg",
		DefaultModel: "recorded",
		Surface:      "virtual",
		CostClass:    "local",
		IsLocal:      true,
		TestOnly:     true, // test-only replay harness; never selected by production tier routing
	},
	"script": {
		Name:       "script",
		Binary:     "ddx-script-agent", // sentinel — never actually exec'd
		PromptMode: "arg",
		Surface:    "script",
		CostClass:  "local",
		IsLocal:    true,
		TestOnly:   true, // test-only directive interpreter; never selected by production tier routing
	},
	"openrouter": {
		Name:           "openrouter",
		Binary:         "",
		Surface:        "embedded-openai",
		CostClass:      "medium",
		IsHTTPProvider: true,
	},
	"lmstudio": {
		Name:           "lmstudio",
		Binary:         "",
		Surface:        "embedded-openai",
		CostClass:      "local",
		IsHTTPProvider: true,
		IsLocal:        true,
	},
	"omlx": {
		Name:           "omlx",
		Binary:         "",
		Surface:        "embedded-openai",
		CostClass:      "local",
		IsHTTPProvider: true,
		IsLocal:        true,
	},
}

// harnessAliases maps convenience names to canonical harness names.
// "local" always routes to the embedded agent; it must never
// fall through to a cloud harness like claude or codex.
var harnessAliases = map[string]string{
	"local": "agent",
}

// ResolveHarnessAlias returns the canonical harness name for an alias,
// or the input unchanged if it is not an alias.
func ResolveHarnessAlias(name string) string {
	if canonical, ok := harnessAliases[name]; ok {
		return canonical
	}
	return name
}

// Registry manages known harnesses.
type Registry struct {
	LookPath  LookPathFunc
	harnesses map[string]HarnessConfig
}

// NewRegistry creates a registry with builtin harnesses.
func NewRegistry() *Registry {
	r := &Registry{
		LookPath:  DefaultLookPath,
		harnesses: make(map[string]HarnessConfig),
	}
	for k, v := range builtinHarnesses {
		r.harnesses[k] = v
	}
	return r
}

// Get returns a harness config by name.
func (r *Registry) Get(name string) (HarnessConfig, bool) {
	h, ok := r.harnesses[name]
	return h, ok
}

// Has returns true if the harness is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.harnesses[name]
	return ok
}

// Names returns all registered harness names in preference order.
func (r *Registry) Names() []string {
	var names []string
	// First add preferred harnesses that exist in registry
	for _, name := range PreferenceOrder {
		if _, ok := r.harnesses[name]; ok {
			names = append(names, name)
		}
	}
	// Then add any extras not in preference list
	for name := range r.harnesses {
		found := false
		for _, pref := range PreferenceOrder {
			if name == pref {
				found = true
				break
			}
		}
		if !found {
			names = append(names, name)
		}
	}
	return names
}

// Discover checks which harnesses are available on the system.
func (r *Registry) Discover() []HarnessStatus {
	var statuses []HarnessStatus
	lookPath := r.LookPath
	if lookPath == nil {
		lookPath = DefaultLookPath
	}
	for _, name := range r.Names() {
		h := r.harnesses[name]
		status := HarnessStatus{
			Name:   name,
			Binary: h.Binary,
		}
		// Embedded harnesses are always available — no binary lookup needed.
		if name == "virtual" || name == "agent" || name == "script" {
			status.Available = true
			status.Path = "(embedded)"
		} else if h.IsHTTPProvider {
			// HTTP-only providers: availability determined by probe, not binary.
			status.Available = true
			status.Path = "(http)"
		} else if path, err := lookPath(h.Binary); err != nil {
			status.Available = false
			status.Error = "binary not found"
		} else {
			status.Available = true
			status.Path = path
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// FirstAvailable returns the first available harness in preference order.
func (r *Registry) FirstAvailable() (string, bool) {
	for _, s := range r.Discover() {
		if s.Available {
			return s.Name, true
		}
	}
	return "", false
}
