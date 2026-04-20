package main

import (
	"time"

	agent "github.com/DocumentDrivenDX/agent"
	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
)

// configAdapter wraps *config.Config and satisfies agent.ServiceConfig.
type configAdapter struct {
	cfg     *agentConfig.Config
	workDir string
}

var _ agent.ServiceConfig = (*configAdapter)(nil)

func (a *configAdapter) ProviderNames() []string { return a.cfg.ProviderNames() }

func (a *configAdapter) DefaultProviderName() string { return a.cfg.DefaultName() }

func (a *configAdapter) Provider(name string) (agent.ServiceProviderEntry, bool) {
	pc, ok := a.cfg.GetProvider(name)
	if !ok {
		return agent.ServiceProviderEntry{}, false
	}
	endpoints := make([]agent.ServiceProviderEndpoint, 0, len(pc.Endpoints))
	for _, endpoint := range pc.Endpoints {
		endpoints = append(endpoints, agent.ServiceProviderEndpoint{
			Name:    endpoint.Name,
			BaseURL: endpoint.BaseURL,
		})
	}
	return agent.ServiceProviderEntry{
		Type:      pc.Type,
		BaseURL:   pc.BaseURL,
		Endpoints: endpoints,
		APIKey:    pc.APIKey,
		Model:     pc.Model,
	}, true
}

func (a *configAdapter) ModelRouteNames() []string { return a.cfg.ModelRouteNames() }

func (a *configAdapter) ModelRouteCandidates(routeName string) []string {
	rc, ok := a.cfg.GetModelRoute(routeName)
	if !ok {
		return nil
	}
	providers := make([]string, 0, len(rc.Candidates))
	for _, c := range rc.Candidates {
		providers = append(providers, c.Provider)
	}
	return providers
}

func (a *configAdapter) ModelRouteConfig(routeName string) agent.ServiceModelRouteConfig {
	rc, ok := a.cfg.GetModelRoute(routeName)
	if !ok {
		return agent.ServiceModelRouteConfig{}
	}
	entries := make([]agent.ServiceRouteCandidateEntry, 0, len(rc.Candidates))
	for _, c := range rc.Candidates {
		entries = append(entries, agent.ServiceRouteCandidateEntry{
			Provider: c.Provider,
			Model:    c.Model,
			Priority: c.Priority,
		})
	}
	return agent.ServiceModelRouteConfig{
		Strategy:   rc.Strategy,
		Candidates: entries,
	}
}

func (a *configAdapter) HealthCooldown() time.Duration { return 0 }

func (a *configAdapter) WorkDir() string { return a.workDir }
