package opencode

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

const OpenCodeModelDiscoveryFreshnessWindow = 24 * time.Hour

var opencodeModelLinePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[^\s]+$`)

func DefaultOpenCodeModelDiscovery() harnesses.ModelDiscoverySnapshot {
	return harnesses.ModelDiscoverySnapshot{
		CapturedAt:      time.Now().UTC(),
		Models:          []string{"opencode/gpt-5.4", "opencode/claude-sonnet-4-6"},
		ReasoningLevels: []string{"minimal", "low", "medium", "high", "max"},
		Source:          "compatibility-table:opencode-cli",
		FreshnessWindow: OpenCodeModelDiscoveryFreshnessWindow.String(),
		Detail:          "opencode models lists provider/model IDs; opencode run --help documents -m/--model and --variant",
	}
}

func ReadOpenCodeModelDiscovery(ctx context.Context, binary string, args ...string) (harnesses.ModelDiscoverySnapshot, error) {
	if binary == "" {
		binary = "opencode"
	}
	if len(args) == 0 {
		args = []string{"models"}
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return harnesses.ModelDiscoverySnapshot{}, fmt.Errorf("opencode models: %w", err)
	}
	models := parseOpenCodeModels(string(out))
	if len(models) == 0 {
		return harnesses.ModelDiscoverySnapshot{}, fmt.Errorf("opencode models returned no provider/model IDs")
	}
	snapshot := DefaultOpenCodeModelDiscovery()
	snapshot.Source = "cli:opencode models"
	snapshot.Models = models
	return snapshot, nil
}

func opencodeDiscoveryFromText(text, source string) harnesses.ModelDiscoverySnapshot {
	snapshot := DefaultOpenCodeModelDiscovery()
	if source != "" {
		snapshot.Source = source
	}
	if models := parseOpenCodeModels(text); len(models) > 0 {
		snapshot.Models = models
	}
	return snapshot
}

func parseOpenCodeModels(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	out := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 1 {
			continue
		}
		model := fields[0]
		if !opencodeModelLinePattern.MatchString(model) {
			continue
		}
		out = appendUniqueString(out, model)
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
