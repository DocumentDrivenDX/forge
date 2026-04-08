package picompat

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings represents the pi settings.json file.
type Settings struct {
	DefaultProvider string `json:"defaultProvider,omitempty"`
	DefaultModel    string `json:"defaultModel,omitempty"`
	MaxIterations   int    `json:"max_iterations,omitempty"`
	// Other fields we don't care about for agent import
}

// LoadSettings reads the pi settings.json file.
func LoadSettings(piDir string) (*Settings, error) {
	path := filepath.Join(piDir, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	return &settings, nil
}
