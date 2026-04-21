package pi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiDiscoveryFromHelp(t *testing.T) {
	text := `Usage:
  --model <id>                   Model ID (default: gemini-2.5-flash)
  --list-models [search]         List available models (with optional fuzzy search)
  --thinking <level>             Set thinking level: off, minimal, low, medium, high, xhigh
`
	snapshot := piDiscoveryFromHelp(text, "test-help")
	require.Equal(t, []string{"gemini-2.5-flash"}, snapshot.Models)
	require.Equal(t, []string{"off", "minimal", "low", "medium", "high", "xhigh"}, snapshot.ReasoningLevels)
	require.Equal(t, "test-help", snapshot.Source)
	require.NotEmpty(t, snapshot.FreshnessWindow)
}

func TestParsePiListModels(t *testing.T) {
	text := `provider           model                      context  max-out  thinking  images
google-gemini-cli  gemini-2.5-flash           1.0M     65.5K    yes       yes
google-gemini-cli  gemini-2.5-pro             1.0M     65.5K    yes       yes
openrouter         anthropic/claude-sonnet-4  1M       64K      yes       yes
google-gemini-cli  gemini-2.5-pro             1.0M     65.5K    yes       yes
`
	require.Equal(t, []string{"gemini-2.5-flash", "gemini-2.5-pro", "anthropic/claude-sonnet-4"}, parsePiListModels(text))
}

func TestReadPiModelDiscoveryFromHelp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed helper requires Unix script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
cat <<'EOF'
Usage:
  --model <id>                   Model ID (default: gemini-2.5-flash)
  --list-models [search]         List available models (with optional fuzzy search)
  --thinking <level>             Set thinking level: off, minimal, low, medium, high, xhigh
EOF
`), 0o700))

	snapshot, err := ReadPiModelDiscoveryFromHelp(context.Background(), script)
	require.NoError(t, err)
	require.Equal(t, []string{"gemini-2.5-flash"}, snapshot.Models)
	require.Contains(t, snapshot.ReasoningLevels, "high")
	require.Equal(t, "cli-help:pi", snapshot.Source)
}

func TestReadPiModelDiscoveryFromListModels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed helper requires Unix script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
cat <<'EOF'
provider           model             context  max-out  thinking  images
google-gemini-cli  gemini-2.5-flash  1.0M     65.5K    yes       yes
google-gemini-cli  gemini-2.5-pro    1.0M     65.5K    yes       yes
EOF
`), 0o700))

	snapshot, err := ReadPiModelDiscoveryFromListModels(context.Background(), script)
	require.NoError(t, err)
	require.Equal(t, []string{"gemini-2.5-flash", "gemini-2.5-pro"}, snapshot.Models)
	require.Contains(t, snapshot.ReasoningLevels, "xhigh")
	require.Equal(t, "cli:list-models", snapshot.Source)
}

func TestReadPiModelDiscoveryFromListModelsRejectsEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed helper requires Unix script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf 'provider model context max-out thinking images\n'
`), 0o700))

	_, err := ReadPiModelDiscoveryFromListModels(context.Background(), script)
	require.Error(t, err)
}
