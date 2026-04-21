package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// CodexQuotaSnapshot captures Codex subscription quota windows in a durable
// cache so foreground service status calls do not need to spawn a live PTY probe.
type CodexQuotaSnapshot struct {
	CapturedAt time.Time               `json:"captured_at"`
	Windows    []harnesses.QuotaWindow `json:"windows"`
	Source     string                  `json:"source"`
}

const DefaultCodexQuotaStaleAfter = 5 * time.Minute

const codexQuotaCacheEnv = "DDX_AGENT_CODEX_QUOTA_CACHE"

// CodexQuotaCachePath returns the durable location for the Codex quota cache.
func CodexQuotaCachePath() (string, error) {
	if path := os.Getenv(codexQuotaCacheEnv); path != "" {
		return path, nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "ddx-agent", "codex-quota.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "ddx-agent", "codex-quota.json"), nil
}

// WriteCodexQuota atomically persists a CodexQuotaSnapshot to path.
func WriteCodexQuota(path string, snapshot CodexQuotaSnapshot) error {
	if path == "" {
		return fmt.Errorf("codex quota cache path is empty")
	}
	if snapshot.CapturedAt.IsZero() {
		snapshot.CapturedAt = time.Now().UTC()
	} else {
		snapshot.CapturedAt = snapshot.CapturedAt.UTC()
	}
	if snapshot.Source == "" {
		snapshot.Source = "pty"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create codex quota cache dir: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal codex quota snapshot: %w", err)
	}
	data = append(data, '\n')
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write codex quota cache tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename codex quota cache: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod codex quota cache: %w", err)
	}
	return nil
}

// ReadCodexQuotaFrom reads one Codex quota snapshot.
func ReadCodexQuotaFrom(path string) (*CodexQuotaSnapshot, bool) {
	data, err := safefs.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var snap CodexQuotaSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, false
	}
	return &snap, true
}

// ReadCodexQuota reads the default Codex quota cache.
func ReadCodexQuota() (*CodexQuotaSnapshot, bool) {
	path, err := CodexQuotaCachePath()
	if err != nil {
		return nil, false
	}
	return ReadCodexQuotaFrom(path)
}

// CodexQuotaSnapshotAge reports snapshot age relative to now.
func CodexQuotaSnapshotAge(snapshot *CodexQuotaSnapshot, now time.Time) time.Duration {
	if snapshot == nil || snapshot.CapturedAt.IsZero() {
		return 0
	}
	age := now.UTC().Sub(snapshot.CapturedAt.UTC())
	if age < 0 {
		return 0
	}
	return age
}

// IsCodexQuotaFresh reports whether a snapshot is present and fresh.
func IsCodexQuotaFresh(snapshot *CodexQuotaSnapshot, now time.Time, staleAfter time.Duration) bool {
	if snapshot == nil || snapshot.CapturedAt.IsZero() {
		return false
	}
	if staleAfter <= 0 {
		staleAfter = DefaultCodexQuotaStaleAfter
	}
	return CodexQuotaSnapshotAge(snapshot, now) <= staleAfter
}
