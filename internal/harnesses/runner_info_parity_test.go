package harnesses_test

import (
	"sort"
	"testing"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	geminiharness "github.com/DocumentDrivenDX/agent/internal/harnesses/gemini"
	opencodeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/opencode"
	piharness "github.com/DocumentDrivenDX/agent/internal/harnesses/pi"
)

func TestRunnerInfoMatchesRegistryMetadata(t *testing.T) {
	registry := harnesses.NewRegistry()
	cases := []struct {
		name string
		info harnesses.HarnessInfo
	}{
		{name: "codex", info: (&codexharness.Runner{Binary: "/test/codex"}).Info()},
		{name: "claude", info: (&claudeharness.Runner{Binary: "/test/claude"}).Info()},
		{name: "gemini", info: (&geminiharness.Runner{Binary: "/test/gemini"}).Info()},
		{name: "opencode", info: (&opencodeharness.Runner{Binary: "/test/opencode"}).Info()},
		{name: "pi", info: (&piharness.Runner{Binary: "/test/pi"}).Info()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ok := registry.Get(tc.name)
			if !ok {
				t.Fatalf("registry missing %s", tc.name)
			}
			if tc.info.Name != cfg.Name {
				t.Fatalf("Name: got %q, want %q", tc.info.Name, cfg.Name)
			}
			if tc.info.ExactPinSupport != cfg.ExactPinSupport {
				t.Fatalf("ExactPinSupport: got %v, want %v", tc.info.ExactPinSupport, cfg.ExactPinSupport)
			}
			if tc.info.DefaultModel != cfg.DefaultModel {
				t.Fatalf("DefaultModel: got %q, want %q", tc.info.DefaultModel, cfg.DefaultModel)
			}
			if tc.info.CostClass != cfg.CostClass {
				t.Fatalf("CostClass: got %q, want %q", tc.info.CostClass, cfg.CostClass)
			}
			if tc.info.IsLocal != cfg.IsLocal {
				t.Fatalf("IsLocal: got %v, want %v", tc.info.IsLocal, cfg.IsLocal)
			}
			if tc.info.IsSubscription != cfg.IsSubscription {
				t.Fatalf("IsSubscription: got %v, want %v", tc.info.IsSubscription, cfg.IsSubscription)
			}
			assertStringSet(t, "SupportedPermissions", tc.info.SupportedPermissions, registryPermissions(cfg))
			assertStringSet(t, "SupportedReasoning", tc.info.SupportedReasoning, cfg.ReasoningLevels)
		})
	}
}

func registryPermissions(cfg harnesses.HarnessConfig) []string {
	if len(cfg.PermissionArgs) == 0 {
		return nil
	}
	perms := make([]string, 0, len(cfg.PermissionArgs))
	for perm := range cfg.PermissionArgs {
		perms = append(perms, perm)
	}
	sort.Strings(perms)
	return perms
}

func assertStringSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}
