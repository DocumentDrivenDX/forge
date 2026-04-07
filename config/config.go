// Package config provides multi-provider configuration loading for forge.
// Supports named providers, environment variable expansion, and backwards
// compatibility with the old flat config format.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DocumentDrivenDX/forge"
	"github.com/DocumentDrivenDX/forge/provider/anthropic"
	oaiProvider "github.com/DocumentDrivenDX/forge/provider/openai"
	"gopkg.in/yaml.v3"
)

// ProviderConfig describes a single named provider.
type ProviderConfig struct {
	Type    string            `yaml:"type"`     // "openai-compat" or "anthropic"
	BaseURL string            `yaml:"base_url"` // e.g., "http://localhost:1234/v1"
	APIKey  string            `yaml:"api_key"`
	Model   string            `yaml:"model"`
	Headers map[string]string `yaml:"headers"` // extra HTTP headers (OpenRouter, Azure)
}

// ImportMetadata records the last import source for drift detection.
type ImportMetadata struct {
	// Source is the import source ("pi" or "opencode").
	Source string `yaml:"source"`
	// Timestamp is when the import occurred (RFC3339).
	Timestamp string `yaml:"timestamp"`
	// SourceHash is the truncated SHA-256 of the source files.
	SourceHash string `yaml:"source_hash"`
}

// Config is the top-level forge configuration.
type Config struct {
	// Providers is a map of named provider configurations.
	Providers map[string]ProviderConfig `yaml:"providers"`

	// Default is the name of the default provider. If empty, uses the first.
	Default string `yaml:"default"`

	// MaxIterations limits agent loop iterations.
	MaxIterations int `yaml:"max_iterations"`

	// SessionLogDir is where session logs are written.
	SessionLogDir string `yaml:"session_log_dir"`

	// Preset is the system prompt preset name.
	Preset string `yaml:"preset"`

	// ImportedFrom records the last import source for drift detection.
	ImportedFrom *ImportMetadata `yaml:"imported_from,omitempty"`

	// Legacy flat fields for backwards compatibility.
	LegacyProvider string `yaml:"provider"`
	LegacyBaseURL  string `yaml:"base_url"`
	LegacyAPIKey   string `yaml:"api_key"`
	LegacyModel    string `yaml:"model"`
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	return Config{
		MaxIterations: 20,
		SessionLogDir: ".forge/sessions",
		Preset:        "forge",
	}
}

// Load reads configuration from .forge/config.yaml (project) and
// ~/.config/forge/config.yaml (global), with env var expansion.
// Project config overrides global config. If no config files exist,
// returns defaults.
func Load(workDir string) (*Config, error) {
	cfg := Defaults()

	// Try global config first, then project config (project wins)
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "forge", "config.yaml"))
	}
	paths = append(paths, filepath.Join(workDir, ".forge", "config.yaml"))

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		expanded := expandEnvVars(string(data))
		if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
			return nil, fmt.Errorf("config: parsing %s: %w", p, err)
		}
	}

	// Migrate legacy flat format
	cfg.migrateLegacy()

	// Apply env var overrides to default provider
	cfg.applyEnvOverrides()

	return &cfg, nil
}

// migrateLegacy converts old flat fields to a named provider.
func (c *Config) migrateLegacy() {
	if c.Providers != nil && len(c.Providers) > 0 {
		return // new format takes precedence
	}
	if c.LegacyProvider == "" && c.LegacyBaseURL == "" {
		return // nothing to migrate
	}

	provType := c.LegacyProvider
	if provType == "" {
		provType = "openai-compat"
	}
	baseURL := c.LegacyBaseURL
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}

	c.Providers = map[string]ProviderConfig{
		"default": {
			Type:    provType,
			BaseURL: baseURL,
			APIKey:  c.LegacyAPIKey,
			Model:   c.LegacyModel,
		},
	}
	if c.Default == "" {
		c.Default = "default"
	}
}

// applyEnvOverrides applies FORGE_* env vars to the default provider.
func (c *Config) applyEnvOverrides() {
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}

	// Get or create default provider
	defName := c.DefaultName()
	p := c.Providers[defName]

	if v := os.Getenv("FORGE_PROVIDER"); v != "" {
		p.Type = v
	}
	if v := os.Getenv("FORGE_BASE_URL"); v != "" {
		p.BaseURL = v
	}
	if v := os.Getenv("FORGE_API_KEY"); v != "" {
		p.APIKey = v
	}
	if v := os.Getenv("FORGE_MODEL"); v != "" {
		p.Model = v
	}

	// Only write back if something was set
	if p.Type != "" || p.BaseURL != "" || p.APIKey != "" || p.Model != "" {
		if p.Type == "" {
			p.Type = "openai-compat"
		}
		if p.BaseURL == "" && p.Type == "openai-compat" {
			p.BaseURL = "http://localhost:1234/v1"
		}
		c.Providers[defName] = p
		if c.Default == "" {
			c.Default = defName
		}
	}
}

// DefaultName returns the name of the default provider.
func (c *Config) DefaultName() string {
	if c.Default != "" {
		return c.Default
	}
	// Return first provider name
	for name := range c.Providers {
		return name
	}
	return "default"
}

// ProviderNames returns configured provider names in stable order.
func (c *Config) ProviderNames() []string {
	if c.Providers == nil {
		return nil
	}
	// Put default first, then alphabetical
	var names []string
	defName := c.DefaultName()
	if _, ok := c.Providers[defName]; ok {
		names = append(names, defName)
	}
	sorted := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		if name != defName {
			sorted = append(sorted, name)
		}
	}
	// Simple sort
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	names = append(names, sorted...)
	return names
}

// BuildProvider creates a forge.Provider from a named provider config.
func (c *Config) BuildProvider(name string) (forge.Provider, error) {
	pc, ok := c.Providers[name]
	if !ok {
		return nil, fmt.Errorf("config: unknown provider %q", name)
	}
	return buildProviderFromConfig(pc)
}

// DefaultProvider creates the default provider.
func (c *Config) DefaultProvider() (forge.Provider, error) {
	return c.BuildProvider(c.DefaultName())
}

// GetProvider returns the ProviderConfig for a named provider.
func (c *Config) GetProvider(name string) (ProviderConfig, bool) {
	pc, ok := c.Providers[name]
	return pc, ok
}

func buildProviderFromConfig(pc ProviderConfig) (forge.Provider, error) {
	switch pc.Type {
	case "openai-compat", "openai":
		return oaiProvider.New(oaiProvider.Config{
			BaseURL: pc.BaseURL,
			APIKey:  pc.APIKey,
			Model:   pc.Model,
			Headers: pc.Headers,
		}), nil
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKey: pc.APIKey,
			Model:  pc.Model,
		}), nil
	default:
		return nil, fmt.Errorf("config: unknown provider type %q (use openai-compat or anthropic)", pc.Type)
	}
}

// envVarPattern matches ${VAR_NAME} patterns.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars replaces ${VAR} patterns with environment variable values.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match // leave unexpanded if not set
	})
}

// Save serializes the config to YAML bytes.
func (c *Config) Save() ([]byte, error) {
	return yaml.Marshal(c)
}

// Save is a package-level alias for Config.Save.
func Save(cfg *Config) ([]byte, error) {
	return cfg.Save()
}

// SaveToFile writes the config to a YAML file.
func SaveToFile(path string, cfg *Config) error {
	data, err := cfg.Save()
	if err != nil {
		return fmt.Errorf("config: marshaling: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("config: writing %s: %w", path, err)
	}
	return nil
}
