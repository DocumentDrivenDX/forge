package agent_test

import (
	"context"
	"testing"

	agent "github.com/DocumentDrivenDX/agent"
)

func TestServiceProfiles_ListResolveAliases(t *testing.T) {
	svc, err := agent.New(agent.ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	profiles, err := svc.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	byName := make(map[string]agent.ProfileInfo)
	for _, profile := range profiles {
		byName[profile.Name] = profile
	}
	if byName["smart"].AliasOf != "code-high" {
		t.Fatalf("smart AliasOf: got %q, want code-high", byName["smart"].AliasOf)
	}
	if byName["cheap"].Target != "code-economy" {
		t.Fatalf("cheap Target: got %q, want code-economy", byName["cheap"].Target)
	}
	if byName["standard"].CatalogVersion == "" {
		t.Fatal("CatalogVersion should be populated")
	}
	if byName["default"].Target != "code-medium" || byName["default"].ProviderPreference != "local-first" {
		t.Fatalf("default profile: %#v, want target code-medium/local-first", byName["default"])
	}
	if byName["local"].Target != "code-economy" || byName["local"].ProviderPreference != "local-only" {
		t.Fatalf("local profile: %#v, want target code-economy/local-only", byName["local"])
	}
	if !byName["claude-sonnet"].Deprecated {
		t.Fatal("claude-sonnet should be listed as a deprecated alias")
	}
	if byName["claude-sonnet"].Replacement != "code-medium" {
		t.Fatalf("claude-sonnet Replacement: got %q, want code-medium", byName["claude-sonnet"].Replacement)
	}

	aliases, err := svc.ProfileAliases(context.Background())
	if err != nil {
		t.Fatalf("ProfileAliases: %v", err)
	}
	if aliases["smart"] != "code-high" {
		t.Fatalf("smart alias: got %q, want code-high", aliases["smart"])
	}
	if aliases["claude-sonnet"] != "code-medium" {
		t.Fatalf("deprecated claude-sonnet alias: got %q, want code-medium", aliases["claude-sonnet"])
	}
}

func TestServiceProfiles_ResolveProfile(t *testing.T) {
	svc, err := agent.New(agent.ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resolved, err := svc.ResolveProfile(context.Background(), "smart")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if resolved.Target != "code-high" {
		t.Fatalf("Target: got %q, want code-high", resolved.Target)
	}
	if len(resolved.Surfaces) == 0 {
		t.Fatal("expected profile surfaces")
	}
	nativeOpenAI := findProfileSurface(resolved.Surfaces, "native-openai")
	if nativeOpenAI == nil {
		t.Fatalf("native-openai surface missing from %#v", resolved.Surfaces)
	}
	if nativeOpenAI.Harness != "agent" {
		t.Fatalf("native-openai Harness: got %q, want agent", nativeOpenAI.Harness)
	}
	if nativeOpenAI.Model == "" || len(nativeOpenAI.Candidates) == 0 {
		t.Fatalf("native-openai model candidates missing: %#v", nativeOpenAI)
	}
	if nativeOpenAI.ReasoningDefault != agent.ReasoningHigh {
		t.Fatalf("ReasoningDefault: got %q, want high", nativeOpenAI.ReasoningDefault)
	}
	if nativeOpenAI.FailurePolicy != "ordered-failover" {
		t.Fatalf("FailurePolicy: got %q, want ordered-failover", nativeOpenAI.FailurePolicy)
	}
	if nativeOpenAI.CostCeilingInputPerMTok == nil || *nativeOpenAI.CostCeilingInputPerMTok != 20 {
		t.Fatalf("CostCeilingInputPerMTok: got %#v, want 20", nativeOpenAI.CostCeilingInputPerMTok)
	}

	gemini := findProfileSurface(resolved.Surfaces, "gemini")
	if gemini == nil {
		t.Fatalf("gemini surface missing from %#v", resolved.Surfaces)
	}
	if gemini.Harness != "gemini" || gemini.Model != "gemini-2.5-pro" {
		t.Fatalf("gemini smart surface: %#v", gemini)
	}
	if gemini.ReasoningDefault != agent.ReasoningOff {
		t.Fatalf("gemini ReasoningDefault: got %q, want off", gemini.ReasoningDefault)
	}
	if len(gemini.Candidates) == 0 || gemini.Candidates[0] != "gemini-2.5-pro" {
		t.Fatalf("gemini candidates: %#v", gemini.Candidates)
	}
}

func TestServiceProfiles_ResolveDeprecatedAliasAndUnknown(t *testing.T) {
	svc, err := agent.New(agent.ServiceOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	deprecated, err := svc.ResolveProfile(context.Background(), "claude-sonnet")
	if err != nil {
		t.Fatalf("ResolveProfile deprecated alias: %v", err)
	}
	if !deprecated.Deprecated {
		t.Fatal("deprecated alias should resolve with Deprecated=true")
	}
	if deprecated.Replacement != "code-medium" {
		t.Fatalf("Replacement: got %q, want code-medium", deprecated.Replacement)
	}

	if _, err := svc.ResolveProfile(context.Background(), "does-not-exist"); err == nil {
		t.Fatal("ResolveProfile unknown should return an error")
	}
}

func findProfileSurface(surfaces []agent.ProfileSurface, name string) *agent.ProfileSurface {
	for i := range surfaces {
		if surfaces[i].Name == name {
			return &surfaces[i]
		}
	}
	return nil
}
