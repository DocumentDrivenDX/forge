package picompat

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ProviderDefinition represents a provider in models.json.
type ProviderDefinition struct {
	Name        string                 `json:"name,omitempty"`
	Provider    string                 `json:"provider,omitempty"`
	BaseURL     string                 `json:"baseUrl,omitempty"`
	APIKey      string                 `json:"api_key,omitempty"`
	API         string                 `json:"api,omitempty"` // e.g., "openai-completions", "anthropic"
	Models      []string               `json:"models,omitempty"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
}

// ModelsConfig represents the pi models.json file.
type ModelsConfig struct {
	Providers []ProviderDefinition `json:"providers,omitempty"`
	Models    []ModelDefinition    `json:"models,omitempty"`
}

// ModelDefinition represents a model entry in models.json.
type ModelDefinition struct {
	ID       string `json:"id,omitempty"`
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
}

// LoadModels reads the pi models.json file.
func LoadModels(piDir string) (*ModelsConfig, error) {
	path := filepath.Join(piDir, "agent", "models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// GetProviderByName finds a provider definition by name.
func (m *ModelsConfig) GetProviderByName(name string) *ProviderDefinition {
	for i := range m.Providers {
		if m.Providers[i].Name == name || m.Providers[i].Provider == name {
			return &m.Providers[i]
		}
	}
	return nil
}
