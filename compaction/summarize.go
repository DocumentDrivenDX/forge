package compaction

import (
	"context"
	"fmt"
	"strings"

	"github.com/DocumentDrivenDX/agent"
)

// Summarization system prompt — prevents the model from continuing the conversation.
const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI coding assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// InitialSummarizationPrompt is the user-side prompt for first-time compaction.
const InitialSummarizationPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a structured handoff summary for another LLM that will resume this task.

Use this EXACT format:

## Goal
[What the user is trying to accomplish]

## Constraints & Preferences
- [Requirements, conventions, or preferences mentioned]

## Progress
### Done
- [x] [Completed work with file paths]

### In Progress
- [ ] [Current work]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next]

## Critical Context
- [Data, error messages, or references needed to continue]

### Blocked
- [Issues preventing progress, if any, or "(none)"]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// UpdateSummarizationPrompt is the user-side prompt when merging with a previous summary.
const UpdateSummarizationPrompt = `The messages above are NEW conversation since the last compaction.
Update the existing summary by merging new information.

RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context
- UPDATE Progress: move completed items from In Progress to Done
- UPDATE Next Steps based on what was accomplished
- PRESERVE exact file paths and error messages
- If something is no longer relevant, you may remove it`

// SummaryInjectionPrefix is prepended to the summary when injecting into conversation.
const SummaryInjectionPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"

// SummaryInjectionSuffix closes the summary injection.
const SummaryInjectionSuffix = "\n</summary>"

// NoSummaryFallback is used when the LLM returns an empty summary.
const NoSummaryFallback = "(no summary available)"

// CompactionResult holds the output of a compaction pass.
type CompactionResult struct {
	Summary      string   // the generated summary text
	FileOps      *FileOps // accumulated file operations
	TokensBefore int      // estimated tokens before compaction
	TokensAfter  int      // estimated tokens after compaction
	CutIndex     int      // index of first kept message
	MessagesKept int      // number of messages preserved verbatim
	Warning      string   // degradation warning, if any
}

// Summarize runs the summarization LLM call and returns the summary text.
// It serializes the messages, builds the prompt, calls the provider, and
// appends file tracking XML.
func Summarize(
	ctx context.Context,
	provider agent.Provider,
	messages []agent.Message,
	toolCalls []agent.ToolCallLog,
	previousSummary string,
	cfg Config,
) (string, *FileOps, error) {
	// Serialize the conversation
	serialized := SerializeConversation(messages, cfg.MaxToolResultChars)

	// Build the prompt
	var promptBuilder strings.Builder
	promptBuilder.WriteString("<conversation>\n")
	promptBuilder.WriteString(serialized)
	promptBuilder.WriteString("\n</conversation>\n\n")

	if previousSummary != "" {
		promptBuilder.WriteString("<previous-summary>\n")
		promptBuilder.WriteString(previousSummary)
		promptBuilder.WriteString("\n</previous-summary>\n\n")
		promptBuilder.WriteString(UpdateSummarizationPrompt)
	} else {
		promptBuilder.WriteString(InitialSummarizationPrompt)
	}

	if cfg.SummarizationFocus != "" {
		promptBuilder.WriteString("\n\nAdditional focus: ")
		promptBuilder.WriteString(cfg.SummarizationFocus)
	}

	// Calculate max tokens for the summary response
	maxTokens := cfg.ReserveTokens * 4 / 5 // 0.8 * ReserveTokens
	if maxTokens < 512 {
		maxTokens = 512
	}

	// Use summarization model if configured
	model := cfg.SummarizationModel
	p := provider
	if cfg.SummarizationProvider != nil {
		p = cfg.SummarizationProvider
	}

	// Call the LLM
	resp, err := p.Chat(ctx, []agent.Message{
		{Role: agent.RoleSystem, Content: SummarizationSystemPrompt},
		{Role: agent.RoleUser, Content: promptBuilder.String()},
	}, nil, agent.Options{
		Model:     model,
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", nil, fmt.Errorf("compaction: summarization failed: %w", err)
	}

	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		summary = NoSummaryFallback
	}

	// Track file operations
	ops := ExtractFileOps(toolCalls)
	summary += ops.FormatXML()

	return summary, ops, nil
}

// InjectSummary creates a user message containing the compaction summary.
func InjectSummary(summary string) agent.Message {
	return agent.Message{
		Role:    agent.RoleUser,
		Content: SummaryInjectionPrefix + summary + SummaryInjectionSuffix,
	}
}

// CompactMessages performs a full compaction pass on a message history.
// Returns the new message list with older messages replaced by a summary.
func CompactMessages(
	ctx context.Context,
	provider agent.Provider,
	messages []agent.Message,
	toolCalls []agent.ToolCallLog,
	previousSummary string,
	previousFileOps *FileOps,
	cfg Config,
) ([]agent.Message, *CompactionResult, error) {
	tokensBefore := EstimateConversationTokens(messages)

	// Find cut point
	cutIndex := FindCutPoint(messages, cfg.KeepRecentTokens)
	if cutIndex == 0 {
		// Nothing to compact
		return messages, nil, nil
	}

	// Filter out previous compaction summaries from messages-to-summarize
	var toSummarize []agent.Message
	for _, msg := range messages[:cutIndex] {
		if !IsCompactionSummary(msg) {
			toSummarize = append(toSummarize, msg)
		}
	}

	// Run summarization
	summary, ops, err := Summarize(ctx, provider, toSummarize, toolCalls, previousSummary, cfg)
	if err != nil {
		return messages, nil, err
	}

	// Merge previous file ops
	if previousFileOps != nil {
		ops.Merge(previousFileOps)
	}

	// Build new message list: kept messages + summary LAST (per SD-006 for prompt cache optimization)
	var newMessages []agent.Message
	newMessages = append(newMessages, messages[cutIndex:]...)
	newMessages = append(newMessages, InjectSummary(summary))

	tokensAfter := EstimateConversationTokens(newMessages)

	result := &CompactionResult{
		Summary:      summary,
		FileOps:      ops,
		TokensBefore: tokensBefore,
		TokensAfter:  tokensAfter,
		CutIndex:     cutIndex,
		MessagesKept: len(messages) - cutIndex,
		Warning:      "Long conversations and multiple compactions can cause the model to be less accurate. Consider starting a new session when possible.",
	}

	return newMessages, result, nil
}
