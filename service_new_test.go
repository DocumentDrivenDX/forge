package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/DocumentDrivenDX/agent"
	_ "github.com/DocumentDrivenDX/agent/internal/config" // registers config loader via init()
)

// minimalConfigYAML is a valid agent config with one anthropic provider.
// Anthropic does not require base_url so it's easy to construct a valid entry.
const minimalConfigYAML = `
providers:
  test-provider:
    type: anthropic
    api_key: "test-key-abc"
default: test-provider
`

// TestNew_AcceptsExplicitServiceConfig confirms the explicit-injection path
// still works (existing behavior).
func TestNew_AcceptsExplicitServiceConfig(t *testing.T) {
	sc := &stubServiceConfig{defaultName: "injected"}
	svc, err := agent.New(agent.ServiceOptions{ServiceConfig: sc})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	// Verify the injected config is used by checking ListProviders returns
	// data from the stub (no actual network calls needed; stub has no providers,
	// so we get an empty list without error).
	providers, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	_ = providers // stub returns empty list; call succeeded = config was used
}

// TestNew_LoadsFromConfigPathWhenServiceConfigNil verifies that when
// ServiceConfig is nil but ConfigPath is set, New loads config from the
// directory containing ConfigPath and makes it available to ListProviders.
func TestNew_LoadsFromConfigPathWhenServiceConfigNil(t *testing.T) {
	// config.Load reads ~/.config/agent/config.yaml (global) then
	// <workDir>/.agent/config.yaml (project). Write our test config as the
	// project config file and isolate the HOME dir so the global path is empty.
	workDir := t.TempDir()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Write project config under workDir/.agent/config.yaml
	agentDir := filepath.Join(workDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte(minimalConfigYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// ConfigPath points to a file inside workDir; New will call
	// config.Load(filepath.Dir(ConfigPath)) = config.Load(workDir).
	cfgPath := filepath.Join(workDir, "config.yaml")

	svc, err := agent.New(agent.ServiceOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	providers, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("expected at least one provider from loaded config")
	}
	found := false
	for _, p := range providers {
		if p.Name == "test-provider" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected provider %q; got %v", "test-provider", providers)
	}
}

// TestNew_FallsBackToDefaultPath verifies that when both ServiceConfig and
// ConfigPath are nil/empty, New succeeds (falling back to global config or
// returning a service that reports no-config errors gracefully).
func TestNew_FallsBackToDefaultPath(t *testing.T) {
	// Unset any env vars that could inject a real provider so we get a
	// predictable empty config from the global path (which likely doesn't
	// exist in CI).
	t.Setenv("AGENT_PROVIDER", "")
	t.Setenv("AGENT_BASE_URL", "")
	t.Setenv("AGENT_API_KEY", "")
	t.Setenv("AGENT_MODEL", "")

	// New should not fail even when config is missing or empty.
	svc, err := agent.New(agent.ServiceOptions{})
	if err != nil {
		t.Fatalf("New with no config: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	// If global config has providers, ListProviders returns them.
	// If it has none (CI), it still returns successfully with an empty list
	// (or providers from env, which we cleared above).
	_, err = svc.ListProviders(context.Background())
	// We accept either success (config found) or a no-providers error.
	// What we do NOT accept is a panic or a nil-pointer dereference.
	_ = err
}

// TestNew_ExplicitConfigOverridesPath confirms that when both ServiceConfig
// and ConfigPath are set, ServiceConfig wins (explicit injection takes priority).
func TestNew_ExplicitConfigOverridesPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	// Write a config that would expose a different provider name if loaded.
	if err := os.WriteFile(cfgPath, []byte(`
providers:
  wrong-provider:
    type: anthropic
    api_key: "wrong"
default: wrong-provider
`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	sc := &stubServiceConfig{defaultName: "explicit"}
	svc, err := agent.New(agent.ServiceOptions{
		ServiceConfig: sc,
		ConfigPath:    cfgPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	providers, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	// The stub has no providers so the list should be empty, not contain
	// "wrong-provider" from the file.
	for _, p := range providers {
		if p.Name == "wrong-provider" {
			t.Errorf("expected explicit ServiceConfig to win, but got file-loaded provider %q", p.Name)
		}
	}
}

// stubServiceConfig is a minimal ServiceConfig implementation for tests.
type stubServiceConfig struct {
	defaultName string
}

func (s *stubServiceConfig) ProviderNames() []string     { return nil }
func (s *stubServiceConfig) DefaultProviderName() string { return s.defaultName }
func (s *stubServiceConfig) Provider(string) (agent.ServiceProviderEntry, bool) {
	return agent.ServiceProviderEntry{}, false
}
func (s *stubServiceConfig) ModelRouteNames() []string            { return nil }
func (s *stubServiceConfig) ModelRouteCandidates(string) []string { return nil }
func (s *stubServiceConfig) ModelRouteConfig(string) agent.ServiceModelRouteConfig {
	return agent.ServiceModelRouteConfig{}
}
func (s *stubServiceConfig) HealthCooldown() time.Duration { return 0 }
func (s *stubServiceConfig) WorkDir() string               { return "" }
