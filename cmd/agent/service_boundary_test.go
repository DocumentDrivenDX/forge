package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIServiceContractUsesTypedEventDecoder(t *testing.T) {
	root := repoRootForBoundaryTest(t)
	data, err := os.ReadFile(filepath.Join(root, "cmd", "agent", "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "agent.DecodeServiceEvent(ev)") {
		t.Fatal("CLI service-contract path must use agent.DecodeServiceEvent")
	}
	if strings.Contains(src, "json.Unmarshal(ev.Data") {
		t.Fatal("CLI must not redefine private ServiceEvent payload shapes by unmarshalling ev.Data directly")
	}
}

func TestCLIInternalImportBoundaryAllowlist(t *testing.T) {
	root := repoRootForBoundaryTest(t)
	entries, err := filepath.Glob(filepath.Join(root, "cmd", "ddx-agent", "*.go"))
	if err != nil {
		t.Fatalf("glob cmd files: %v", err)
	}
	approved := []string{
		"github.com/DocumentDrivenDX/agent/internal/compaction",
		"github.com/DocumentDrivenDX/agent/internal/config",
		"github.com/DocumentDrivenDX/agent/internal/core",
		"github.com/DocumentDrivenDX/agent/internal/modelcatalog",
		"github.com/DocumentDrivenDX/agent/internal/observations",
		"github.com/DocumentDrivenDX/agent/internal/prompt",
		"github.com/DocumentDrivenDX/agent/internal/provider/openai",
		"github.com/DocumentDrivenDX/agent/internal/reasoning",
		"github.com/DocumentDrivenDX/agent/internal/safefs",
		"github.com/DocumentDrivenDX/agent/internal/session",
		"github.com/DocumentDrivenDX/agent/internal/tool",
	}
	for _, path := range entries {
		if filepath.Base(path) == "service_boundary_test.go" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(data)
		if !strings.Contains(src, "/internal/") {
			continue
		}
		for _, line := range strings.Split(src, "\n") {
			if !strings.Contains(line, "github.com/DocumentDrivenDX/agent/internal/") {
				continue
			}
			ok := false
			for _, prefix := range approved {
				if strings.Contains(line, prefix) {
					ok = true
					break
				}
			}
			if !ok {
				t.Fatalf("unapproved internal import in %s: %s", path, strings.TrimSpace(line))
			}
		}
	}
}

func repoRootForBoundaryTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
