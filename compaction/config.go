package compaction

import (
	"strings"

	"github.com/DocumentDrivenDX/agent"
)

// DefaultContextWindow is used when no context window is configured.
const DefaultContextWindow = 8192

// DefaultReserveTokens is the token budget reserved for the model response.
const DefaultReserveTokens = 8192

// DefaultKeepRecentTokens is how many tokens of recent messages to keep verbatim.
const DefaultKeepRecentTokens = 8192

// DefaultMaxToolResultChars is the max chars per tool result in summarization input.
const DefaultMaxToolResultChars = 2000

// DefaultEffectivePercent is the safety margin on the context window.
const DefaultEffectivePercent = 95

// Config configures automatic conversation compaction.
type Config struct {
	// Enabled controls whether automatic compaction runs. Default: true.
	Enabled bool

	// ContextWindow is the model's context window in tokens.
	ContextWindow int

	// ReserveTokens is the budget reserved for the model response.
	ReserveTokens int

	// KeepRecentTokens is how many tokens of recent messages to keep verbatim.
	KeepRecentTokens int

	// MaxToolResultChars is the max chars per tool result in summarization input.
	MaxToolResultChars int

	// EffectivePercent is the percentage of ContextWindow to actually use (0-100).
	EffectivePercent int

	// SummarizationModel overrides the model used for summarization.
	SummarizationModel string

	// SummarizationProvider overrides the provider for summarization.
	SummarizationProvider agent.Provider

	// SummarizationFocus is optional text appended to the summarization prompt.
	SummarizationFocus string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		ContextWindow:      DefaultContextWindow,
		ReserveTokens:      DefaultReserveTokens,
		KeepRecentTokens:   DefaultKeepRecentTokens,
		MaxToolResultChars: DefaultMaxToolResultChars,
		EffectivePercent:   DefaultEffectivePercent,
	}
}

// FindCutPoint walks backwards from the end of messages, accumulating token
// estimates, and returns the index of the first message to keep. Messages
// before this index will be summarized. The cut is always at a valid turn
// boundary — never between a tool call and its result.
func FindCutPoint(messages []agent.Message, keepRecentTokens int) int {
	if len(messages) == 0 {
		return 0
	}

	accumulated := 0
	cutIndex := 0

	for i := len(messages) - 1; i >= 0; i-- {
		tokens := EstimateMessageTokens(messages[i])
		accumulated += tokens

		if accumulated >= keepRecentTokens {
			// Find a valid cut point at or after this index
			cutIndex = findValidBoundary(messages, i)
			break
		}
	}

	return cutIndex
}

// findValidBoundary scans forward from index to find a valid turn boundary.
// Valid boundaries: user messages, assistant messages (without pending tool results).
// Tool result messages are NOT valid cut points.
func findValidBoundary(messages []agent.Message, index int) int {
	for i := index; i < len(messages); i++ {
		msg := messages[i]
		switch msg.Role {
		case agent.RoleUser:
			return i
		case agent.RoleAssistant:
			// Only valid if this isn't followed by tool results that belong to it
			if len(msg.ToolCalls) == 0 {
				return i
			}
			// If it has tool calls, the cut should be before this assistant message
			// (keep the tool calls and their results together)
			continue
		case agent.RoleTool:
			// Never cut here — tool results must follow their call
			continue
		case agent.RoleSystem:
			// System messages are always at the start, valid boundary
			return i
		}
	}
	// If no valid boundary found, keep everything
	return len(messages)
}

// IsCompactionSummary checks if a message is a compaction summary injection.
func IsCompactionSummary(msg agent.Message) bool {
	return msg.Role == agent.RoleUser &&
		len(msg.Content) > 50 &&
		(strings.Contains(msg.Content, "<summary>") || strings.Contains(msg.Content, "compacted into the following summary"))
}
