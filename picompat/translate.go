package picompat

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	agentConfig "github.com/DocumentDrivenDX/agent/config"
)

// ProviderMapping maps pi provider names to agent configurations.
type ProviderMapping struct {
	AgentName string // name in agent config
	Type      string // agent type: "anthropic" or "openai-compat"
	BaseURL   string // default URL if not specified in pi
}

// Known mappings per SD-007
var knownMappings = map[string]ProviderMapping{
	"anthropic":    {AgentName: "anthropic", Type: "anthropic"},
	"openai-codex": {AgentName: "openai", Type: "openai-compat", BaseURL: "https://api.openai.com/v1"},
	"openrouter":   {AgentName: "openrouter", Type: "openai-compat", BaseURL: "https://openrouter.ai/api/v1"},
}

// Warnings collects import warnings.
type Warnings []string

// Add appends a warning message.
func (w *Warnings) Add(format string, args ...interface{}) {
	*w = append(*w, fmt.Sprintf(format, args...))
}

// TranslationResult contains the result of translating pi config to agent config.
type TranslationResult struct {
	Providers map[string]agentConfig.ProviderConfig
	Default   string
	Warnings  Warnings
}

// Translate merges pi auth, models, and settings into agent provider configs.
// It implements the two-source merge algorithm from SD-007.
func Translate(piDir string) (*TranslationResult, error) {
	result := &TranslationResult{
		Providers: make(map[string]agentConfig.ProviderConfig),
	}

	// Load all three sources
	auth, err := LoadAuth(piDir)
	if err != nil {
		return nil, fmt.Errorf("reading pi auth.json: %w", err)
	}

	models, err := LoadModels(piDir)
	if err != nil {
		return nil, fmt.Errorf("reading pi models.json: %w", err)
	}

	var settings *Settings
	settings, _ = LoadSettings(piDir) // settings is optional

	// Step 1: Start with models.json providers (have baseUrl and model IDs)
	for _, provider := range models.Providers {
		pc := translateProvider(provider, auth[provider.Name])
		result.Providers[pc.Name] = pc.Config
	}

	// Step 2 & 3: For auth.json entries with NO matching models.json provider,
	// create agent providers using well-known defaults
	for name, cred := range auth {
		// Skip if already added from models
		if _, exists := result.Providers[name]; exists {
			continue
		}

		// Skip unsupported providers per SD-007
		if name == "google-gemini-cli" || name == "github-copilot" {
			result.Warnings.Add("skipped provider %q: not yet supported", name)
			continue
		}

		// Check for !command API key
		if len(cred.APIKey) > 0 && cred.APIKey[0] == '!' {
			result.Warnings.Add("provider %q uses shell-resolved key, set AGENT_API_KEY or add api_key manually", name)
			continue
		}

		// Try known mappings
		if mapping, known := knownMappings[name]; known {
			pc := agentConfig.ProviderConfig{
				Type:   mapping.Type,
				APIKey: cred.APIKey,
			}
			if cred.AccessToken != "" {
				pc.APIKey = cred.AccessToken
			}
			if mapping.BaseURL != "" {
				pc.BaseURL = mapping.BaseURL
			}
			result.Providers[mapping.AgentName] = pc
			continue
		}

		// Unknown provider
		result.Warnings.Add("skipped provider %q: unknown provider type", name)
	}

	// Step 4: Apply settings.json defaultProvider/defaultModel
	if settings != nil && settings.DefaultProvider != "" {
		// Map pi provider name to agent name
		agentName := settings.DefaultProvider
		if mapping, known := knownMappings[settings.DefaultProvider]; known {
			agentName = mapping.AgentName
		}
		// Check if this provider exists
		if pc, exists := result.Providers[agentName]; exists {
			result.Default = agentName
			if settings.DefaultModel != "" {
				pc.Model = settings.DefaultModel
				result.Providers[agentName] = pc
			}
		} else if settings.DefaultProvider != "" {
			result.Warnings.Add("default provider %q not found in config", settings.DefaultProvider)
		}
	}

	return result, nil
}

// translatedProvider holds both the agent name and config.
type translatedProvider struct {
	Name   string
	Config agentConfig.ProviderConfig
}

func translateProvider(def ProviderDefinition, cred AuthEntry) translatedProvider {
	name := def.Name
	if name == "" {
		name = def.Provider
	}

	pc := agentConfig.ProviderConfig{}

	// Determine type from api field or default
	switch def.API {
	case "anthropic":
		pc.Type = "anthropic"
	case "openai-completions", "openai-chat", "": // default to openai-compat for unknown/empty
		pc.Type = "openai-compat"
	default:
		pc.Type = "openai-compat"
	}

	// Set base URL
	if def.BaseURL != "" {
		pc.BaseURL = def.BaseURL
	}

	// Prefer auth.json API key, but use model's if auth doesn't have one
	if cred.APIKey != "" && cred.APIKey[0] != '!' {
		pc.APIKey = cred.APIKey
	} else if def.APIKey != "" && def.APIKey[0] != '!' {
		pc.APIKey = def.APIKey
	}

	// Check for !command values (unsupported)
	if cred.APIKey != "" && cred.APIKey[0] == '!' {
		// Will be skipped during translation
	}

	// Set model if specified
	if len(def.Models) > 0 {
		pc.Model = def.Models[0]
	}

	return translatedProvider{Name: name, Config: pc}
}

// ComputeSourceHash computes a truncated SHA-256 hash of the source files.
func ComputeSourceHash(piDir string) (string, error) {
	authPath := piDir + "/agent/auth.json"
	modelsPath := piDir + "/agent/models.json"

	authData, err := os.ReadFile(authPath)
	if err != nil {
		return "", err
	}
	modelsData, err := os.ReadFile(modelsPath)
	if err != nil {
		return "", err
	}

	// Concatenate and hash
	combined := append(authData, modelsData...)
	return hashString(combined), nil
}

func hashString(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])[:8]
}
