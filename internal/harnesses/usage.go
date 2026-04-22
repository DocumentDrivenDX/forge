package harnesses

import (
	"encoding/json"
	"fmt"
	"sort"
)

const (
	UsageSourceNativeStream     = "native_stream"
	UsageSourceNativeTokenCount = "native_token_count"
	UsageSourceTranscript       = "transcript"
	UsageSourceStatusOutput     = "status_output"
	UsageSourceFallback         = "fallback"

	UsageWarningMalformed    = "usage_malformed"
	UsageWarningDisagreement = "usage_source_disagreement"
)

// UsageCandidate is one candidate source considered for final token usage.
type UsageCandidate struct {
	Source     string
	Fresh      *bool
	CapturedAt string
	Counts     UsageTokenCounts
	Warning    string
}

// ResolveFinalUsage applies the documented source precedence:
// native_stream > transcript > status_output > fallback. It returns nil usage
// when no source reported a token count, while still returning warnings for
// malformed sources or source disagreements.
func ResolveFinalUsage(candidates []UsageCandidate) (*FinalUsage, []FinalWarning) {
	if len(candidates) == 0 {
		return nil, nil
	}
	ordered := append([]UsageCandidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return usageSourceRank(ordered[i].Source) < usageSourceRank(ordered[j].Source)
	})

	var warnings []FinalWarning
	var valid []UsageCandidate
	validBySource := map[string]int{}
	for _, candidate := range ordered {
		if candidate.Counts.Any() {
			if idx, ok := validBySource[candidate.Source]; ok {
				valid[idx] = candidate
			} else {
				validBySource[candidate.Source] = len(valid)
				valid = append(valid, candidate)
			}
		}
		if candidate.Warning != "" {
			warnings = append(warnings, FinalWarning{
				Code:    UsageWarningMalformed,
				Message: candidate.Warning,
				Sources: []UsageSourceEvidence{
					usageEvidence(candidate),
				},
			})
		}
	}
	if len(valid) == 0 {
		return nil, warnings
	}

	chosen := valid[0]
	for i := 1; i < len(valid); i++ {
		if usageCountsDisagree(chosen.Counts, valid[i].Counts) {
			warnings = append(warnings, FinalWarning{
				Code:    UsageWarningDisagreement,
				Message: "token usage sources disagree; selected source by documented precedence",
				Sources: []UsageSourceEvidence{
					usageEvidence(chosen),
					usageEvidence(valid[i]),
				},
			})
		}
	}

	usage := &FinalUsage{
		InputTokens:      chosen.Counts.InputTokens,
		OutputTokens:     chosen.Counts.OutputTokens,
		CacheReadTokens:  chosen.Counts.CacheReadTokens,
		CacheWriteTokens: chosen.Counts.CacheWriteTokens,
		CacheTokens:      chosen.Counts.CacheTokens,
		ReasoningTokens:  chosen.Counts.ReasoningTokens,
		TotalTokens:      chosen.Counts.TotalTokens,
		Source:           chosen.Source,
		Fresh:            chosen.Fresh,
		CapturedAt:       chosen.CapturedAt,
		Sources:          make([]UsageSourceEvidence, 0, len(valid)),
	}
	for _, candidate := range valid {
		usage.Sources = append(usage.Sources, usageEvidence(candidate))
	}
	return usage, warnings
}

func usageSourceRank(source string) int {
	switch source {
	case UsageSourceNativeStream:
		return 0
	case UsageSourceNativeTokenCount:
		return 1
	case UsageSourceTranscript:
		return 2
	case UsageSourceStatusOutput:
		return 3
	case UsageSourceFallback:
		return 4
	default:
		return 100
	}
}

func usageEvidence(candidate UsageCandidate) UsageSourceEvidence {
	counts := candidate.Counts
	var usage *UsageTokenCounts
	if counts.Any() {
		usage = &counts
	}
	return UsageSourceEvidence{
		Source:     candidate.Source,
		Fresh:      candidate.Fresh,
		CapturedAt: candidate.CapturedAt,
		Usage:      usage,
		Warning:    candidate.Warning,
	}
}

