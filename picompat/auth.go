// Package picompat provides compatibility for importing configurations from pi.
package picompat

import (
	"encoding/json"
	"path/filepath"

	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// AuthEntry represents a single credential entry in auth.json.
// Pi uses several field-name conventions across versions:
//   - OAuth entries use "access" (token) and "refresh"
//   - API-key entries use "key" (preferred) or "api_key"
//   - The "type" field is "oauth" or "api_key"
type AuthEntry struct {
	// OAuth fields
	AuthType     string `json:"type,omitempty"`
	AccessToken  string `json:"access,omitempty"`  // pi oauth
	RefreshToken string `json:"refresh,omitempty"` // pi oauth
	// API-key fields — pi uses "key"; some versions use "api_key"
	Key     string `json:"key,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Expires int64  `json:"expires,omitempty"` // epoch milliseconds
}

// ResolvedKey returns the usable credential: the OAuth access token for oauth
// entries, or the API key for api_key entries. Returns "" if nothing is set.
func (a AuthEntry) ResolvedKey() string {
	if a.AccessToken != "" {
		return a.AccessToken
	}
	if a.Key != "" {
		return a.Key
	}
	return a.APIKey
}

// AuthCredentials maps provider names to their credentials.
type AuthCredentials map[string]AuthEntry

// LoadAuth reads the pi auth.json file.
func LoadAuth(piDir string) (AuthCredentials, error) {
	path := filepath.Join(piDir, "agent", "auth.json")
	data, err := safefs.ReadFile(path)
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
