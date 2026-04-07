// Package occompat provides compatibility for importing configurations from opencode.
package occompat

import (
	"os"
	"os/user"
)

// DefaultOpenCodeDir returns the default opencode config directory.
func DefaultOpenCodeDir() string {
	home, err := user.Current()
	if err != nil {
		return ""
	}
	return home.HomeDir + "/.local/share/opencode"
}

// CheckExists checks if opencode config directory and auth.json exist.
func CheckExists() bool {
	dir := DefaultOpenCodeDir()
	if dir == "" {
		return false
	}
	_, err := os.Stat(dir + "/auth.json")
	return err == nil
}
