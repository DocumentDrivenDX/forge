// Package observations provides a simple key-value store for recording and
// querying observed runtime metrics (e.g. tokens-per-second) across providers
// and models.
package observations

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Key identifies an observed metric by provider system and model.
type Key struct {
	ProviderSystem string
	Model          string
}

// Store holds aggregated observations indexed by Key.
type Store struct {
	entries map[Key]entry
}

type entry struct {
	sumSpeed float64
	count    int
}

// MeanSpeed returns the mean observed tokens-per-second for the given key.
// Returns (0, false) if no observations are recorded for the key.
func (s *Store) MeanSpeed(key Key) (float64, bool) {
	if s == nil {
		return 0, false
	}
	e, ok := s.entries[key]
	if !ok || e.count == 0 {
		return 0, false
	}
	return e.sumSpeed / float64(e.count), true
}

// Record adds a single tokens-per-second observation for the given key.
func (s *Store) Record(key Key, tokensPerSec float64) {
	if s == nil {
		return
	}
	e := s.entries[key]
	e.sumSpeed += tokensPerSec
	e.count++
	s.entries[key] = e
}

// storedEntry is the on-disk representation of a single observation record.
type storedEntry struct {
	ProviderSystem string  `json:"provider_system"`
	Model          string  `json:"model"`
	TokensPerSec   float64 `json:"tokens_per_sec"`
}

// DefaultStorePath returns the default file path for the observations store.
func DefaultStorePath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "agent", "observations.jsonl")
}

// LoadStore loads observations from a JSONL file at path.
// Each line is expected to be a JSON object with provider_system, model, and
// tokens_per_sec fields. Lines that cannot be parsed are silently skipped.
// Returns a non-nil Store even on partial load or when path does not exist.
func LoadStore(path string) (*Store, error) {
	s := &Store{entries: make(map[Key]entry)}
	if path == "" {
		return s, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}

	// Parse JSONL: one JSON object per line.
	lines := splitLines(data)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var rec storedEntry
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.ProviderSystem == "" || rec.Model == "" || rec.TokensPerSec <= 0 {
			continue
		}
		key := Key{ProviderSystem: rec.ProviderSystem, Model: rec.Model}
		e := s.entries[key]
		e.sumSpeed += rec.TokensPerSec
		e.count++
		s.entries[key] = e
	}

	return s, nil
}

// splitLines splits data on newline characters, returning each non-empty line.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
