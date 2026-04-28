// Package registry is the single source of truth for provider-type →
// factory mappings.
//
// Why this package exists. Before it, the agent had two parallel
// factories that knew about provider types: internal/config/config.go
// `BuildProvider` (config-time) and service_native_provider.go
// `buildNativeProvider` (service-execute time). When lucebox + vllm
// were added in v0.9.18-.20, only the first factory got the new types;
// the second one rejected requests pinning those providers at execute
// time. Two factories drifting is the architectural smell `agent-8e4eb44c`
// was filed to fix.
//
// Each provider package registers a Descriptor in its init(). Both
// factories call Lookup. New providers are one Register call away from
// being usable through every code path.
//
// The registry deliberately covers only the factory drift case in v1.
// Other type-switch sites (default port, base URL, surface map,
// capability flags, validators) are local to internal/config and
// don't have the cross-package drift problem; they stay there. Future
// drift cases can be additively folded into the registry as they appear.
package registry

import (
	"sort"
	"sync"

	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
)

// Inputs is the canonical factory input shape. Both the config-time
// path (internal/config builds this from a ProviderConfig) and the
// service-time path (service_native_provider builds this from a
// ServiceProviderEntry) populate it. Provider factories take exactly
// one argument so the call sites can't accidentally diverge.
type Inputs struct {
	// ProviderName is the user-facing config name for this provider
	// (e.g., "vidar-omlx"). Distinct from the provider TYPE (e.g.,
	// "omlx"). Some factories include it in attribution metadata.
	ProviderName string

	// BaseURL is the resolved endpoint (with default substituted).
	BaseURL string
	// APIKey is the bearer token; empty for unauthenticated endpoints.
	APIKey string
	// Model is the configured default; empty when discovery is in use.
	Model string

	// ModelPattern is an optional regex used by openai-compat providers
	// during model discovery (Tier-2 ranking).
	ModelPattern string

	// KnownModels maps surface model IDs to canonical IDs from the
	// model catalog. Used by openai-compat providers to recognize
	// catalog-tracked models on a discovery list.
	KnownModels map[string]string

	// Headers is extra HTTP headers (OpenRouter, Azure, custom).
	Headers map[string]string

	// Reasoning is the per-provider default reasoning policy.
	Reasoning reasoning.Reasoning

	// ModelReasoningWire is a model_id → reasoning_wire map sourced
	// from the catalog. Today only openrouter and openai-compat
	// providers consume it; others ignore it.
	ModelReasoningWire map[string]string
}

// Factory builds a provider from canonical inputs.
type Factory func(Inputs) agentcore.Provider

// Descriptor pairs a provider type with its factory + a small set of
// invariants. Provider packages register these in init().
type Descriptor struct {
	// Type is the canonical provider-type string (e.g., "omlx").
	// Matches the `type:` value in user config and the descriptor's
	// case in internal/config switches.
	Type string

	// Factory builds an agent.Provider from canonical inputs.
	Factory Factory

	// DefaultBaseURL is used by the config-normalizer when no base_url
	// is explicitly set.
	DefaultBaseURL string

	// DefaultPort is used by endpoint-config inference when only a
	// host is supplied. 0 means "no inference; require explicit BaseURL".
	DefaultPort int
}

var (
	mu      sync.RWMutex
	entries = map[string]Descriptor{}
)

// Register installs a provider Descriptor. Called from each provider
// package's init() function. Re-registering the same type panics —
// duplicate registration is always a programmer error.
func Register(d Descriptor) {
	if d.Type == "" {
		panic("provider/registry: Descriptor.Type is required")
	}
	if d.Factory == nil {
		panic("provider/registry: Descriptor.Factory is required for type " + d.Type)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := entries[d.Type]; exists {
		panic("provider/registry: duplicate registration for type " + d.Type)
	}
	entries[d.Type] = d
}

// Lookup returns the Descriptor for a provider type, or false if the
// type is not registered.
func Lookup(typ string) (Descriptor, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := entries[typ]
	return d, ok
}

// Types returns all registered provider type names in sorted order.
// Used by validators and conformance descriptors that iterate over
// every known provider type.
func Types() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(entries))
	for t := range entries {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
