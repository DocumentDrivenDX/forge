package registry_test

import (
	"testing"

	// Blank-import every provider package so init()-side registrations
	// fire under the test binary. This mirrors the production import
	// graph (cmd/agent transitively imports all providers via service)
	// without depending on those packages directly here.
	_ "github.com/DocumentDrivenDX/agent/internal/provider/anthropic"
	_ "github.com/DocumentDrivenDX/agent/internal/provider/lmstudio"
	_ "github.com/DocumentDrivenDX/agent/internal/provider/lucebox"
	_ "github.com/DocumentDrivenDX/agent/internal/provider/ollama"
	_ "github.com/DocumentDrivenDX/agent/internal/provider/omlx"
	_ "github.com/DocumentDrivenDX/agent/internal/provider/openai"
	_ "github.com/DocumentDrivenDX/agent/internal/provider/openrouter"
	"github.com/DocumentDrivenDX/agent/internal/provider/registry"
	_ "github.com/DocumentDrivenDX/agent/internal/provider/vllm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectedTypes is the canonical list of provider types the agent
// recognizes. Adding a new provider type means adding an entry here AND
// registering in the new package. Removing one means deleting both.
//
// This is the single round-trip invariant that catches the drift class
// agent-8e4eb44c was filed to fix: every type in this list MUST have a
// registered Descriptor whose Factory returns a non-nil provider given
// a minimal-but-valid Inputs.
var expectedTypes = []string{
	"anthropic",
	"lmstudio",
	"lucebox",
	"minimax",
	"ollama",
	"omlx",
	"openai",
	"openrouter",
	"qwen",
	"vllm",
	"zai",
}

func TestRegistry_AllExpectedTypesRegistered(t *testing.T) {
	registered := registry.Types()
	assert.ElementsMatch(t, expectedTypes, registered,
		"every expected provider type must be registered; "+
			"if you added a new provider type, also add it to expectedTypes; "+
			"if you removed one, also remove it here")
}

func TestRegistry_FactoriesProduceNonNilProviders(t *testing.T) {
	for _, typ := range expectedTypes {
		typ := typ
		t.Run(typ, func(t *testing.T) {
			d, ok := registry.Lookup(typ)
			require.True(t, ok, "type %q must resolve via registry.Lookup", typ)
			require.NotNil(t, d.Factory, "Descriptor.Factory must not be nil for %q", typ)

			// Minimal Inputs sufficient for every current factory.
			// Some factories construct an HTTP client lazily, so the
			// non-nil assertion below works without a real server.
			p := d.Factory(registry.Inputs{
				ProviderName: "test-" + typ,
				BaseURL:      "http://localhost:0/v1",
				APIKey:       "test-key",
				Model:        "test-model",
			})
			assert.NotNil(t, p, "Factory must return a non-nil provider for %q", typ)
		})
	}
}

func TestRegistry_LookupUnknownTypeReturnsFalse(t *testing.T) {
	_, ok := registry.Lookup("not-a-real-provider-type")
	assert.False(t, ok, "Lookup of unknown type must return false")
}

func TestRegistry_TypesIsSorted(t *testing.T) {
	got := registry.Types()
	for i := 1; i < len(got); i++ {
		assert.True(t, got[i-1] < got[i], "Types() must return sorted order; got %v", got)
	}
}
