package agent

import (
	"fmt"
	"strings"

	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/provider/anthropic"
	"github.com/DocumentDrivenDX/agent/internal/provider/lmstudio"
	"github.com/DocumentDrivenDX/agent/internal/provider/lucebox"
	"github.com/DocumentDrivenDX/agent/internal/provider/ollama"
	"github.com/DocumentDrivenDX/agent/internal/provider/omlx"
	oaiProvider "github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/provider/openrouter"
	"github.com/DocumentDrivenDX/agent/internal/provider/vllm"
)

type nativeProviderResolution struct {
	Provider agentcore.Provider
	Name     string
	Entry    ServiceProviderEntry
}

func (s *service) resolveConfiguredNativeProvider(req ServiceExecuteRequest) nativeProviderResolution {
	sc := s.opts.ServiceConfig
	if sc == nil {
		return nativeProviderResolution{}
	}
	name, entry, ok := selectConfiguredNativeProvider(sc, req)
	if !ok {
		return nativeProviderResolution{}
	}
	if req.Model != "" {
		entry.Model = req.Model
	}
	provider := buildNativeProvider(name, entry)
	if provider == nil {
		return nativeProviderResolution{Name: name, Entry: entry}
	}
	return nativeProviderResolution{Provider: provider, Name: name, Entry: entry}
}

func selectConfiguredNativeProvider(sc ServiceConfig, req ServiceExecuteRequest) (string, ServiceProviderEntry, bool) {
	if req.Provider != "" {
		if entry, ok := sc.Provider(req.Provider); ok {
			return req.Provider, entry, true
		}
		if name, entry, ok := selectConfiguredEndpointProvider(sc, req.Provider); ok {
			return name, entry, true
		}
	}

	wantedType := requestedNativeProviderType(req)
	if wantedType != "" {
		if name := sc.DefaultProviderName(); name != "" {
			if entry, ok := sc.Provider(name); ok && normalizeServiceProviderType(entry.Type) == wantedType {
				return name, entry, true
			}
		}
		for _, name := range sc.ProviderNames() {
			entry, ok := sc.Provider(name)
			if ok && normalizeServiceProviderType(entry.Type) == wantedType {
				return name, entry, true
			}
		}
	}

	if req.Provider == "" && wantedType == "" {
		name := sc.DefaultProviderName()
		if name == "" {
			return "", ServiceProviderEntry{}, false
		}
		entry, ok := sc.Provider(name)
		return name, entry, ok
	}

	return "", ServiceProviderEntry{}, false
}

func selectConfiguredEndpointProvider(sc ServiceConfig, ref string) (string, ServiceProviderEntry, bool) {
	providerName, endpointName, ok := splitEndpointProviderRef(ref)
	if !ok {
		return "", ServiceProviderEntry{}, false
	}
	entry, ok := sc.Provider(providerName)
	if !ok {
		return "", ServiceProviderEntry{}, false
	}
	for _, endpoint := range modelDiscoveryEndpoints(entry) {
		if endpoint.Name != endpointName {
			continue
		}
		entry.BaseURL = endpoint.BaseURL
		entry.Endpoints = []ServiceProviderEndpoint{{Name: endpoint.Name, BaseURL: endpoint.BaseURL}}
		return ref, entry, true
	}
	return "", ServiceProviderEntry{}, false
}

func endpointProviderRef(providerName, endpointName string) string {
	if endpointName == "" {
		return providerName
	}
	return providerName + "@" + endpointName
}

func splitEndpointProviderRef(ref string) (string, string, bool) {
	providerName, endpointName, ok := strings.Cut(ref, "@")
	if !ok || providerName == "" || endpointName == "" {
		return "", "", false
	}
	return providerName, endpointName, true
}

func requestedNativeProviderType(req ServiceExecuteRequest) string {
	if req.Provider != "" {
		return normalizeServiceProviderType(req.Provider)
	}
	switch req.Harness {
	case "", "agent":
		return ""
	default:
		return normalizeServiceProviderType(req.Harness)
	}
}

func (s *service) nativeProviderNotConfiguredError(req ServiceExecuteRequest, decision RouteDecision) string {
	wantedType := requestedNativeProviderType(req)
	if wantedType == "" {
		errMsg := "orphan model: " + decision.Model
		if decision.Model == "" {
			errMsg = "no provider configured for native harness"
		}
		return errMsg
	}
	available := s.availableProviderTypes()
	harness := decision.Harness
	if harness == "" {
		harness = "agent"
	}
	return fmt.Sprintf("harness %q: no configured provider matches type %q (available: %s)", harness, wantedType, available)
}

func (s *service) availableProviderTypes() string {
	sc := s.opts.ServiceConfig
	if sc == nil {
		return "[]"
	}
	var parts []string
	for _, name := range sc.ProviderNames() {
		entry, ok := sc.Provider(name)
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", name, normalizeServiceProviderType(entry.Type)))
	}
	if len(parts) == 0 {
		return "[]"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func buildNativeProvider(name string, entry ServiceProviderEntry) agentcore.Provider {
	modelWire := nativeModelReasoningWireMap()
	switch normalizeServiceProviderType(entry.Type) {
	case "anthropic":
		return anthropic.New(anthropic.Config{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey,
			Model:   entry.Model,
		})
	case "lmstudio":
		return lmstudio.New(lmstudio.Config{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey,
			Model:   entry.Model,
		})
	case "openrouter":
		return openrouter.New(openrouter.Config{
			BaseURL:            entry.BaseURL,
			APIKey:             entry.APIKey,
			Model:              entry.Model,
			ModelReasoningWire: modelWire,
		})
	case "omlx":
		return omlx.New(omlx.Config{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey,
			Model:   entry.Model,
		})
	case "lucebox":
		return lucebox.New(lucebox.Config{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey,
			Model:   entry.Model,
		})
	case "vllm":
		return vllm.New(vllm.Config{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey,
			Model:   entry.Model,
		})
	case "ollama":
		return ollama.New(ollama.Config{
			BaseURL: entry.BaseURL,
			APIKey:  entry.APIKey,
			Model:   entry.Model,
		})
	case "openai", "minimax", "qwen", "zai":
		return oaiProvider.New(oaiProvider.Config{
			BaseURL:            entry.BaseURL,
			APIKey:             entry.APIKey,
			Model:              entry.Model,
			ProviderName:       name,
			ProviderSystem:     normalizeServiceProviderType(entry.Type),
			ModelReasoningWire: modelWire,
		})
	default:
		return nil
	}
}

// nativeModelReasoningWireMap returns the catalog reasoning_wire map for use
// by the native (service-side) provider builder. Models without an explicit
// reasoning_wire are omitted; the provider treats absence as the "provider"
// default.
func nativeModelReasoningWireMap() map[string]string {
	cat, err := modelcatalog.Default()
	if err != nil {
		return nil
	}
	all := cat.AllModels()
	if len(all) == 0 {
		return nil
	}
	out := make(map[string]string, len(all))
	for id, entry := range all {
		if entry.ReasoningWire != "" {
			out[id] = entry.ReasoningWire
		}
	}
	return out
}
