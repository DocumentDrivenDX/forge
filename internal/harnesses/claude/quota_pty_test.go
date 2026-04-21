package claude

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses/ptyquota"
	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/stretchr/testify/require"
)

func TestReadClaudeQuotaViaPTYWaitsForRequiredUsageSections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf 'Claude Max\r\n❯ '
IFS= read line
printf 'Current session\r\n4%% used\r\nResets 4pm (UTC)\r\n'
printf 'Current week (all models)\r\n'
sleep 5
`), 0o700))
	cassetteDir := filepath.Join(dir, "cassette")

	windows, account, err := ReadClaudeQuotaViaPTY(200*time.Millisecond, WithQuotaPTYCommand(script), WithQuotaPTYCassetteDir(cassetteDir))
	require.Error(t, err)
	require.Empty(t, windows)
	require.Nil(t, account)
	require.Equal(t, ptyquota.StatusError, ptyquota.ErrorStatus(err))
	require.Contains(t, err.Error(), "timed out")
	_, statErr := os.Stat(filepath.Join(cassetteDir, cassette.ManifestFile))
	require.True(t, errors.Is(statErr, os.ErrNotExist), "partial usage output should not promote a cassette")
}

func TestReadClaudeQuotaViaPTYAcceptsSonnetOnlyWeeklyUsage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf 'Claude Max\r\n❯ '
IFS= read line
cat <<'EOF'
Current session
4% used
Resets 4pm (UTC)
Current week (Sonnet only)
10% used
Resets Monday (UTC)
EOF
sleep 1
`), 0o700))

	windows, account, err := ReadClaudeQuotaViaPTY(2*time.Second, WithQuotaPTYCommand(script))
	require.NoError(t, err)
	require.NotNil(t, account)
	require.True(t, hasQuotaWindow(windows, "session"))
	require.True(t, hasQuotaWindow(windows, "weekly-sonnet"))
}
