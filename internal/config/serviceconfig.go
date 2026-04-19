package config

import (
	"time"

	agent "github.com/DocumentDrivenDX/agent"
)

func init() {
	// Register the config loader into the root package so that agent.New can
	// load configuration without importing this package directly (which would
	// create an import cycle: config → agent → config).
	agent.RegisterConfigLoader(func(dir string) (agent.ServiceConfig, error) {
		cfg, err := Load(dir)
		if err != nil {
			return nil, err
		}
		return &configServiceConfig{cfg: cfg, baseDir: dir}, nil
	})
}

// configServiceConfig wraps a loaded *Config and satisfies agent.ServiceConfig.
// It is the agent-internal equivalent of DDx's ServiceConfigAdapter.
type configServiceConfig struct {
	cfg     *Config
	baseDir string
}

func (c *configServiceConfig) ProviderNames() []string {
	if c.cfg == nil {
		return nil
	}
	return c.cfg.ProviderNames()
}

func (c *configServiceConfig) DefaultProviderName() string {
	if c.cfg == nil {
		return ""
	}
	return c.cfg.DefaultName()
}

func (c *configServiceConfig) Provider(name string) (agent.ServiceProviderEntry, bool) {
	if c.cfg == nil {
		return agent.ServiceProviderEntry{}, false
	}
	pc, ok := c.cfg.Providers[name]
	if !ok {
		return agent.ServiceProviderEntry{}, false
	}
	return agent.ServiceProviderEntry{
		Type:    pc.Type,
		BaseURL: pc.BaseURL,
		APIKey:  pc.APIKey,
		Model:   pc.Model,
	}, true
}

func (c *configServiceConfig) ModelRouteNames() []string {
	if c.cfg == nil {
		return nil
	}
	return c.cfg.ModelRouteNames()
}

func (c *configServiceConfig) ModelRouteCandidates(routeName string) []string {
	if c.cfg == nil {
		return nil
	}
	route, ok := c.cfg.ModelRoutes[routeName]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(route.Candidates))
	for _, cand := range route.Candidates {
		out = append(out, cand.Provider)
	}
	return out
}

func (c *configServiceConfig) ModelRouteConfig(routeName string) agent.ServiceModelRouteConfig {
	if c.cfg == nil {
		return agent.ServiceModelRouteConfig{}
	}
	route, ok := c.cfg.ModelRoutes[routeName]
	if !ok {
		return agent.ServiceModelRouteConfig{}
	}
	candidates := make([]agent.ServiceRouteCandidateEntry, 0, len(route.Candidates))
	for _, cand := range route.Candidates {
		candidates = append(candidates, agent.ServiceRouteCandidateEntry{
			Provider: cand.Provider,
			Model:    cand.Model,
			Priority: cand.Priority,
		})
	}
	return agent.ServiceModelRouteConfig{
		Strategy:   route.Strategy,
		Candidates: candidates,
	}
}

func (c *configServiceConfig) HealthCooldown() time.Duration {
	if c.cfg == nil || c.cfg.Routing.HealthCooldown == "" {
		return 0
	}
	d, err := time.ParseDuration(c.cfg.Routing.HealthCooldown)
	if err != nil {
		return 0
	}
	return d
}

func (c *configServiceConfig) WorkDir() string {
	return c.baseDir
}
