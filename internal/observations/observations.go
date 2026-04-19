// Package observations tracks per-(provider,model) speed samples
// for use by routing strategies.
package observations

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/safefs"
	"gopkg.in/yaml.v3"
)

const defaultCapacity = 200

// Key identifies a provider+model combination for observation tracking.
type Key struct {
	ProviderSystem string `yaml:"provider_system"`
	Model          string `yaml:"model"`
}

// flatKey returns a stable string representation for use as a map key.
func (k Key) flatKey() string {
	return k.ProviderSystem + ":" + k.Model
}

// Sample is one speed observation.
type Sample struct {
	OutputTokensPerSec float64   `yaml:"output_tokens_per_sec"`
	DurationMs         int64     `yaml:"duration_ms"`
	RecordedAt         time.Time `yaml:"recorded_at"`
}

// Ring is a fixed-capacity circular buffer of Samples.
type Ring struct {
	Capacity int      `yaml:"capacity"`
	Samples  []Sample `yaml:"samples"`
	// head points to the slot where the next sample will be written.
	head int `yaml:"-"`
}

// NewRing creates a new Ring with the given capacity.
func NewRing(capacity int) *Ring {
	return &Ring{
		Capacity: capacity,
		Samples:  make([]Sample, 0, capacity),
	}
}

// Add appends a sample to the ring, overwriting the oldest entry when full.
func (r *Ring) Add(sample Sample) {
	if len(r.Samples) < r.Capacity {
		// Still filling up.
		r.Samples = append(r.Samples, sample)
		r.head = len(r.Samples) % r.Capacity
	} else {
		// Overwrite oldest slot.
		r.Samples[r.head] = sample
		r.head = (r.head + 1) % r.Capacity
	}
}

// Mean returns the mean output tokens/sec of all non-zero samples.
// ok is false when the ring is empty.
func (r *Ring) Mean() (outputTokensPerSec float64, ok bool) {
	var sum float64
	var count int
	for _, s := range r.Samples {
		if s.OutputTokensPerSec > 0 {
			sum += s.OutputTokensPerSec
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return sum / float64(count), true
}

// ringEntry is used for YAML serialization of the Store.
type ringEntry struct {
	ProviderSystem string `yaml:"provider_system"`
	Model          string `yaml:"model"`
	Ring           *Ring  `yaml:"ring"`
}

// Store holds one Ring per Key.
// Internally uses a flat "providerSystem:model" string key for simplicity.
type Store struct {
	rings map[string]*Ring
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{rings: make(map[string]*Ring)}
}

// Add records a sample for the given key, auto-creating a Ring(cap=200) if needed.
func (s *Store) Add(key Key, sample Sample) {
	k := key.flatKey()
	if s.rings[k] == nil {
		s.rings[k] = NewRing(defaultCapacity)
	}
	s.rings[k].Add(sample)
}

// MeanSpeed returns the mean output tokens/sec for the given key.
func (s *Store) MeanSpeed(key Key) (float64, bool) {
	r, ok := s.rings[key.flatKey()]
	if !ok || r == nil {
		return 0, false
	}
	return r.Mean()
}

// AllKeys returns all provider+model keys present in the store.
func (s *Store) AllKeys() []Key {
	out := make([]Key, 0, len(s.rings))
	for flat := range s.rings {
		ps, m, err := splitFlatKey(flat)
		if err != nil {
			continue
		}
		out = append(out, Key{ProviderSystem: ps, Model: m})
	}
	return out
}

// RingFor returns the Ring for the given key, or nil if not present.
func (s *Store) RingFor(key Key) *Ring {
	return s.rings[key.flatKey()]
}

// MarshalYAML serializes the store as a sequence of entries.
func (s *Store) MarshalYAML() (interface{}, error) {
	type storeYAML struct {
		Rings []ringEntry `yaml:"rings"`
	}
	out := storeYAML{Rings: make([]ringEntry, 0, len(s.rings))}
	for flat, r := range s.rings {
		ps, m, _ := splitFlatKey(flat)
		out.Rings = append(out.Rings, ringEntry{
			ProviderSystem: ps,
			Model:          m,
			Ring:           r,
		})
	}
	return out, nil
}

// UnmarshalYAML deserializes the store from a sequence of entries.
func (s *Store) UnmarshalYAML(value *yaml.Node) error {
	type storeYAML struct {
		Rings []ringEntry `yaml:"rings"`
	}
	var raw storeYAML
	if err := value.Decode(&raw); err != nil {
		return err
	}
	if s.rings == nil {
		s.rings = make(map[string]*Ring)
	}
	for _, e := range raw.Rings {
		k := Key{ProviderSystem: e.ProviderSystem, Model: e.Model}
		ring := e.Ring
		if ring == nil {
			ring = NewRing(defaultCapacity)
		}
		// Restore head: since we always append in order, head == len%cap
		if ring.Capacity > 0 {
			ring.head = len(ring.Samples) % ring.Capacity
		}
		s.rings[k.flatKey()] = ring
	}
	return nil
}

// splitFlatKey splits a "providerSystem:model" key back into its parts.
func splitFlatKey(flat string) (providerSystem, model string, err error) {
	for i, c := range flat {
		if c == ':' {
			return flat[:i], flat[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("invalid flat key: %q", flat)
}

// LoadStore reads a Store from path. Returns an empty store on not-found.
// If the DDX_OBSERVATIONS_FILE env var is set and path is DefaultStorePath(),
// the env var value takes precedence (handled by DefaultStorePath).
func LoadStore(path string) (*Store, error) {
	data, err := safefs.ReadFile(path)
	if os.IsNotExist(err) {
		return NewStore(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("observations: read %s: %w", path, err)
	}
	store := NewStore()
	if err := yaml.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("observations: parse %s: %w", path, err)
	}
	return store, nil
}

// Save writes the store to path atomically (write temp file, rename).
func (s *Store) Save(path string) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("observations: marshal: %w", err)
	}
	dir := filepath.Dir(path)
	if err := safefs.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("observations: create dir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "observations-*.yaml")
	if err != nil {
		return fmt.Errorf("observations: create temp file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath) // clean up on failure
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("observations: write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("observations: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("observations: rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// DefaultStorePath returns the path for the observations file, respecting XDG.
// If the DDX_OBSERVATIONS_FILE environment variable is set, it takes precedence.
func DefaultStorePath() string {
	if env := os.Getenv("DDX_OBSERVATIONS_FILE"); env != "" {
		return env
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg != "" {
		return filepath.Join(xdg, "ddx-agent", "observations.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "ddx-agent", "observations.yaml")
	}
	return filepath.Join(home, ".config", "ddx-agent", "observations.yaml")
}
