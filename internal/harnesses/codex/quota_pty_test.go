package codex

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

func TestReadCodexQuotaViaPTYDoesNotAcceptStaleStartupStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-codex")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf '› gpt-5.4 high · 100%% left · /tmp/work\n'
IFS= read line
sleep 5
`), 0o700))
	cassetteDir := filepath.Join(dir, "cassette")

	windows, err := ReadCodexQuotaViaPTY(200*time.Millisecond, WithQuotaPTYCommand(script), WithQuotaPTYCassetteDir(cassetteDir))
	require.Error(t, err)
	require.Empty(t, windows)
	require.Equal(t, ptyquota.StatusError, ptyquota.ErrorStatus(err))
	_, statErr := os.Stat(filepath.Join(cassetteDir, cassette.ManifestFile))
	require.True(t, errors.Is(statErr, os.ErrNotExist), "stale startup output should not promote a cassette")
}
