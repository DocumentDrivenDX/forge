package opencode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultOpenCodeModelDiscovery(t *testing.T) {
	snapshot := DefaultOpenCodeModelDiscovery()
	if snapshot.Source != "compatibility-table:opencode-cli" {
		t.Fatalf("Source = %q, want compatibility-table:opencode-cli", snapshot.Source)
	}
	if len(snapshot.Models) == 0 {
		t.Fatal("default discovery should include compatibility-table model IDs")
	}
	assertContainsString(t, snapshot.ReasoningLevels, "high", "reasoning")
	if snapshot.FreshnessWindow != OpenCodeModelDiscoveryFreshnessWindow.String() {
		t.Fatalf("FreshnessWindow = %q, want %q", snapshot.FreshnessWindow, OpenCodeModelDiscoveryFreshnessWindow.String())
	}
}

func TestParseOpenCodeModels(t *testing.T) {
	input := `
opencode/gpt-5.4
opencode/claude-sonnet-4-6
opencode/gpt-5.4
lm-studio/*
Name Provider Context
`
	models := parseOpenCodeModels(input)
	want := []string{"opencode/gpt-5.4", "opencode/claude-sonnet-4-6", "lm-studio/*"}
	if len(models) != len(want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models = %#v, want %#v", models, want)
		}
	}
}

func TestReadOpenCodeModelDiscovery(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-opencode")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
cat <<'EOF'
opencode/gpt-5.4
opencode/claude-sonnet-4-6
EOF
`), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot, err := ReadOpenCodeModelDiscovery(ctx, script)
	if err != nil {
		t.Fatalf("ReadOpenCodeModelDiscovery: %v", err)
	}
	if snapshot.Source != "cli:opencode models" {
		t.Fatalf("Source = %q, want cli:opencode models", snapshot.Source)
	}
	assertContainsString(t, snapshot.Models, "opencode/gpt-5.4", "models")
	assertContainsString(t, snapshot.Models, "opencode/claude-sonnet-4-6", "models")
	assertContainsString(t, snapshot.ReasoningLevels, "max", "reasoning")
}

func TestReadOpenCodeModelDiscoveryRejectsEmptyOutput(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-opencode")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
printf 'no models here\n'
`), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := ReadOpenCodeModelDiscovery(ctx, script); err == nil {
		t.Fatal("expected empty model output to fail discovery")
	}
}

func assertContainsString(t *testing.T, values []string, want, label string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%s missing %q in %#v", label, want, values)
}
