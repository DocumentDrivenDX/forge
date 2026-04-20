package agent_test

import (
	"context"
	"path/filepath"
	"testing"

	agent "github.com/DocumentDrivenDX/agent"
)

func TestListHarnesses_shape(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, ".config"))
	svc, err := agent.New(agent.ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	list, err := svc.ListHarnesses(context.Background())
	if err != nil {
		t.Fatalf("ListHarnesses: %v", err)
	}

	if len(list) == 0 {
		t.Fatal("expected at least one harness")
	}

	// Index by name for targeted assertions.
	byName := make(map[string]agent.HarnessInfo, len(list))
	for _, h := range list {
		if h.Name == "" {
			t.Errorf("harness with empty Name found")
		}
		byName[h.Name] = h
	}

	// All builtins must appear.
	expected := []string{
		"codex", "claude", "gemini", "opencode", "agent",
		"pi", "virtual", "script", "openrouter", "lmstudio", "omlx",
	}
	for _, name := range expected {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing harness %q", name)
		}
	}

	t.Run("codex", func(t *testing.T) {
		h := byName["codex"]
		assertContains(t, h.SupportedPermissions, "safe", "codex permissions")
		assertContains(t, h.SupportedPermissions, "supervised", "codex permissions")
		assertContains(t, h.SupportedPermissions, "unrestricted", "codex permissions")
		assertContains(t, h.SupportedReasoning, "low", "codex reasoning")
		assertContains(t, h.SupportedReasoning, "medium", "codex reasoning")
		assertContains(t, h.SupportedReasoning, "high", "codex reasoning")
		assertContains(t, h.SupportedReasoning, "xhigh", "codex reasoning")
		assertContains(t, h.SupportedReasoning, "max", "codex reasoning")
		if h.CostClass != "medium" {
			t.Errorf("codex CostClass: want medium, got %q", h.CostClass)
		}
		if h.IsSubscription != true {
			t.Errorf("codex IsSubscription: want true")
		}
		if h.IsLocal {
			t.Errorf("codex IsLocal: want false")
		}
		if h.Type != "subprocess" {
			t.Errorf("codex Type: want subprocess, got %q", h.Type)
		}
		if h.DefaultModel != "gpt-5.4" {
			t.Errorf("codex DefaultModel: want gpt-5.4, got %q", h.DefaultModel)
		}
	})

	t.Run("claude", func(t *testing.T) {
		h := byName["claude"]
		assertContains(t, h.SupportedPermissions, "safe", "claude permissions")
		assertContains(t, h.SupportedPermissions, "unrestricted", "claude permissions")
		assertContains(t, h.SupportedReasoning, "low", "claude reasoning")
		assertContains(t, h.SupportedReasoning, "high", "claude reasoning")
		assertContains(t, h.SupportedReasoning, "xhigh", "claude reasoning")
		assertContains(t, h.SupportedReasoning, "max", "claude reasoning")
		if h.CostClass != "medium" {
			t.Errorf("claude CostClass: want medium, got %q", h.CostClass)
		}
		if h.IsSubscription != true {
			t.Errorf("claude IsSubscription: want true")
		}
		if h.Type != "subprocess" {
			t.Errorf("claude Type: want subprocess, got %q", h.Type)
		}
		if h.DefaultModel != "claude-sonnet-4-6" {
			t.Errorf("claude DefaultModel: want claude-sonnet-4-6, got %q", h.DefaultModel)
		}
		// Quota may be nil (no cache on CI); just check it doesn't panic.
		_ = h.Quota
	})

	t.Run("agent_native", func(t *testing.T) {
		h := byName["agent"]
		if h.Type != "native" {
			t.Errorf("agent Type: want native, got %q", h.Type)
		}
		if !h.IsLocal {
			t.Errorf("agent IsLocal: want true")
		}
		if h.CostClass != "local" {
			t.Errorf("agent CostClass: want local, got %q", h.CostClass)
		}
		if !h.Available {
			t.Errorf("agent Available: want true (embedded)")
		}
		if h.DefaultModel != "" {
			t.Errorf("agent DefaultModel: want empty, got %q", h.DefaultModel)
		}
	})

	t.Run("openrouter_native", func(t *testing.T) {
		h := byName["openrouter"]
		if h.Type != "native" {
			t.Errorf("openrouter Type: want native, got %q", h.Type)
		}
		if h.CostClass != "medium" {
			t.Errorf("openrouter CostClass: want medium, got %q", h.CostClass)
		}
	})

	t.Run("lmstudio_local", func(t *testing.T) {
		h := byName["lmstudio"]
		if h.CostClass != "local" {
			t.Errorf("lmstudio CostClass: want local, got %q", h.CostClass)
		}
		if !h.IsLocal {
			t.Errorf("lmstudio IsLocal: want true")
		}
	})

	t.Run("omlx_local", func(t *testing.T) {
		h := byName["omlx"]
		if h.Type != "native" {
			t.Errorf("omlx Type: want native, got %q", h.Type)
		}
		if h.CostClass != "local" {
			t.Errorf("omlx CostClass: want local, got %q", h.CostClass)
		}
		if !h.IsLocal {
			t.Errorf("omlx IsLocal: want true")
		}
	})

	t.Run("gemini_permissions_nil", func(t *testing.T) {
		h := byName["gemini"]
		// gemini has no PermissionArgs → SupportedPermissions should be nil/empty
		if len(h.SupportedPermissions) != 0 {
			t.Errorf("gemini SupportedPermissions: want empty, got %v", h.SupportedPermissions)
		}
	})

	t.Run("opencode_permissions_all_levels", func(t *testing.T) {
		h := byName["opencode"]
		assertContains(t, h.SupportedPermissions, "safe", "opencode permissions")
		assertContains(t, h.SupportedPermissions, "supervised", "opencode permissions")
		assertContains(t, h.SupportedPermissions, "unrestricted", "opencode permissions")
		// opencode has non-standard effort levels; only std ones count.
		assertContains(t, h.SupportedReasoning, "low", "opencode reasoning")
		assertContains(t, h.SupportedReasoning, "medium", "opencode reasoning")
		assertContains(t, h.SupportedReasoning, "high", "opencode reasoning")
		assertContains(t, h.SupportedReasoning, "minimal", "opencode reasoning")
		assertContains(t, h.SupportedReasoning, "max", "opencode reasoning")
	})

	t.Run("capability_matrix_all_harnesses", func(t *testing.T) {
		expected := map[string]agent.HarnessCapabilityMatrix{
			"codex": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityUnsupported),
				ModelPinning:    capStatus(agent.HarnessCapabilityOptional),
				WorkdirContext:  capStatus(agent.HarnessCapabilityOptional),
				ReasoningLevels: capStatus(agent.HarnessCapabilityOptional),
				PermissionModes: capStatus(agent.HarnessCapabilityOptional),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityUnsupported),
				QuotaStatus:     capStatus(agent.HarnessCapabilityOptional),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"claude": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityUnsupported),
				ModelPinning:    capStatus(agent.HarnessCapabilityOptional),
				WorkdirContext:  capStatus(agent.HarnessCapabilityOptional),
				ReasoningLevels: capStatus(agent.HarnessCapabilityOptional),
				PermissionModes: capStatus(agent.HarnessCapabilityOptional),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityOptional),
				QuotaStatus:     capStatus(agent.HarnessCapabilityOptional),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"gemini": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityUnsupported),
				ModelPinning:    capStatus(agent.HarnessCapabilityOptional),
				WorkdirContext:  capStatus(agent.HarnessCapabilityOptional),
				ReasoningLevels: capStatus(agent.HarnessCapabilityUnsupported),
				PermissionModes: capStatus(agent.HarnessCapabilityUnsupported),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityUnsupported),
				QuotaStatus:     capStatus(agent.HarnessCapabilityUnsupported),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"opencode": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityUnsupported),
				ModelPinning:    capStatus(agent.HarnessCapabilityOptional),
				WorkdirContext:  capStatus(agent.HarnessCapabilityOptional),
				ReasoningLevels: capStatus(agent.HarnessCapabilityOptional),
				PermissionModes: capStatus(agent.HarnessCapabilityOptional),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityUnsupported),
				QuotaStatus:     capStatus(agent.HarnessCapabilityUnsupported),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"agent": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityOptional),
				ModelPinning:    capStatus(agent.HarnessCapabilityOptional),
				WorkdirContext:  capStatus(agent.HarnessCapabilityOptional),
				ReasoningLevels: capStatus(agent.HarnessCapabilityOptional),
				PermissionModes: capStatus(agent.HarnessCapabilityUnsupported),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityOptional),
				QuotaStatus:     capStatus(agent.HarnessCapabilityNotApplicable),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"pi": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityUnsupported),
				ModelPinning:    capStatus(agent.HarnessCapabilityOptional),
				WorkdirContext:  capStatus(agent.HarnessCapabilityOptional),
				ReasoningLevels: capStatus(agent.HarnessCapabilityOptional),
				PermissionModes: capStatus(agent.HarnessCapabilityUnsupported),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityUnsupported),
				QuotaStatus:     capStatus(agent.HarnessCapabilityUnsupported),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"virtual": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityNotApplicable),
				ModelPinning:    capStatus(agent.HarnessCapabilityNotApplicable),
				WorkdirContext:  capStatus(agent.HarnessCapabilityNotApplicable),
				ReasoningLevels: capStatus(agent.HarnessCapabilityNotApplicable),
				PermissionModes: capStatus(agent.HarnessCapabilityNotApplicable),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityNotApplicable),
				QuotaStatus:     capStatus(agent.HarnessCapabilityNotApplicable),
				RecordReplay:    capStatus(agent.HarnessCapabilityRequired),
			},
			"script": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityNotApplicable),
				ModelPinning:    capStatus(agent.HarnessCapabilityNotApplicable),
				WorkdirContext:  capStatus(agent.HarnessCapabilityNotApplicable),
				ReasoningLevels: capStatus(agent.HarnessCapabilityNotApplicable),
				PermissionModes: capStatus(agent.HarnessCapabilityNotApplicable),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityNotApplicable),
				QuotaStatus:     capStatus(agent.HarnessCapabilityNotApplicable),
				RecordReplay:    capStatus(agent.HarnessCapabilityRequired),
			},
			"openrouter": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityOptional),
				ModelPinning:    capStatus(agent.HarnessCapabilityUnsupported),
				WorkdirContext:  capStatus(agent.HarnessCapabilityUnsupported),
				ReasoningLevels: capStatus(agent.HarnessCapabilityUnsupported),
				PermissionModes: capStatus(agent.HarnessCapabilityUnsupported),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityUnsupported),
				QuotaStatus:     capStatus(agent.HarnessCapabilityUnsupported),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"lmstudio": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityOptional),
				ModelPinning:    capStatus(agent.HarnessCapabilityUnsupported),
				WorkdirContext:  capStatus(agent.HarnessCapabilityUnsupported),
				ReasoningLevels: capStatus(agent.HarnessCapabilityUnsupported),
				PermissionModes: capStatus(agent.HarnessCapabilityUnsupported),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityUnsupported),
				QuotaStatus:     capStatus(agent.HarnessCapabilityNotApplicable),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
			"omlx": {
				ExecutePrompt:   capStatus(agent.HarnessCapabilityRequired),
				ModelDiscovery:  capStatus(agent.HarnessCapabilityOptional),
				ModelPinning:    capStatus(agent.HarnessCapabilityUnsupported),
				WorkdirContext:  capStatus(agent.HarnessCapabilityUnsupported),
				ReasoningLevels: capStatus(agent.HarnessCapabilityUnsupported),
				PermissionModes: capStatus(agent.HarnessCapabilityUnsupported),
				ProgressEvents:  capStatus(agent.HarnessCapabilityRequired),
				UsageCapture:    capStatus(agent.HarnessCapabilityOptional),
				FinalText:       capStatus(agent.HarnessCapabilityOptional),
				ToolEvents:      capStatus(agent.HarnessCapabilityUnsupported),
				QuotaStatus:     capStatus(agent.HarnessCapabilityNotApplicable),
				RecordReplay:    capStatus(agent.HarnessCapabilityUnsupported),
			},
		}

		for name, want := range expected {
			got := byName[name].CapabilityMatrix
			assertCapabilityMatrix(t, name, got, want)
		}
	})
}

