package codex

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

func TestCodexQuotaSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-quota.json")
	original := CodexQuotaSnapshot{
		CapturedAt: time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
		Source:     "pty",
		Windows: []harnesses.QuotaWindow{
			{Name: "5h", LimitID: "codex", WindowMinutes: 300, UsedPercent: 25, State: "ok"},
		},
	}
	if err := WriteCodexQuota(path, original); err != nil {
		t.Fatalf("WriteCodexQuota: %v", err)
	}
	loaded, ok := ReadCodexQuotaFrom(path)
	if !ok {
		t.Fatal("ReadCodexQuotaFrom returned ok=false")
	}
	if !loaded.CapturedAt.Equal(original.CapturedAt) {
		t.Fatalf("CapturedAt: got %v, want %v", loaded.CapturedAt, original.CapturedAt)
	}
	if loaded.Source != "pty" {
		t.Fatalf("Source: got %q, want pty", loaded.Source)
	}
	if len(loaded.Windows) != 1 || loaded.Windows[0].UsedPercent != 25 {
		t.Fatalf("Windows: got %#v", loaded.Windows)
	}
}

func TestReadCodexQuotaUsesDefaultPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-quota.json")
	t.Setenv(codexQuotaCacheEnv, path)
	if err := WriteCodexQuota(path, CodexQuotaSnapshot{
		CapturedAt: time.Now().UTC(),
		Source:     "pty",
		Windows:    []harnesses.QuotaWindow{{Name: "5h", State: "ok"}},
	}); err != nil {
		t.Fatalf("WriteCodexQuota: %v", err)
	}
	if _, ok := ReadCodexQuota(); !ok {
		t.Fatal("ReadCodexQuota returned ok=false")
	}
}

func TestIsCodexQuotaFresh(t *testing.T) {
	now := time.Now().UTC()
	if IsCodexQuotaFresh(nil, now, time.Minute) {
		t.Fatal("nil snapshot should not be fresh")
	}
	fresh := &CodexQuotaSnapshot{CapturedAt: now.Add(-30 * time.Second)}
	if !IsCodexQuotaFresh(fresh, now, time.Minute) {
		t.Fatal("fresh snapshot should be fresh")
	}
	stale := &CodexQuotaSnapshot{CapturedAt: now.Add(-2 * time.Minute)}
	if IsCodexQuotaFresh(stale, now, time.Minute) {
		t.Fatal("stale snapshot should not be fresh")
	}
}
