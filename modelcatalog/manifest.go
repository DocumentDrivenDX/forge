package modelcatalog

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	statusActive     = "active"
	statusDeprecated = "deprecated"
	statusStale      = "stale"
)

//go:embed catalog/models.yaml
var embeddedManifest []byte

// LoadOptions configures how a catalog manifest is loaded.
type LoadOptions struct {
	ManifestPath    string
	RequireExternal bool
}

type manifest struct {
	Version     int                     `yaml:"version"`
	GeneratedAt string                  `yaml:"generated_at"`
	Profiles    map[string]profileEntry `yaml:"profiles"`
	Targets     map[string]targetEntry  `yaml:"targets"`
}

type profileEntry struct {
	Target string `yaml:"target"`
}

type targetEntry struct {
	Family       string            `yaml:"family"`
	Aliases      []string          `yaml:"aliases"`
	Status       string            `yaml:"status"`
	Replacement  string            `yaml:"replacement,omitempty"`
	DeprecatedAt string            `yaml:"deprecated_at,omitempty"`
	Surfaces     map[string]string `yaml:"surfaces"`
}

// Default loads the embedded default catalog snapshot.
func Default() (*Catalog, error) {
	return Load(LoadOptions{})
}

// Load loads a catalog from an external manifest or falls back to the embedded snapshot.
func Load(opts LoadOptions) (*Catalog, error) {
	data := embeddedManifest
	source := "embedded"

	if opts.ManifestPath != "" {
		externalData, err := os.ReadFile(opts.ManifestPath)
		if err != nil {
			if opts.RequireExternal {
				return nil, fmt.Errorf("modelcatalog: read manifest %s: %w", opts.ManifestPath, err)
			}
		} else {
			data = externalData
			source = opts.ManifestPath
		}
	}

	return loadManifest(data, source)
}

func loadManifest(data []byte, source string) (*Catalog, error) {
	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("modelcatalog: parse manifest %s: %w", source, err)
	}
	if err := validateManifest(m); err != nil {
		return nil, fmt.Errorf("modelcatalog: validate manifest %s: %w", source, err)
	}

	catalog := &Catalog{
		manifest:    m,
		manifestSrc: source,
		aliasToID:   make(map[string]string),
		profileToID: make(map[string]string),
	}

	for profile, entry := range m.Profiles {
		catalog.profileToID[profile] = entry.Target
	}
	for targetID, target := range m.Targets {
		for _, alias := range target.Aliases {
			catalog.aliasToID[alias] = targetID
		}
	}

	return catalog, nil
}

func validateManifest(m manifest) error {
	if m.Version <= 0 {
		return fmt.Errorf("version must be greater than zero")
	}
	if len(m.Targets) == 0 {
		return fmt.Errorf("targets must not be empty")
	}

	reserved := make(map[string]string)
	targetIDs := make([]string, 0, len(m.Targets))
	for targetID := range m.Targets {
		targetIDs = append(targetIDs, targetID)
	}
	sort.Strings(targetIDs)

	for _, targetID := range targetIDs {
		target := m.Targets[targetID]
		if strings.TrimSpace(targetID) == "" {
			return fmt.Errorf("target ID must not be empty")
		}
		if strings.TrimSpace(target.Family) == "" {
			return fmt.Errorf("target %q must define family", targetID)
		}
		if len(target.Surfaces) == 0 {
			return fmt.Errorf("target %q must define at least one surface", targetID)
		}

		status := normalizedStatus(target.Status)
		switch status {
		case statusActive, statusDeprecated, statusStale:
		default:
			return fmt.Errorf("target %q has invalid status %q", targetID, target.Status)
		}

		if target.Replacement != "" {
			if _, ok := m.Targets[target.Replacement]; !ok {
				return fmt.Errorf("target %q replacement %q not found", targetID, target.Replacement)
			}
			if replacementCycle := findReplacementCycle(m, targetID); replacementCycle != "" {
				return fmt.Errorf("target %q replacement chain contains cycle via %q", targetID, replacementCycle)
			}
		}

		if owner, exists := reserved[targetID]; exists {
			return fmt.Errorf("reference %q collides with %s", targetID, owner)
		}
		reserved[targetID] = fmt.Sprintf("target %q", targetID)

		for surface, concrete := range target.Surfaces {
			if strings.TrimSpace(surface) == "" {
				return fmt.Errorf("target %q has empty surface key", targetID)
			}
			if strings.TrimSpace(concrete) == "" {
				return fmt.Errorf("target %q has empty model for surface %q", targetID, surface)
			}
		}

		for _, alias := range target.Aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				return fmt.Errorf("target %q has empty alias", targetID)
			}
			if owner, exists := reserved[alias]; exists {
				return fmt.Errorf("alias %q for target %q collides with %s", alias, targetID, owner)
			}
			reserved[alias] = fmt.Sprintf("alias for target %q", targetID)
		}
	}

	profiles := make([]string, 0, len(m.Profiles))
	for profile := range m.Profiles {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)

	for _, profile := range profiles {
		entry := m.Profiles[profile]
		if strings.TrimSpace(profile) == "" {
			return fmt.Errorf("profile name must not be empty")
		}
		if strings.TrimSpace(entry.Target) == "" {
			return fmt.Errorf("profile %q must define target", profile)
		}
		if _, ok := m.Targets[entry.Target]; !ok {
			return fmt.Errorf("profile %q references unknown target %q", profile, entry.Target)
		}
		if owner, exists := reserved[profile]; exists {
			return fmt.Errorf("profile %q collides with %s", profile, owner)
		}
		reserved[profile] = fmt.Sprintf("profile %q", profile)
	}

	return nil
}

func normalizedStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return statusActive
	}
	return status
}

func findReplacementCycle(m manifest, start string) string {
	seen := map[string]bool{start: true}
	current := start
	for {
		next := strings.TrimSpace(m.Targets[current].Replacement)
		if next == "" {
			return ""
		}
		if seen[next] {
			return next
		}
		seen[next] = true
		current = next
	}
}
