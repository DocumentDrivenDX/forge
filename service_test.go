package agent_test

import (
	"context"
	"testing"

	agent "github.com/DocumentDrivenDX/agent"
)

func TestListHarnesses_shape(t *testing.T) {
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

	// All 10 builtins must appear.
	expected := []string{
		"codex", "claude", "gemini", "opencode", "agent",
		"pi", "virtual", "script", "openrouter", "lmstudio",
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
		assertContains(t, h.SupportedEfforts, "low", "codex efforts")
		assertContains(t, h.SupportedEfforts, "medium", "codex efforts")
		assertContains(t, h.SupportedEfforts, "high", "codex efforts")
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
	})

	t.Run("claude", func(t *testing.T) {
		h := byName["claude"]
		assertContains(t, h.SupportedPermissions, "safe", "claude permissions")
		assertContains(t, h.SupportedPermissions, "unrestricted", "claude permissions")
		assertContains(t, h.SupportedEfforts, "low", "claude efforts")
		assertContains(t, h.SupportedEfforts, "high", "claude efforts")
		if h.CostClass != "medium" {
			t.Errorf("claude CostClass: want medium, got %q", h.CostClass)
		}
		if h.IsSubscription != true {
			t.Errorf("claude IsSubscription: want true")
		}
		if h.Type != "subprocess" {
			t.Errorf("claude Type: want subprocess, got %q", h.Type)
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
		assertContains(t, h.SupportedEfforts, "low", "opencode efforts")
		assertContains(t, h.SupportedEfforts, "medium", "opencode efforts")
		assertContains(t, h.SupportedEfforts, "high", "opencode efforts")
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
