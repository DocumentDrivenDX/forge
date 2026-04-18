package gemini

import (
	"encoding/json"
	"strings"
)

// streamAggregate captures usage extracted from gemini output.
type streamAggregate struct {
	FinalText    string
	InputTokens  int
	OutputTokens int
}

// geminiStatsEnvelope is a minimal view of the gemini JSON stats block.
// From DDx ExtractUsage("gemini"):
//
//	{"stats":{"models":{"<model>":{"tokens":{"input":N,"total":M}}}}}
//
// output_tokens = total - input.
type geminiStatsEnvelope struct {
	Stats struct {
		Models map[string]struct {
			Tokens struct {
				Input int `json:"input"`
				Total int `json:"total"`
			} `json:"tokens"`
		} `json:"models"`
	} `json:"stats"`
}

// parseGeminiUsage extracts token usage from the last non-empty line of
// gemini output that is a valid JSON stats envelope.
// Returns a streamAggregate with FinalText = full output.
func parseGeminiUsage(output string) *streamAggregate {
	agg := &streamAggregate{FinalText: output}

	// Try last non-empty line (gemini may emit stats on the last line).
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = lines[i]
			break
		}
	}
	if last == "" {
		return agg
	}

	var env geminiStatsEnvelope
	if err := json.Unmarshal([]byte(last), &env); err != nil {
		return agg
	}

	for _, model := range env.Stats.Models {
		agg.InputTokens += model.Tokens.Input
		agg.OutputTokens += model.Tokens.Total - model.Tokens.Input
	}
	return agg
}