func usageCountsDisagree(a, b UsageTokenCounts) bool {
	return ptrIntDisagree(a.InputTokens, b.InputTokens) ||
		ptrIntDisagree(a.OutputTokens, b.OutputTokens) ||
		ptrIntDisagree(a.CacheReadTokens, b.CacheReadTokens) ||
		ptrIntDisagree(a.CacheWriteTokens, b.CacheWriteTokens) ||
		ptrIntDisagree(a.CacheTokens, b.CacheTokens) ||
		ptrIntDisagree(a.ReasoningTokens, b.ReasoningTokens) ||
		ptrIntDisagree(a.TotalTokens, b.TotalTokens)
}

func ptrIntDisagree(a, b *int) bool {
	return a != nil && b != nil && *a != *b
}

// ParseUsageJSON normalizes common Claude/Codex/OpenAI-style usage objects.
// Unknown dimensions remain nil. A present zero remains present.
func ParseUsageJSON(raw json.RawMessage) (UsageTokenCounts, error) {
	var counts UsageTokenCounts
	if len(raw) == 0 || string(raw) == "null" {
		return counts, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return counts, fmt.Errorf("usage object is not JSON: %w", err)
	}
	if obj == nil {
		return counts, nil
	}

	var err error
	if counts.InputTokens, err = firstInt(obj, "input_tokens", "prompt_tokens", "input"); err != nil {
		return counts, err
	}
	if counts.OutputTokens, err = firstInt(obj, "output_tokens", "completion_tokens", "output"); err != nil {
		return counts, err
	}
	if counts.CacheReadTokens, err = firstInt(obj, "cache_read_input_tokens", "cache_read_tokens", "cached_input_tokens"); err != nil {
		return counts, err
	}
	if counts.CacheWriteTokens, err = firstInt(obj, "cache_creation_input_tokens", "cache_write_tokens"); err != nil {
		return counts, err
	}
	if counts.CacheTokens, err = firstInt(obj, "cache_tokens", "cached_tokens"); err != nil {
		return counts, err
	}
	if counts.ReasoningTokens, err = firstInt(obj, "reasoning_tokens", "reasoning_output_tokens"); err != nil {
		return counts, err
	}
	if counts.TotalTokens, err = firstInt(obj, "total_tokens", "total"); err != nil {
		return counts, err
	}

	if details, ok := rawObject(obj, "prompt_tokens_details"); ok && counts.CacheReadTokens == nil {
		if counts.CacheReadTokens, err = firstInt(details, "cached_tokens"); err != nil {
			return counts, err
		}
	}
	if details, ok := rawObject(obj, "completion_tokens_details"); ok && counts.ReasoningTokens == nil {
		if counts.ReasoningTokens, err = firstInt(details, "reasoning_tokens"); err != nil {
			return counts, err
		}
	}
	if details, ok := rawObject(obj, "output_tokens_details"); ok && counts.ReasoningTokens == nil {
		if counts.ReasoningTokens, err = firstInt(details, "reasoning_tokens"); err != nil {
			return counts, err
		}
	}

	if counts.TotalTokens == nil && counts.InputTokens != nil && counts.OutputTokens != nil {
		total := *counts.InputTokens + *counts.OutputTokens
		counts.TotalTokens = &total
	}
	if counts.CacheTokens == nil && (counts.CacheReadTokens != nil || counts.CacheWriteTokens != nil) {
		total := 0
		if counts.CacheReadTokens != nil {
			total += *counts.CacheReadTokens
		}
		if counts.CacheWriteTokens != nil {
			total += *counts.CacheWriteTokens
		}
		counts.CacheTokens = &total
	}
	return counts, nil
}

func firstInt(obj map[string]json.RawMessage, names ...string) (*int, error) {
	for _, name := range names {
		raw, ok := obj[name]
		if !ok {
			continue
		}
		var n int
		if err := json.Unmarshal(raw, &n); err == nil {
			return &n, nil
		}
		var f float64
		if err := json.Unmarshal(raw, &f); err == nil {
			n = int(f)
			return &n, nil
		}
		return nil, fmt.Errorf("usage field %q is not numeric", name)
	}
	return nil, nil
}

func rawObject(obj map[string]json.RawMessage, name string) (map[string]json.RawMessage, bool) {
	raw, ok := obj[name]
	if !ok {
		return nil, false
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil {
		return nil, false
	}
	return nested, true
}
