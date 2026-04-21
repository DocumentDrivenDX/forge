//go:build integration && !windows

package codex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/stretchr/testify/require"
)

func Test_quotaRecordCodexPTY(t *testing.T) {
	if os.Getenv("AGENT_HARNESS_RECORD") != "1" {
		t.Skip("set AGENT_HARNESS_RECORD=1 to refresh authenticated codex quota cassette")
	}
	dir := filepath.Join(recordBaseDir(t), "codex", "quota")
	windows, err := ReadCodexQuotaViaPTY(45*time.Second, WithQuotaPTYCassetteDir(dir))
	if err != nil {
		assertNoAcceptedCassette(t, dir)
		t.Fatalf("record codex quota via PTY: %v", err)
	}
	require.NotEmpty(t, windows)
	reader, err := cassette.Open(dir)
	require.NoError(t, err)
	require.NotNil(t, reader.Quota())
}

func recordBaseDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("AGENT_HARNESS_CASSETTE_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("AGENT_HARNESS_RECORD_DIR"); dir != "" {
		return dir
	}
	return t.TempDir()
}

func assertNoAcceptedCassette(t *testing.T, dir string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, cassette.ManifestFile))
	if err == nil {
		t.Fatalf("failed quota record left accepted cassette evidence at %s", dir)
	}
	require.True(t, errors.Is(err, os.ErrNotExist), "unexpected cassette stat error")
}
