package catalogdist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// Index describes a published catalog bundle location.
type Index struct {
	SchemaVersion   int    `json:"schema_version"`
	CatalogVersion  string `json:"catalog_version"`
	Channel         string `json:"channel,omitempty"`
	PublishedAt     string `json:"published_at"`
	ManifestPath    string `json:"manifest_path"`
	ManifestSHA256  string `json:"manifest_sha256"`
	MinAgentVersion string `json:"min_agent_version"`
	Notes           string `json:"notes,omitempty"`
}

// BuildOptions configures catalog bundle generation.
type BuildOptions struct {
	ManifestPath    string
	OutputDir       string
	Channel         string
	PublishedAt     time.Time
	MinAgentVersion string
	Notes           string
}

// Build validates a manifest and writes the stable plus versioned published bundle layout.
func Build(opts BuildOptions) (Index, error) {
	if strings.TrimSpace(opts.ManifestPath) == "" {
		return Index{}, fmt.Errorf("catalogdist: manifest path is required")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		return Index{}, fmt.Errorf("catalogdist: output directory is required")
	}
	channel := strings.TrimSpace(opts.Channel)
	if channel == "" {
		channel = "stable"
	}

	data, err := os.ReadFile(opts.ManifestPath)
	if err != nil {
		return Index{}, fmt.Errorf("catalogdist: read manifest: %w", err)
	}
	catalog, err := modelcatalog.Load(modelcatalog.LoadOptions{
		ManifestPath:    opts.ManifestPath,
		RequireExternal: true,
	})
	if err != nil {
		return Index{}, err
	}
	meta := catalog.Metadata()
	if strings.TrimSpace(meta.CatalogVersion) == "" {
		return Index{}, fmt.Errorf("catalogdist: manifest %s missing catalog_version", opts.ManifestPath)
	}

	hash := sha256.Sum256(data)
	checksum := hex.EncodeToString(hash[:])
	publishedAt := opts.PublishedAt.UTC()
	if publishedAt.IsZero() {
		publishedAt = time.Now().UTC()
	}
	index := Index{
		SchemaVersion:   meta.ManifestVersion,
		CatalogVersion:  meta.CatalogVersion,
		Channel:         channel,
		PublishedAt:     publishedAt.Format(time.RFC3339),
		ManifestPath:    "models.yaml",
		ManifestSHA256:  checksum,
		MinAgentVersion: strings.TrimPrefix(strings.TrimSpace(opts.MinAgentVersion), "v"),
		Notes:           strings.TrimSpace(opts.Notes),
	}

	if err := writeBundle(filepath.Join(opts.OutputDir, channel), index, data, checksum); err != nil {
		return Index{}, err
	}

	versionIndex := index
	versionIndex.Channel = meta.CatalogVersion
	if err := writeBundle(filepath.Join(opts.OutputDir, "versions", meta.CatalogVersion), versionIndex, data, checksum); err != nil {
		return Index{}, err
	}

	return index, nil
}

func writeBundle(dir string, index Index, manifestData []byte, checksum string) error {
	if err := safefs.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("catalogdist: create output dir %s: %w", dir, err)
	}
	if err := safefs.WriteFile(filepath.Join(dir, "models.yaml"), manifestData, 0o600); err != nil {
		return fmt.Errorf("catalogdist: write models.yaml: %w", err)
	}
	if err := safefs.WriteFile(filepath.Join(dir, "models.sha256"), []byte(checksum+"\n"), 0o600); err != nil {
		return fmt.Errorf("catalogdist: write models.sha256: %w", err)
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("catalogdist: encode index: %w", err)
	}
	data = append(data, '\n')
	if err := safefs.WriteFile(filepath.Join(dir, "index.json"), data, 0o600); err != nil {
		return fmt.Errorf("catalogdist: write index.json: %w", err)
	}
	return nil
}
