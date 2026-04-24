package main

import (
	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
)

func buildProviderFromResolvedConfig(name string, pc agentConfig.ProviderConfig) (agentcore.Provider, error) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{name: pc},
	}
	return cfg.BuildProvider(name)
}
