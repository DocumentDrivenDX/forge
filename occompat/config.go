package occompat

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// OpenCodeConfig represents the opencode.json configuration file.
type OpenCodeConfig struct {
	Options OpenCodeOptions `json:"options,omitempty"`
}

// OpenCodeOptions contains the configuration options.
type OpenCodeOptions struct {
	BaseURL  string            `json:"baseURL,omitempty"`
	APIKey   string            `json:"apiKey,omitempty"`
	Model    string            `json:"model,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	NPM      string            `json:"npm,omitempty"` // e.g., "@ai-sdk/openai-compatible"
}

// LoadConfig reads the opencode.json file from the project or global location.
func LoadConfig(projectDir string) (*OpenCodeConfig, error) {
	// Try project config first
	path := filepath.Join(projectDir, "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		// Fall back to global config
		home, errHome := os.UserHomeDir()
		if errHome != nil {
			return nil, err
		}
		path = filepath.Join(home, ".config", "opencode", "opencode.json")
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}

	var cfg OpenCodeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// LoadGlobalConfig reads the global opencode.json config file.
func LoadGlobalConfig() (*OpenCodeConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg OpenCodeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
