package compaction

import (
	"context"
	"errors"
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
	maxTokens int,
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

	// Calculate max tokens for the summary response.
	summaryMaxTokens := cfg.ReserveTokens * 4 / 5 // 0.8 * ReserveTokens
	if summaryMaxTokens < 512 {
		summaryMaxTokens = 512
	}
	if maxTokens > 0 && maxTokens < summaryMaxTokens {
		summaryMaxTokens = maxTokens
	}
	if summaryMaxTokens < 1 {
		summaryMaxTokens = 1
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
		MaxTokens: summaryMaxTokens,
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

// recoverCompactionStateFromHistory reconstructs the latest compaction state
// from carried history. This lets resumed runs continue compaction even when
// they use a fresh compactor instance.
func recoverCompactionStateFromHistory(messages []agent.Message) (string, *FileOps) {
	for i := len(messages) - 1; i >= 0; i-- {
		if !IsCompactionSummary(messages[i]) {
			continue
		}

		summary, ops, ok := parseSummaryInjection(messages[i].Content)
		if !ok {
			return "", nil
		}
		return summary, ops
	}

	return "", nil
}

// parseSummaryInjection removes the wrapper around a compacted summary and
// extracts any serialized file-op XML that was appended to it.
func parseSummaryInjection(content string) (string, *FileOps, bool) {
	if !strings.HasPrefix(content, SummaryInjectionPrefix) || !strings.HasSuffix(content, SummaryInjectionSuffix) {
		return "", nil, false
	}

	inner := strings.TrimPrefix(content, SummaryInjectionPrefix)
	inner = strings.TrimSuffix(inner, SummaryInjectionSuffix)
	summary, ops := splitSummaryAndFileOps(inner)
	return summary, ops, true
}

// splitSummaryAndFileOps separates the human-readable summary text from the
// XML file-op suffix appended by Summarize.
func splitSummaryAndFileOps(summary string) (string, *FileOps) {
	boundary := len(summary)
	if idx := strings.Index(summary, "\n\n<read-files>"); idx >= 0 && idx < boundary {
		boundary = idx
	}
	if idx := strings.Index(summary, "\n\n<modified-files>"); idx >= 0 && idx < boundary {
		boundary = idx
	}

	if boundary == len(summary) {
		return summary, nil
	}

	body := summary[:boundary]
	ops := parseFileOpsXML(summary[boundary:])
	return body, ops
}

// parseFileOpsXML reconstructs a FileOps value from the XML suffix emitted by
// FileOps.FormatXML.
func parseFileOpsXML(xml string) *FileOps {
	ops := NewFileOps()
	seen := false

	if block, ok := extractTagBlock(xml, "read-files"); ok {
		seen = true
		for _, path := range strings.Split(block, "\n") {
			path = strings.TrimSpace(path)
			if path != "" {
				ops.Read[path] = true
			}
		}
	}
	if block, ok := extractTagBlock(xml, "modified-files"); ok {
		seen = true
		for _, path := range strings.Split(block, "\n") {
			path = strings.TrimSpace(path)
			if path != "" {
				ops.Modified[path] = true
			}
		}
	}

	if !seen || (len(ops.Read) == 0 && len(ops.Modified) == 0) {
		return nil
	}
	return ops
}

func extractTagBlock(xml, tag string) (string, bool) {
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"

	start := strings.Index(xml, startTag)
	if start < 0 {
		return "", false
	}
	start += len(startTag)

	end := strings.Index(xml[start:], endTag)
	if end < 0 {
		return "", false
	}

	return xml[start : start+end], true
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
	return compactMessages(ctx, provider, messages, toolCalls, previousSummary, previousFileOps, cfg, 0)
}

func compactMessages(
	ctx context.Context,
	provider agent.Provider,
	messages []agent.Message,
	toolCalls []agent.ToolCallLog,
	previousSummary string,
	previousFileOps *FileOps,
	cfg Config,
	prefixTokens int,
) ([]agent.Message, *CompactionResult, error) {
	if previousSummary == "" || previousFileOps == nil {
		recoveredSummary, recoveredOps := recoverCompactionStateFromHistory(messages)
		if previousSummary == "" {
			previousSummary = recoveredSummary
		}
		if previousFileOps == nil {
			previousFileOps = recoveredOps
		}
	}

	tokensBefore := EstimateConversationTokens(messages)
	effectiveWindow := cfg.ContextWindow*cfg.EffectivePercent/100 - cfg.ReserveTokens
	lastUserIndex := findLastUserMessageIndex(messages)

	cutIndex := FindCutPoint(messages, cfg.KeepRecentTokens)
	if cutIndex == 0 && len(messages) > 0 {
		cutIndex = nextValidBoundary(messages, 0)
	}
	if lastUserIndex >= 0 && cutIndex > lastUserIndex {
		cutIndex = lastUserIndex
	}
	if cutIndex <= 0 || cutIndex > len(messages) {
		return messages, nil, nil
	}

	var (
		bestMessages []agent.Message
		bestResult   *CompactionResult
		sawNoFit     bool
	)

	for {
		newMessages, result, err := compactAtCutIndex(ctx, provider, messages, toolCalls, previousSummary, previousFileOps, cfg, cutIndex, tokensBefore, prefixTokens, effectiveWindow)
		if err != nil {
			if !errors.Is(err, agent.ErrCompactionNoFit) {
				return messages, nil, err
			}
			sawNoFit = true
		}
		if result == nil {
			nextCut := nextValidBoundary(messages, cutIndex)
			if nextCut <= cutIndex || (lastUserIndex >= 0 && nextCut > lastUserIndex) {
				if sawNoFit {
					return messages, nil, agent.ErrCompactionNoFit
				}
				return messages, nil, nil
			}
			cutIndex = nextCut
			continue
		}

		bestMessages = newMessages
		bestResult = result

		if prefixTokens <= 0 || result.TokensAfter+prefixTokens <= effectiveWindow {
			return newMessages, result, nil
		}

		nextCut := nextValidBoundary(messages, cutIndex)
		if nextCut <= cutIndex || (lastUserIndex >= 0 && nextCut > lastUserIndex) {
			break
		}
		cutIndex = nextCut
	}

	if bestResult == nil {
		if sawNoFit {
			return messages, nil, agent.ErrCompactionNoFit
		}
		return messages, nil, nil
	}
	if prefixTokens > 0 && bestResult.TokensAfter+prefixTokens > effectiveWindow {
		return messages, nil, agent.ErrCompactionNoFit
	}
	return bestMessages, bestResult, nil
}

func compactAtCutIndex(
	ctx context.Context,
	provider agent.Provider,
	messages []agent.Message,
	toolCalls []agent.ToolCallLog,
	previousSummary string,
	previousFileOps *FileOps,
	cfg Config,
	cutIndex int,
	tokensBefore int,
	prefixTokens int,
	effectiveWindow int,
) ([]agent.Message, *CompactionResult, error) {
	// Filter out previous compaction summaries from messages-to-summarize
	var toSummarize []agent.Message
	for _, msg := range messages[:cutIndex] {
		if !IsCompactionSummary(msg) {
			toSummarize = append(toSummarize, msg)
		}
	}

	// When the effective window is positive, budget the injected summary against
	// the remaining post-prefix window. Keep the legacy unbounded path for
	// callers that intentionally use the default negative reserve window in
	// tests and other direct compaction entry points.
	summaryBudgetTokens := 0
	budgetingEnabled := effectiveWindow > 0
	if budgetingEnabled {
		keptTokens := EstimateConversationTokens(messages[cutIndex:])
		availableTokens := effectiveWindow - prefixTokens - keptTokens
		if availableTokens <= 0 {
			return nil, nil, agent.ErrCompactionNoFit
		}

		budgetedOps := ExtractFileOps(toolCalls)
		if previousFileOps != nil {
			budgetedOps.Merge(previousFileOps)
		}

		summaryXML := budgetedOps.FormatXML()
		contentBudgetTokens := availableTokens - EstimateTokens(string(agent.RoleUser))
		staticTokens := EstimateTokens(SummaryInjectionPrefix + summaryXML + SummaryInjectionSuffix)
		summaryBudgetTokens = contentBudgetTokens - staticTokens
		if summaryBudgetTokens < 0 {
			return nil, nil, agent.ErrCompactionNoFit
		}
	}

	// Run summarization with a bounded response budget so the injected summary can fit.
	summary, ops, err := Summarize(ctx, provider, toSummarize, toolCalls, previousSummary, cfg, summaryBudgetTokens)
	if err != nil {
		return messages, nil, err
	}

	summaryBody, _ := splitSummaryAndFileOps(summary)

	// Merge previous file ops
	if previousFileOps != nil {
		ops.Merge(previousFileOps)
	}

	if budgetingEnabled {
		availableTokens := effectiveWindow - prefixTokens - EstimateConversationTokens(messages[cutIndex:])
		trimmedSummary, ok := fitSummaryToBudget(summaryBody, ops.FormatXML(), availableTokens)
		if !ok {
			return nil, nil, agent.ErrCompactionNoFit
		}
		summary = trimmedSummary
	} else {
		summary = summaryBody + ops.FormatXML()
	}

	// Compute token budget available for tail user-message re-inclusion.
	// After fitting the summary and kept messages, any remaining window space
	// (up to cfg.UserMessageTailTokens) can be used to re-include real user
	// messages from the compacted section.
	var tailBudget int
	if cfg.UserMessageTailTokens > 0 {
		tailBudget = cfg.UserMessageTailTokens
		if budgetingEnabled {
			summaryTokens := EstimateMessageTokens(InjectSummary(summary))
			keptTokens := EstimateConversationTokens(messages[cutIndex:])
			leftover := effectiveWindow - prefixTokens - summaryTokens - keptTokens
			if leftover < tailBudget {
				tailBudget = leftover
			}
		}
	}

	// Collect real user messages from the compacted section for re-inclusion.
	tailMessages := collectUserMessageTail(messages[:cutIndex], tailBudget)

	// Build new message list: tail user messages (oldest first), then kept recent
	// messages, then summary LAST (per SD-006 for prompt cache optimization).
	var newMessages []agent.Message
	newMessages = append(newMessages, tailMessages...)
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

func fitSummaryToBudget(summaryBody, summaryXML string, availableTokens int) (string, bool) {
	contentBudgetTokens := availableTokens - EstimateTokens(string(agent.RoleUser))
	if contentBudgetTokens <= 0 {
		return "", false
	}

	staticTokens := EstimateTokens(SummaryInjectionPrefix + summaryXML + SummaryInjectionSuffix)
	bodyBudgetTokens := contentBudgetTokens - staticTokens
	if bodyBudgetTokens < 0 {
		return "", false
	}

	if EstimateTokens(summaryBody) > bodyBudgetTokens {
		maxChars := bodyBudgetTokens * charsPerToken
		if maxChars < 0 {
			return "", false
		}
		if len(summaryBody) > maxChars {
			summaryBody = summaryBody[:maxChars]
		}
	}

	return summaryBody + summaryXML, true
}

// collectUserMessageTail returns real user-role messages from the tail of
// messages, subject to a token budget. Messages are accumulated newest-to-oldest
// and returned in chronological order (oldest first). A message that would exceed
// the remaining budget is truncated rather than dropped entirely.
//
// Excluded: system messages, compaction summary messages, and tool-result messages.
// Only genuine user-role messages (role == RoleUser without <summary> tags) are kept.
func collectUserMessageTail(messages []agent.Message, budgetTokens int) []agent.Message {
	if budgetTokens <= 0 {
		return nil
	}

	var collected []agent.Message
	remaining := budgetTokens

	for i := len(messages) - 1; i >= 0; i-- {
		if remaining <= 0 {
			break
		}

		msg := messages[i]

		// Only include real user messages — exclude system, tool, and summary messages.
		if msg.Role != agent.RoleUser {
			continue
		}
		if IsCompactionSummary(msg) {
			continue
		}

		msgTokens := EstimateMessageTokens(msg)
		if msgTokens <= remaining {
			collected = append(collected, msg)
			remaining -= msgTokens
		} else {
			// Truncate the message content to fit the remaining budget.
			// Subtract role overhead to get the content-only budget.
			roleTokens := EstimateTokens(string(msg.Role))
			contentBudget := remaining - roleTokens
			if contentBudget > 0 {
				maxChars := contentBudget * charsPerToken
				truncated := msg
				if len(msg.Content) > maxChars {
					truncated.Content = msg.Content[:maxChars]
				}
				collected = append(collected, truncated)
			}
			remaining = 0
		}
	}

	// Reverse to restore chronological order (oldest first).
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}

	return collected
}

func nextValidBoundary(messages []agent.Message, index int) int {
	return findValidBoundary(messages, index+1)
}

func findLastUserMessageIndex(messages []agent.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == agent.RoleUser {
			return i
		}
	}
	return -1
}