func assertContains(t *testing.T, slice []string, want, context string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Errorf("%s: want %q in %v", context, want, slice)
}

func capStatus(status agent.HarnessCapabilityStatus) agent.HarnessCapability {
	return agent.HarnessCapability{Status: status}
}

func assertCapabilityMatrix(t *testing.T, name string, got, want agent.HarnessCapabilityMatrix) {
	t.Helper()
	assertCapability(t, name, "ExecutePrompt", got.ExecutePrompt, want.ExecutePrompt)
	assertCapability(t, name, "ModelDiscovery", got.ModelDiscovery, want.ModelDiscovery)
	assertCapability(t, name, "ModelPinning", got.ModelPinning, want.ModelPinning)
	assertCapability(t, name, "WorkdirContext", got.WorkdirContext, want.WorkdirContext)
	assertCapability(t, name, "ReasoningLevels", got.ReasoningLevels, want.ReasoningLevels)
	assertCapability(t, name, "PermissionModes", got.PermissionModes, want.PermissionModes)
	assertCapability(t, name, "ProgressEvents", got.ProgressEvents, want.ProgressEvents)
	assertCapability(t, name, "UsageCapture", got.UsageCapture, want.UsageCapture)
	assertCapability(t, name, "FinalText", got.FinalText, want.FinalText)
	assertCapability(t, name, "ToolEvents", got.ToolEvents, want.ToolEvents)
	assertCapability(t, name, "QuotaStatus", got.QuotaStatus, want.QuotaStatus)
	assertCapability(t, name, "RecordReplay", got.RecordReplay, want.RecordReplay)
}

func assertCapability(t *testing.T, harness, field string, got, want agent.HarnessCapability) {
	t.Helper()
	if got.Status != want.Status {
		t.Errorf("%s.%s Status: got %q, want %q", harness, field, got.Status, want.Status)
	}
	if got.Detail == "" {
		t.Errorf("%s.%s Detail: should explain the capability status", harness, field)
	}
}
