package main

import (
	"testing"

	agentConfig "github.com/DocumentDrivenDX/agent/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveProviderForRun_DefaultProvider(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"local": {
				Type:    "openai-compat",
				BaseURL: "http://localhost:1234/v1",
				Model:   "configured-model",
			},
		},
		Default: "local",
	}

	name, p, pc, err := resolveProviderForRun(cfg, "", agentConfig.ProviderOverrides{})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "local", name)
	assert.Equal(t, "configured-model", pc.Model)
}

func TestResolveProviderForRun_ModelRef(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
		Default: "cloud",
	}

	name, p, pc, err := resolveProviderForRun(cfg, "", agentConfig.ProviderOverrides{
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "cloud", name)
	assert.Equal(t, "claude-sonnet-4-20250514", pc.Model)
}

func TestResolveProviderForRun_DeprecatedModelRefRejectedByDefault(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
		Default: "cloud",
	}

	_, _, _, err := resolveProviderForRun(cfg, "", agentConfig.ProviderOverrides{
		ModelRef: "claude-sonnet-3.7",
	})
	require.Error(t, err)
}

func TestResolveProviderForRun_DeprecatedModelRefAllowed(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
			},
		},
		Default: "cloud",
	}

	_, p, pc, err := resolveProviderForRun(cfg, "", agentConfig.ProviderOverrides{
		ModelRef:        "claude-sonnet-3.7",
		AllowDeprecated: true,
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "claude-3-7-sonnet-20250219", pc.Model)
}

func TestResolveProviderForRun_ExplicitModelWins(t *testing.T) {
	cfg := &agentConfig.Config{
		Providers: map[string]agentConfig.ProviderConfig{
			"cloud": {
				Type:   "anthropic",
				APIKey: "test",
				Model:  "configured-model",
			},
		},
		Default: "cloud",
	}

	_, p, pc, err := resolveProviderForRun(cfg, "", agentConfig.ProviderOverrides{
		Model:    "exact-model",
		ModelRef: "code-smart",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	assert.Equal(t, "exact-model", pc.Model)
}
