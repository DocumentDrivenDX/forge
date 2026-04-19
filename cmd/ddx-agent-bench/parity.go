package main

import (
	"encoding/json"
	"strings"

	"github.com/DocumentDrivenDX/agent/internal/comparison"
)

// normalizeInput canonicalizes a JSON tool-input string for comparison:
// unmarshal + marshal to strip insignificant whitespace/key-order differences.
// Falls back to trimmed raw string if the input is not valid JSON.
func normalizeInput(input string) string {
	var v interface{}
	if err := json.Unmarshal([]byte(input), &v); err == nil {
		b, err := json.Marshal(v)
		if err == nil {
			return string(b)
		}
	}
	return strings.TrimSpace(input)
}

// toolCallSeq reduces a []ToolCallEntry to a comparable slice of
// "(tool, normalizedArgs)" pairs.
type toolCallPair struct {
	Tool  string
	Input string
}

func toToolCallSeq(entries []comparison.ToolCallEntry) []toolCallPair {
	seq := make([]toolCallPair, len(entries))
	for i, e := range entries {
		seq[i] = toolCallPair{
			Tool:  e.Tool,
			Input: normalizeInput(e.Input),
		}
	}
	return seq
}

// ToolCallSeqEqual returns true when two RunResults have identical
// (tool_name, normalised_args) sequences.
func ToolCallSeqEqual(a, b comparison.RunResult) bool {
	sa := toToolCallSeq(a.ToolCalls)
	sb := toToolCallSeq(b.ToolCalls)
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// LevenshteinRatio returns a [0,1] similarity score between two strings
// (1.0 = identical, 0.0 = completely different) using Levenshtein distance.
// Operates on runes so multi-byte characters are handled correctly.
func LevenshteinRatio(a, b string) float64 {
	ra := []rune(a)
	rb := []rune(b)
	la := len(ra)
	lb := len(rb)
	if la == 0 && lb == 0 {
		return 1.0
	}
	maxLen := la + lb
	if maxLen == 0 {
		return 1.0
	}
	dist := levenshteinDistance(ra, rb)
	return 1.0 - float64(dist)/float64(maxLen)
}

func levenshteinDistance(a, b []rune) int {
	la, lb := len(a), len(b)
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1]
			} else {
				m := prev[j-1]
				if prev[j] < m {
					m = prev[j]
				}
				if curr[j-1] < m {
					m = curr[j-1]
				}
				curr[j] = m + 1
			}
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// ParityCell captures the parity analysis between two arms for one task.
type ParityCell struct {
	ArmA            string  `json:"arm_a"`
	ArmB            string  `json:"arm_b"`
	ToolSeqEqual    bool    `json:"tool_seq_equal"`
	OutputSimilarity float64 `json:"output_similarity"`
}

// BuildParityMatrix computes parity cells for all pairs of arms in a
// single comparison record. Labels map arm index → arm label.
func BuildParityMatrix(record comparison.ComparisonRecord, labels []string) []ParityCell {
	arms := record.Arms
	n := len(arms)
	var cells []ParityCell
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			la := labelFor(i, labels, arms[i].Harness)
			lb := labelFor(j, labels, arms[j].Harness)

			ra := comparison.RunResult{
				ToolCalls: arms[i].ToolCalls,
				Output:    arms[i].Output,
			}
			rb := comparison.RunResult{
				ToolCalls: arms[j].ToolCalls,
				Output:    arms[j].Output,
			}

			sim := LevenshteinRatio(
				truncateRunes(ra.Output, 2000),
				truncateRunes(rb.Output, 2000),
			)

			cells = append(cells, ParityCell{
				ArmA:            la,
				ArmB:            lb,
				ToolSeqEqual:    ToolCallSeqEqual(ra, rb),
				OutputSimilarity: sim,
			})
		}
	}
	return cells
}

func labelFor(idx int, labels []string, fallback string) string {
	if idx < len(labels) && labels[idx] != "" {
		return labels[idx]
	}
	return fallback
}

// truncateRunes truncates s to at most n runes for similarity comparison so
// very long outputs don't make Levenshtein quadratic.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
