package agent

import "github.com/DocumentDrivenDX/agent/internal/harnesses"

// HarnessCapabilityStatus classifies one harness capability in the public
// ListHarnesses capability matrix.
type HarnessCapabilityStatus string

const (
	HarnessCapabilityRequired      HarnessCapabilityStatus = "required"
	HarnessCapabilityOptional      HarnessCapabilityStatus = "optional"
	HarnessCapabilityUnsupported   HarnessCapabilityStatus = "unsupported"
	HarnessCapabilityNotApplicable HarnessCapabilityStatus = "not_applicable"
)

// HarnessCapability describes one capability row for one harness.
type HarnessCapability struct {
	Status HarnessCapabilityStatus
	Detail string
}

// HarnessCapabilityMatrix is the public, per-harness capability table exposed
// by ListHarnesses. The fields intentionally match CONTRACT-003's required
// capability categories.
type HarnessCapabilityMatrix struct {
	ExecutePrompt   HarnessCapability
	ModelDiscovery  HarnessCapability
	ModelPinning    HarnessCapability
	WorkdirContext  HarnessCapability
	ReasoningLevels HarnessCapability
	PermissionModes HarnessCapability
	ProgressEvents  HarnessCapability
	UsageCapture    HarnessCapability
	FinalText       HarnessCapability
	ToolEvents      HarnessCapability
	QuotaStatus     HarnessCapability
	RecordReplay    HarnessCapability
}

func capRequired(detail string) HarnessCapability {
	return HarnessCapability{Status: HarnessCapabilityRequired, Detail: detail}
}

func capOptional(detail string) HarnessCapability {
	return HarnessCapability{Status: HarnessCapabilityOptional, Detail: detail}
}

func capUnsupported(detail string) HarnessCapability {
	return HarnessCapability{Status: HarnessCapabilityUnsupported, Detail: detail}
}

func capNotApplicable(detail string) HarnessCapability {
	return HarnessCapability{Status: HarnessCapabilityNotApplicable, Detail: detail}
}

func harnessCapabilityMatrix(name string, cfg harnesses.HarnessConfig) HarnessCapabilityMatrix {
	return HarnessCapabilityMatrix{
		ExecutePrompt:   executePromptCapability(name, cfg),
		ModelDiscovery:  modelDiscoveryCapability(name, cfg),
		ModelPinning:    modelPinningCapability(cfg),
		WorkdirContext:  workdirContextCapability(name, cfg),
		ReasoningLevels: reasoningCapability(cfg),
		PermissionModes: permissionCapability(cfg),
		ProgressEvents:  progressEventsCapability(name, cfg),
		UsageCapture:    usageCaptureCapability(name, cfg),
		FinalText:       finalTextCapability(name, cfg),
		ToolEvents:      toolEventsCapability(name, cfg),
		QuotaStatus:     quotaStatusCapability(cfg),
		RecordReplay:    recordReplayCapability(cfg),
	}
}

func serviceExecuteWired(name string, cfg harnesses.HarnessConfig) bool {
	switch name {
	case "agent", "claude", "codex", "gemini", "opencode", "pi", "virtual", "script":
		return true
	default:
		return cfg.IsHTTPProvider
	}
}

func executePromptCapability(name string, cfg harnesses.HarnessConfig) HarnessCapability {
	if serviceExecuteWired(name, cfg) {
		return capRequired("Service.Execute has a wired dispatch path for this harness")
	}
	return capUnsupported("registered harness is not wired through Service.Execute yet")
}

func modelDiscoveryCapability(name string, cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly {
		return capNotApplicable("test-only harness has no live model catalog")
	}
	if name == "agent" || cfg.IsHTTPProvider {
		return capOptional("models are discovered through the native provider catalog when configured")
	}
	return capUnsupported("subprocess harness exposes no stable model-discovery API")
}

func modelPinningCapability(cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly {
		return capNotApplicable("test-only harness uses deterministic fixtures or directives")
	}
	if cfg.ExactPinSupport {
		return capOptional("registry marks exact model pinning as supported")
	}
	return capUnsupported("registry does not mark exact model pinning as supported")
}

func workdirContextCapability(name string, cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly {
		return capNotApplicable("test-only harness does not operate on a real workdir")
	}
	if name == "agent" || cfg.WorkDirFlag != "" {
		return capOptional("harness accepts an explicit workdir/context")
	}
	if name == "claude" || name == "gemini" || name == "pi" {
		return capOptional("service runner sets the subprocess working directory")
	}
	return capUnsupported("no explicit workdir/context support is registered")
}

func reasoningCapability(cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly {
		return capNotApplicable("test-only harness does not perform model reasoning")
	}
	if len(cfg.ReasoningLevels) > 0 || cfg.MaxReasoningTokens > 0 {
		return capOptional("registry declares supported reasoning levels or token budget")
	}
	return capUnsupported("registry declares no reasoning control")
}

func permissionCapability(cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly {
		return capNotApplicable("test-only harness does not enforce tool permissions")
	}
	if len(supportedPermissions(cfg)) > 0 {
		return capOptional("registry declares permission modes")
	}
	return capUnsupported("registry declares no permission modes")
}

func progressEventsCapability(name string, cfg harnesses.HarnessConfig) HarnessCapability {
	if serviceExecuteWired(name, cfg) {
		return capRequired("Service.Execute emits routing/progress/final events")
	}
	return capUnsupported("progress events are unavailable until Service.Execute dispatch is wired")
}

func usageCaptureCapability(name string, cfg harnesses.HarnessConfig) HarnessCapability {
	if serviceExecuteWired(name, cfg) {
		return capOptional("usage capture is best-effort and reported on final events when available")
	}
	return capUnsupported("usage capture is unavailable until Service.Execute dispatch is wired")
}

func finalTextCapability(name string, cfg harnesses.HarnessConfig) HarnessCapability {
	if name == "virtual" || name == "script" {
		return capOptional("test-only final events include deterministic final_text")
	}
	if cfg.TestOnly {
		return capNotApplicable("test-only harness does not expose normalized live response text")
	}
	switch name {
	case "agent", "codex", "claude", "gemini", "opencode", "pi":
		return capOptional("final events include normalized final_text when response text is available")
	default:
		if cfg.IsHTTPProvider {
			return capOptional("native-provider final events include normalized final_text when response text is available")
		}
		return capUnsupported("final events do not expose normalized final response text")
	}
}

func toolEventsCapability(name string, cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly {
		return capNotApplicable("test-only harness does not expose live tool events")
	}
	switch name {
	case "agent", "claude", "codex":
		return capOptional("Service.Execute emits tool_call and tool_result events")
	default:
		return capUnsupported("tool-call/tool-result events are not exposed for this harness")
	}
}

func quotaStatusCapability(cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly || cfg.IsLocal {
		return capNotApplicable("local or test-only harness has no subscription quota")
	}
	if cfg.IsSubscription && cfg.TUIQuotaCommand != "" {
		return capOptional("subscription quota can be probed or read from a cache")
	}
	return capUnsupported("no quota/status monitor is registered")
}

func recordReplayCapability(cfg harnesses.HarnessConfig) HarnessCapability {
	if cfg.TestOnly {
		return capRequired("test-only harness provides deterministic replay or directive execution")
	}
	return capUnsupported("production harness does not provide deterministic record/replay")
}
