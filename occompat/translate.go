package occompat

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	agentConfig "github.com/DocumentDrivenDX/agent/config"
)

// TranslationResult contains the result of translating opencode config to agent config.
type TranslationResult struct {
	Provider agentConfig.ProviderConfig
	Warnings []string
}

// Translate converts opencode configuration to agent provider config per SD-007.
func Translate(opencodeDir, authKey string) *TranslationResult {
	result := &TranslationResult{
		Provider: agentConfig.ProviderConfig{
			Type: "openai-compat", // default
		},
	}

	// Load opencode config
	cfg, err := LoadConfig(opencodeDir)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not load opencode config: %v", err))
		return result
	}

	// Map options.baseURL → base_url
	if cfg.Options.BaseURL != "" {
		result.Provider.BaseURL = cfg.Options.BaseURL
	}

	// Map options.apiKey or auth.json key → api_key
	if cfg.Options.APIKey != "" {
		result.Provider.APIKey = cfg.Options.APIKey
	} else if authKey != "" {
		result.Provider.APIKey = authKey
	}

	// Map npm → type
	if cfg.Options.NPM != "" {
		// @ai-sdk/openai-compatible maps to openai-compat
		if cfg.Options.NPM == "@ai-sdk/openai-compatible" {
			result.Provider.Type = "openai-compat"
		}
	}

	// Map options.headers
	if len(cfg.Options.Headers) > 0 {
		result.Provider.Headers = cfg.Options.Headers
	}

	return result
}

// ComputeSourceHash computes a truncated SHA-256 hash of the source files.
func ComputeSourceHash(opencodeDir string) (string, error) {
	authPath := opencodeDir + "/auth.json"
	// Try project config first
	configPath := "opencode.json"
	home, _ := os.UserHomeDir()
	if home != "" {
		configPath = home + "/.config/opencode/opencode.json"
	}

	authData, err := os.ReadFile(authPath)
	if err != nil {
		return "", err
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}

	combined := append(authData, configData...)
	h := sha256.Sum256(combined)
	return hex.EncodeToString(h[:])[:8], nil
}
