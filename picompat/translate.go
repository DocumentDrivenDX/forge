package picompat

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	agentConfig "github.com/DocumentDrivenDX/agent/config"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// ProviderMapping maps pi provider names to agent configurations.
type ProviderMapping struct {
	AgentName string // name in agent config
	Type      string // agent type: "anthropic" or "openai-compat"
	BaseURL   string // default URL if not specified in pi
}

// Known mappings per SD-007. Keyed by the provider name as it appears in
// pi's auth.json. BaseURL is the canonical OpenAI-compatible endpoint for
// cloud providers; local providers get their URL from models.json instead.
var knownMappings = map[string]ProviderMapping{
	// Established cloud providers
	"anthropic":    {AgentName: "anthropic", Type: "anthropic"},
	"openai-codex": {AgentName: "openai", Type: "openai-compat", BaseURL: "https://api.openai.com/v1"},
	"openrouter":   {AgentName: "openrouter", Type: "openai-compat", BaseURL: "https://openrouter.ai/api/v1"},
	// Qwen / Alibaba Cloud DashScope
	"qwen":       {AgentName: "qwen", Type: "openai-compat", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	"dashscope":  {AgentName: "qwen", Type: "openai-compat", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	// MiniMax
	"minimax": {AgentName: "minimax", Type: "openai-compat", BaseURL: "https://api.minimaxi.chat/v1"},
	// Z.ai
	"z.ai": {AgentName: "z.ai", Type: "openai-compat", BaseURL: "https://api.z.ai/v1"},
	"zai":  {AgentName: "z.ai", Type: "openai-compat", BaseURL: "https://api.z.ai/v1"},
}

// thinkingModelRe matches model IDs that support extended thinking / reasoning
// tokens. When a provider's default model matches, thinking_level is set to
// "medium" automatically during import.
var thinkingModelRe = regexp.MustCompile(
	`(?i)^(qwen3|qwen-3|deepseek-r1|deepseek_r1|qwq)`,
)

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
		resolvedKey := cred.ResolvedKey()
		if len(resolvedKey) > 0 && resolvedKey[0] == '!' {
			result.Warnings.Add("provider %q uses shell-resolved key, set AGENT_API_KEY or add api_key manually", name)
			continue
		}

		// Try known mappings
		if mapping, known := knownMappings[name]; known {
			pc := agentConfig.ProviderConfig{
				Type:   mapping.Type,
				APIKey: resolvedKey,
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

	// Prefer auth.json credential, fall back to model's inline api_key.
	credKey := cred.ResolvedKey()
	if credKey != "" && credKey[0] != '!' {
		pc.APIKey = credKey
	} else if def.APIKey != "" && def.APIKey[0] != '!' {
		pc.APIKey = def.APIKey
	}

	// Set model if specified.
	if len(def.Models) > 0 {
		pc.Model = def.Models[0]
	}

	// Auto-configure thinking_level for known reasoning models (Qwen3,
	// DeepSeek-R1, QwQ). Only set when not already specified.
	if pc.ThinkingLevel == "" && pc.Model != "" && isThinkingModel(pc.Model) {
		pc.ThinkingLevel = "medium"
	}

	return translatedProvider{Name: name, Config: pc}
}

// isThinkingModel reports whether modelID belongs to a model family that
// supports extended thinking / reasoning tokens and benefits from an explicit
// thinking budget in the provider config. Claude-distilled variants (e.g.
// "qwen3-claude-opus-distilled") are excluded since they do not expose a
// native thinking API.
func isThinkingModel(modelID string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if strings.Contains(lower, "claude") {
		return false
	}
	return thinkingModelRe.MatchString(strings.TrimSpace(modelID))
}

// ComputeSourceHash computes a truncated SHA-256 hash of the source files.
func ComputeSourceHash(piDir string) (string, error) {
	authPath := piDir + "/agent/auth.json"
	modelsPath := piDir + "/agent/models.json"

	authData, err := safefs.ReadFile(authPath)
	if err != nil {
		return "", err
	}
	modelsData, err := safefs.ReadFile(modelsPath)
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
