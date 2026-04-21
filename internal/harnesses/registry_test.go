package harnesses

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryBuiltinHarnesses(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"codex", "claude", "gemini", "opencode", "agent", "pi"} {
		assert.True(t, r.Has(name), "should have builtin harness: %s", name)
	}
	assert.False(t, r.Has("nonexistent"))
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	h, ok := r.Get("codex")
	require.True(t, ok)
	assert.Equal(t, "codex", h.Name)
	assert.Equal(t, "codex", h.Binary)
	assert.Equal(t, "arg", h.PromptMode)
	assert.Equal(t, "-m", h.ModelFlag)
	assert.Equal(t, "-C", h.WorkDirFlag)
}

func TestRegistryDefaultBaseArgs(t *testing.T) {
	r := NewRegistry()

	codex, ok := r.Get("codex")
	require.True(t, ok)
	assert.Equal(t, []string{"exec", "--json"}, codex.BaseArgs)

	claude, ok := r.Get("claude")
	require.True(t, ok)
	assert.Equal(t, []string{"--print", "-p", "--verbose", "--output-format", "stream-json"}, claude.BaseArgs)
}

func TestRegistryNamesPreferenceOrder(t *testing.T) {
	r := NewRegistry()
	names := r.Names()
	require.Len(t, names, 11)
	assert.Equal(t, "codex", names[0])
	assert.Equal(t, "claude", names[1])
	assert.Equal(t, "opencode", names[2])
	assert.Equal(t, "gemini", names[8])
	assert.Contains(t, names, "virtual")
}

func TestRegistryDiscoverEmbeddedAlwaysAvailable(t *testing.T) {
	r := NewRegistry()
	r.LookPath = func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}
	statuses := r.Discover()
	byName := make(map[string]HarnessStatus)
	for _, s := range statuses {
		byName[s.Name] = s
	}
	assert.True(t, byName["agent"].Available, "embedded agent should always be available")
	assert.True(t, byName["virtual"].Available, "virtual harness should always be available")
	assert.True(t, byName["script"].Available, "script harness should always be available")
}

func TestRegistryDiscoverHTTPProviders(t *testing.T) {
	r := NewRegistry()
	r.LookPath = func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}
	statuses := r.Discover()
	byName := make(map[string]HarnessStatus)
	for _, s := range statuses {
		byName[s.Name] = s
	}
	assert.True(t, byName["openrouter"].Available, "http provider openrouter should be available")
	assert.True(t, byName["lmstudio"].Available, "http provider lmstudio should be available")
	assert.True(t, byName["omlx"].Available, "http provider omlx should be available")
}

func TestRegistryFirstAvailable(t *testing.T) {
	r := NewRegistry()
	// With default LookPath, embedded agent is always available.
	name, ok := r.FirstAvailable()
	assert.True(t, ok)
	assert.NotEmpty(t, name)
}

func TestRegistryFirstAvailableEmbeddedFallback(t *testing.T) {
	r := NewRegistry()
	r.LookPath = func(file string) (string, error) {
		return "", fmt.Errorf("not found")
	}
	// HTTP providers and embedded harnesses are always available,
	// so FirstAvailable should return the first in preference order.
	name, ok := r.FirstAvailable()
	assert.True(t, ok)
	// openrouter or lmstudio or agent/virtual/script depending on preference order
	assert.NotEmpty(t, name)
}

func TestResolveHarnessAlias(t *testing.T) {
	assert.Equal(t, "agent", ResolveHarnessAlias("local"))
	assert.Equal(t, "claude", ResolveHarnessAlias("claude"))
	assert.Equal(t, "unknown", ResolveHarnessAlias("unknown"))
}

func TestBuiltinHarnessesPermissionArgs(t *testing.T) {
	r := NewRegistry()

	codex, ok := r.Get("codex")
	require.True(t, ok)
	assert.Equal(t, []string{"--dangerously-bypass-approvals-and-sandbox"}, codex.PermissionArgs["unrestricted"])

	claude, ok := r.Get("claude")
	require.True(t, ok)
	assert.Contains(t, claude.PermissionArgs["unrestricted"], "--dangerously-skip-permissions")
}

func TestBuiltinHarnessesMetadata(t *testing.T) {
	r := NewRegistry()

	codex, _ := r.Get("codex")
	assert.True(t, codex.IsSubscription)
	assert.False(t, codex.IsLocal)
	assert.True(t, codex.AutoRoutingEligible)
	assert.True(t, codex.ExactPinSupport)

	agent, _ := r.Get("agent")
	assert.True(t, agent.IsLocal)
	assert.Equal(t, "local", agent.CostClass)
	assert.True(t, agent.AutoRoutingEligible)

	claude, _ := r.Get("claude")
	assert.True(t, claude.AutoRoutingEligible)

	gemini, _ := r.Get("gemini")
	assert.False(t, gemini.AutoRoutingEligible)

	opencode, _ := r.Get("opencode")
	assert.True(t, opencode.AutoRoutingEligible)
	assert.Equal(t, "opencode/gpt-5.4", opencode.DefaultModel)
	assert.Contains(t, opencode.Models, "opencode/gpt-5.4")

	pi, _ := r.Get("pi")
	assert.True(t, pi.AutoRoutingEligible)
	assert.Equal(t, "gemini-2.5-flash", pi.DefaultModel)
	assert.Contains(t, pi.Models, "gemini-2.5-pro")

	virtual, _ := r.Get("virtual")
	assert.True(t, virtual.TestOnly)
	assert.False(t, virtual.AutoRoutingEligible)

	script, _ := r.Get("script")
	assert.True(t, script.TestOnly)
	assert.False(t, script.AutoRoutingEligible)

	openrouter, _ := r.Get("openrouter")
	assert.True(t, openrouter.IsHTTPProvider)
	assert.False(t, openrouter.AutoRoutingEligible)

	lmstudio, _ := r.Get("lmstudio")
	assert.True(t, lmstudio.IsHTTPProvider)
	assert.True(t, lmstudio.IsLocal)
	assert.False(t, lmstudio.AutoRoutingEligible)

	omlx, _ := r.Get("omlx")
	assert.True(t, omlx.IsHTTPProvider)
	assert.True(t, omlx.IsLocal)
	assert.Equal(t, "local", omlx.CostClass)
	assert.False(t, omlx.AutoRoutingEligible)
}
