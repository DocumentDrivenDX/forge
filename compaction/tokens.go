// Package compaction provides conversation compaction for the agent loop.
// When the conversation history approaches the model's context window limit,
// compaction summarizes older messages and replaces them with a structured summary.
package compaction

import (
	"encoding/json"

	"github.com/DocumentDrivenDX/agent"
)

const (
	// charsPerToken is the conservative heuristic for token estimation.
	// Overestimates slightly, which is safer for compaction trigger timing.
	charsPerToken = 4

	// imageTokenEstimate is the fixed token estimate for image content.
	// Based on pi's 4800 chars / 4 = 1200 tokens.
	imageTokenEstimate = 1200
)

// EstimateTokens estimates the token count for a string using chars/4.
func EstimateTokens(s string) int {
	n := len(s)
	return (n + charsPerToken - 1) / charsPerToken // ceiling division
}

// EstimateMessageTokens estimates the token count for a single message,
// including role, content, tool calls, and tool call arguments.
func EstimateMessageTokens(msg agent.Message) int {
	tokens := EstimateTokens(string(msg.Role))
	tokens += EstimateTokens(msg.Content)
	for _, tc := range msg.ToolCalls {
		tokens += EstimateTokens(tc.Name)
		tokens += EstimateTokens(string(tc.Arguments))
	}
	if msg.ToolCallID != "" {
		tokens += EstimateTokens(msg.ToolCallID)
	}
	return tokens
}

// EstimateConversationTokens estimates the total tokens for a slice of messages.
func EstimateConversationTokens(messages []agent.Message) int {
	total := 0
	for _, msg := range messages {
		total += EstimateMessageTokens(msg)
	}
	return total
}

// EffectiveTokenCount computes the effective context consumption from
// provider-reported token usage. Includes all four components since they
// all contribute to context window consumption.
func EffectiveTokenCount(usage agent.TokenUsage) int {
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// ShouldCompact returns true if the conversation should be compacted.
// effectiveWindow = contextWindow * effectivePercent / 100.
func ShouldCompact(estimatedTokens, contextWindow, effectivePercent, reserveTokens int) bool {
	if contextWindow <= 0 || effectivePercent <= 0 {
		return false
	}
	effectiveWindow := contextWindow * effectivePercent / 100
	threshold := effectiveWindow - reserveTokens
	if threshold <= 0 {
		return false
	}
	return estimatedTokens > threshold
}

// TruncateToolResult truncates a tool result string to maxChars,
// appending a truncation marker if shortened.
func TruncateToolResult(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	remaining := len(s) - maxChars
	return s[:maxChars] + "\n[... " + formatInt(remaining) + " more characters truncated]"
}

func formatInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
