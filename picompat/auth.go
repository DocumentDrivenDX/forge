// Package picompat provides compatibility for importing configurations from pi.
package picompat

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AuthEntry represents a single credential entry in auth.json.
type AuthEntry struct {
	AccessToken string `json:"access_token,omitempty"`
	TokenType  string `json:"token_type,omitempty"`
	Expires    int64  `json:"expires,omitempty"` // epoch milliseconds
	APIKey     string `json:"api_key,omitempty"`
}

// AuthCredentials maps provider names to their credentials.
type AuthCredentials map[string]AuthEntry

// LoadAuth reads the pi auth.json file.
func LoadAuth(piDir string) (AuthCredentials, error) {
	path := filepath.Join(piDir, "agent", "auth.json")
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

// TokenExpiryStatus returns the expiry status of a credential.
// Returns hours remaining if not expired, negative if expired, and a warning message.
func (a *AuthEntry) TokenExpiryStatus() (hoursRemaining float64, warning string) {
	if a.Expires == 0 {
		return 24, "" // no expiry info, assume safe
	}

	// Convert from milliseconds to hours
	hoursRemaining = float64(a.Expires) / (1000 * 60 * 60)

	if hoursRemaining < 0 {
		return hoursRemaining, "token already expired"
	}
	if hoursRemaining < 24 {
		return hoursRemaining, "token expires soon"
	}
	return hoursRemaining, ""
}
