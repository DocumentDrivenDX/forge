package gemini

import (
	"regexp"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

const GeminiModelDiscoveryFreshnessWindow = 24 * time.Hour

var (
	geminiANSISequencePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	geminiModelPattern        = regexp.MustCompile(`\bgemini-[A-Za-z0-9][A-Za-z0-9._-]*\b`)
)

// ModelDiscoveryFromText extracts Gemini model IDs from caller-provided CLI
// output without assuming a current default model list.
func ModelDiscoveryFromText(text, source string) harnesses.ModelDiscoverySnapshot {
	if source == "" {
		source = "cli-output:gemini"
	}
	return harnesses.ModelDiscoverySnapshot{
		CapturedAt:      time.Now().UTC(),
		Models:          parseGeminiModels(text),
		ReasoningLevels: nil,
		Source:          source,
		FreshnessWindow: GeminiModelDiscoveryFreshnessWindow.String(),
		Detail:          "gemini CLI harness does not expose DDx permission or reasoning controls; model IDs are extracted only from supplied CLI output",
	}
}

func parseGeminiModels(text string) []string {
	text = geminiANSISequencePattern.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	return uniqueGeminiStrings(geminiModelPattern.FindAllString(text, -1))
}

func uniqueGeminiStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
