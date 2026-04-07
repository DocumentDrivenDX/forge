// Package occompat provides compatibility for importing configurations from opencode.
package occompat

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AuthEntry represents a credential entry in opencode auth.json.
type AuthEntry struct {
	Type string `json:"type,omitempty"` // "api", "bearer", etc.
	Key  string `json:"key,omitempty"`
}

// AuthCredentials maps provider names to their credentials.
type AuthCredentials map[string]AuthEntry

// LoadAuth reads the opencode auth.json file.
func LoadAuth(opencodeDir string) (AuthCredentials, error) {
	path := filepath.Join(opencodeDir, "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds AuthCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	return creds, nil
}
