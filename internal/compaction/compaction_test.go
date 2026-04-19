package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multiTurnProvider simulates a multi-turn conversation. It returns tool calls
// for the first N calls, then a text response.
type multiTurnProvider struct {
	toolRounds     int
	callCount      int
	summarizeCount int
}

func (p *multiTurnProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	p.callCount++

	// Summarization calls (no tools offered, system prompt contains "summarization")
	if len(tools) == 0 {
		p.summarizeCount++
		return agent.Response{
			Content: fmt.Sprintf("## Goal\nComplete the multi-step task\n\n## Progress\n### Done\n- [x] Completed %d rounds of work\n\n## Next Steps\n1. Continue processing", p.callCount),
			Usage:   agent.TokenUsage{Input: 500, Output: 100, Total: 600},
		}, nil
	}

	// Regular agent calls
	if p.callCount <= p.toolRounds {
		return agent.Response{
			ToolCalls: []agent.ToolCall{
				{
					ID:        fmt.Sprintf("tc%d", p.callCount),
					Name:      "read",
					Arguments: json.RawMessage(fmt.Sprintf(`{"path":"file%d.go"}`, p.callCount)),
				},
			},
			Usage: agent.TokenUsage{Input: 200, Output: 50, Total: 250},
		}, nil
	}

	return agent.Response{
		Content: "All done! Processed all files.",
		Usage:   agent.TokenUsage{Input: 300, Output: 30, Total: 330},
	}, nil
}

func TestCompactor_TriggersOnLargeConversation(t *testing.T) {
	provider := &multiTurnProvider{toolRounds: 15}

	cfg := DefaultConfig()
	cfg.ContextWindow = 500 // Very small window to force compaction
	cfg.ReserveTokens = 100
	cfg.KeepRecentTokens = 100
	cfg.EffectivePercent = 95

	compactor := NewCompactor(cfg)

	// Build a conversation that will exceed the context window
	var messages []agent.Message
	messages = append(messages, agent.Message{Role: agent.RoleSystem, Content: "You are a helpful assistant."})
	messages = append(messages, agent.Message{Role: agent.RoleUser, Content: "Process all 15 files in this project."})

	var toolCalls []agent.ToolCallLog
	compactionCount := 0

	// Simulate the agent loop
	for i := 0; i < 15; i++ {
		// Check compaction
		newMsgs, _, err := compactor(context.Background(), messages, provider, toolCalls)
		require.NoError(t, err)
		if len(newMsgs) < len(messages) {
			compactionCount++
		}
		messages = newMsgs

		// Simulate a tool call round
		messages = append(messages, agent.Message{
			Role: agent.RoleAssistant,
			ToolCalls: []agent.ToolCall{
				{ID: fmt.Sprintf("tc%d", i), Name: "read", Arguments: json.RawMessage(fmt.Sprintf(`{"path":"file%d.go"}`, i))},
			},
		})
		messages = append(messages, agent.Message{
			Role:       agent.RoleTool,
			Content:    fmt.Sprintf("package file%d\n\nfunc Do%d() { /* implementation with lots of code */ }\n%s", i, i, string(make([]byte, 300))),
			ToolCallID: fmt.Sprintf("tc%d", i),
		})
		messages = append(messages, agent.Message{
			Role:    agent.RoleAssistant,
			Content: fmt.Sprintf("Read file%d.go — it contains function Do%d with substantial implementation details.", i, i),
		})
		messages = append(messages, agent.Message{
			Role:    agent.RoleUser,
			Content: "Continue with the next step.",
		})

		toolCalls = append(toolCalls, agent.ToolCallLog{
			Tool:  "read",
			Input: json.RawMessage(fmt.Sprintf(`{"path":"file%d.go"}`, i)),
		})
	}

	assert.Greater(t, compactionCount, 0, "compaction should have fired at least once")
	assert.Greater(t, provider.summarizeCount, 0, "summarization should have been called")

	// After compaction, first message should be a summary
	hasSummary := false
	for _, msg := range messages {
		if IsCompactionSummary(msg) {
			hasSummary = true
			break
		}
	}
	assert.True(t, hasSummary, "conversation should contain a compaction summary")

	t.Logf("Compactions: %d, Summarize calls: %d, Final messages: %d",
		compactionCount, provider.summarizeCount, len(messages))
}

func TestCompactor_NoCompactionWhenDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false

	compactor := NewCompactor(cfg)

	// Even with a huge conversation, shouldn't compact
	var messages []agent.Message
	for i := 0; i < 100; i++ {
		messages = append(messages, agent.Message{Role: agent.RoleUser, Content: string(make([]byte, 1000))})
	}

	result, _, err := compactor(context.Background(), messages, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, len(messages), len(result))
}

func TestCompactor_SkipsRecompaction(t *testing.T) {
	provider := &multiTurnProvider{toolRounds: 0}

	cfg := DefaultConfig()
	cfg.ContextWindow = 100
	cfg.ReserveTokens = 10
	cfg.KeepRecentTokens = 20

	compactor := NewCompactor(cfg)

	// Start with a summary as the last message (simulating just-compacted state)
	messages := []agent.Message{
		InjectSummary("## Goal\nJust compacted"),
	}

	result, _, err := compactor(context.Background(), messages, provider, nil)
	require.NoError(t, err)
	assert.Equal(t, len(messages), len(result), "should skip re-compaction")
}

func TestEndToEnd_AgentLoopWithCompaction(t *testing.T) {
	provider := &multiTurnProvider{toolRounds: 8}

	cfg := DefaultConfig()
	cfg.ContextWindow = 400
	cfg.ReserveTokens = 80
	cfg.KeepRecentTokens = 80

	// Simulate what agent.Run does
	var messages []agent.Message
	messages = append(messages, agent.Message{Role: agent.RoleUser, Content: "Process all files"})

	readTool := agent.ToolDef{Name: "read", Description: "Read file"}
	tools := []agent.ToolDef{readTool}
	var allToolCalls []agent.ToolCallLog

	compactor := NewCompactor(cfg)
	var events []string

	for iteration := 0; iteration < 20; iteration++ {
		// Pre-iteration compaction check
		newMsgs, _, err := compactor(context.Background(), messages, provider, allToolCalls)
		require.NoError(t, err)
		if len(newMsgs) < len(messages) {
			events = append(events, fmt.Sprintf("compacted at iteration %d: %d -> %d msgs", iteration, len(messages), len(newMsgs)))
		}
		messages = newMsgs

		// Call provider
		resp, err := provider.Chat(context.Background(), messages, tools, agent.Options{})
		require.NoError(t, err)

		if len(resp.ToolCalls) == 0 {
			events = append(events, fmt.Sprintf("done at iteration %d: %q", iteration, resp.Content))
			break
		}

		// Execute tool calls
		messages = append(messages, agent.Message{Role: agent.RoleAssistant, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			messages = append(messages, agent.Message{
				Role:       agent.RoleTool,
				Content:    "file content here " + string(make([]byte, 200)),
				ToolCallID: tc.ID,
			})
			allToolCalls = append(allToolCalls, agent.ToolCallLog{Tool: tc.Name, Input: tc.Arguments})
		}
	}

	assert.GreaterOrEqual(t, len(events), 1, "should have at least completed")
	for _, e := range events {
		t.Log(e)
	}
}

type resumableSummaryProvider struct {
	responses      []agent.Response
	callCount      int
	summaryPrompts []string
}

func (p *resumableSummaryProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	if len(messages) >= 2 &&
		messages[0].Role == agent.RoleSystem &&
		strings.Contains(messages[0].Content, "context summarization assistant") {
		p.summaryPrompts = append(p.summaryPrompts, messages[1].Content)
		if previous := extractPromptBlock(messages[1].Content, "<previous-summary>", "</previous-summary>"); previous != "" {
			return agent.Response{Content: previous}, nil
		}
		return agent.Response{
			Content: "## Goal\nCarry forward the resumed session\n\n## Progress\n### Done\n- [x] Preserved earlier context",
		}, nil
	}

	if p.callCount >= len(p.responses) {
		return agent.Response{Content: "done"}, nil
	}

	resp := p.responses[p.callCount]
	p.callCount++
	return resp, nil
}

func extractPromptBlock(content, startTag, endTag string) string {
	start := strings.Index(content, startTag)
	if start < 0 {
		return ""
	}
	start += len(startTag)
	end := strings.Index(content[start:], endTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(content[start : start+end])
}

func TestRun_ResumedHistoryPreservesCompactionStateAcrossFreshCompactors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ContextWindow = 500
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 100
	cfg.EffectivePercent = 100

	seedSummary := InjectSummary("## Goal\nSeeded prior work\n\n## Progress\n### Done\n- [x] Read first.go\n\n<read-files>\nfirst.go\n</read-files>")
	padding := strings.Repeat("padding ", 250)

	provider := &resumableSummaryProvider{
		responses: []agent.Response{
			{Content: "first run complete"},
			{Content: "second run complete"},
		},
	}

	first, err := agent.Run(context.Background(), agent.Request{
		History: []agent.Message{
			seedSummary,
			{Role: agent.RoleUser, Content: padding},
			{Role: agent.RoleAssistant, Content: padding},
		},
		Prompt:    strings.Repeat("continue ", 40),
		Provider:  provider,
		Compactor: NewCompactor(cfg),
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, first.Status)
	require.Len(t, provider.summaryPrompts, 1)
	assert.Contains(t, provider.summaryPrompts[0], "Seeded prior work")
	assert.Contains(t, provider.summaryPrompts[0], "first.go")

	second, err := agent.Run(context.Background(), agent.Request{
		History:   first.Messages,
		Prompt:    strings.Repeat("resume ", 240),
		Provider:  provider,
		Compactor: NewCompactor(cfg),
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, second.Status)
	require.Len(t, provider.summaryPrompts, 2)
	assert.Contains(t, provider.summaryPrompts[1], "Seeded prior work")
	assert.Contains(t, provider.summaryPrompts[1], "first.go")

	var carriedSummary string
	for i := len(second.Messages) - 1; i >= 0; i-- {
		if IsCompactionSummary(second.Messages[i]) {
			carriedSummary = second.Messages[i].Content
			break
		}
	}
	require.NotEmpty(t, carriedSummary, "second run should still contain a compaction summary")
	assert.Contains(t, carriedSummary, "Seeded prior work")
	assert.Contains(t, carriedSummary, "first.go")
}

func TestCompact_UserMessageTailReInclusion(t *testing.T) {
	provider := &multiTurnProvider{toolRounds: 0}

	cfg := DefaultConfig()
	cfg.ContextWindow = 500
	cfg.ReserveTokens = 50
	cfg.KeepRecentTokens = 80
	cfg.EffectivePercent = 100
	cfg.UserMessageTailTokens = 500 // generous budget to include tail messages

	// Build a history with several distinct user messages followed by assistant/tool turns.
	// The compacted section (before the cut) should contain real user messages that
	// get re-included alongside the summary.
	var messages []agent.Message
	messages = append(messages, agent.Message{Role: agent.RoleUser, Content: "Initial request: process all files"})
	for i := 0; i < 8; i++ {
		messages = append(messages, agent.Message{
			Role:    agent.RoleAssistant,
			Content: fmt.Sprintf("Working on step %d: reading file and processing it now.", i) + string(make([]byte, 200)),
		})
		messages = append(messages, agent.Message{
			Role:    agent.RoleUser,
			Content: fmt.Sprintf("User follow-up %d: continue with next file", i),
		})
	}
	// The last user message will be kept verbatim in the recent section.
	messages = append(messages, agent.Message{Role: agent.RoleUser, Content: "Final step: wrap up"})

	compactor := NewCompactor(cfg)
	newMsgs, result, err := compactor(context.Background(), messages, provider, nil)
	require.NoError(t, err)
	require.NotNil(t, result, "compaction should have triggered")

	// Verify a summary is present.
	hasSummary := false
	for _, msg := range newMsgs {
		if IsCompactionSummary(msg) {
			hasSummary = true
			break
		}
	}
	assert.True(t, hasSummary, "compacted history must contain a summary message")

	// Verify that at least one real user message from the compacted section was
	// re-included alongside the summary (the tail re-inclusion feature).
	reIncludedUserCount := 0
	for _, msg := range newMsgs {
		if msg.Role == agent.RoleUser && !IsCompactionSummary(msg) {
			reIncludedUserCount++
		}
	}
	assert.Greater(t, reIncludedUserCount, 0, "at least one real user message should be re-included after compaction")

	t.Logf("Total messages after compaction: %d, re-included user messages: %d", len(newMsgs), reIncludedUserCount)
}

// stuckProvider always returns a summary that is too small to change the
// compaction decision, simulating the stuck state where ShouldCompact keeps
// returning true but compactMessages can't produce a valid result.
type stuckProvider struct {
	callCount int
}

func (p *stuckProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	p.callCount++
	// Return an empty summary so compactAtCutIndex can't fit it properly.
	return agent.Response{
		Content: "",
		Usage:   agent.TokenUsage{Input: 10, Output: 1, Total: 11},
	}, nil
}

func TestCompactor_ReturnsErrCompactionStuckAfterThreshold(t *testing.T) {
	// Configure a compactor where ShouldCompact is always true but
	// compactMessages cannot find a valid cut point, producing consecutive
	// nil-result returns.
	cfg := DefaultConfig()
	cfg.ContextWindow = 100
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 0
	cfg.EffectivePercent = 100
	cfg.StuckThreshold = 3

	compactor := NewCompactor(cfg)

	// Build a minimal conversation that exceeds the context window but has
	// no valid cut point because it contains only a single assistant message
	// (no user messages after the first to cut at).
	messages := []agent.Message{
		{Role: agent.RoleUser, Content: strings.Repeat("A", 200)},
		{Role: agent.RoleAssistant, Content: strings.Repeat("B", 200)},
	}

	var lastErr error
	for i := 0; i < cfg.StuckThreshold+2; i++ {
		_, _, lastErr = compactor(context.Background(), messages, &stuckProvider{}, nil)
		if lastErr != nil {
			break
		}
	}

	require.Error(t, lastErr, "compactor should have returned an error after %d consecutive failed attempts", cfg.StuckThreshold)
	assert.ErrorIs(t, lastErr, agent.ErrCompactionStuck)
}

func TestCompactor_StuckCounterResetsOnSuccess(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ContextWindow = 200
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 40
	cfg.EffectivePercent = 100
	cfg.StuckThreshold = 3

	provider := &multiTurnProvider{toolRounds: 0}
	compactor := NewCompactor(cfg)

	// Minimal messages that exceed the threshold but can't compact (no valid cut).
	smallMessages := []agent.Message{
		{Role: agent.RoleUser, Content: strings.Repeat("A", 200)},
		{Role: agent.RoleAssistant, Content: strings.Repeat("B", 200)},
	}

	// Accumulate some failed attempts (but below threshold).
	for i := 0; i < cfg.StuckThreshold-1; i++ {
		_, _, err := compactor(context.Background(), smallMessages, provider, nil)
		require.NoError(t, err, "should not error before threshold")
	}

	// Now feed a conversation that compacts successfully to reset the counter.
	var bigMessages []agent.Message
	bigMessages = append(bigMessages, agent.Message{Role: agent.RoleUser, Content: "start"})
	for i := 0; i < 10; i++ {
		bigMessages = append(bigMessages, agent.Message{
			Role:    agent.RoleAssistant,
			Content: fmt.Sprintf("assistant reply %d: %s", i, strings.Repeat("X", 200)),
		})
		bigMessages = append(bigMessages, agent.Message{
			Role:    agent.RoleUser,
			Content: fmt.Sprintf("user message %d", i),
		})
	}

	newMsgs, result, err := compactor(context.Background(), bigMessages, provider, nil)
	require.NoError(t, err)
	require.NotNil(t, result, "compaction should succeed and reset the counter")
	require.Less(t, len(newMsgs), len(bigMessages))

	// After reset, the small-message noop should be tolerated again.
	for i := 0; i < cfg.StuckThreshold-1; i++ {
		_, _, err := compactor(context.Background(), smallMessages, provider, nil)
		require.NoError(t, err, "should not error after counter reset")
	}
}

func TestCompactor_StuckCounterResetsWhenBelowThreshold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ContextWindow = 200
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 40
	cfg.EffectivePercent = 100
	cfg.StuckThreshold = 3

	compactor := NewCompactor(cfg)

	// Messages that exceed threshold — will trigger compaction attempts.
	bigMessages := []agent.Message{
		{Role: agent.RoleUser, Content: strings.Repeat("A", 200)},
		{Role: agent.RoleAssistant, Content: strings.Repeat("B", 200)},
	}

	// Messages that are below threshold — will NOT trigger compaction.
	smallMessages := []agent.Message{
		{Role: agent.RoleUser, Content: "hi"},
	}

	// Accumulate failures just below the threshold.
	for i := 0; i < cfg.StuckThreshold-1; i++ {
		_, _, err := compactor(context.Background(), bigMessages, &stuckProvider{}, nil)
		require.NoError(t, err)
	}

	// A call where ShouldCompact returns false resets the counter.
	_, _, err := compactor(context.Background(), smallMessages, &stuckProvider{}, nil)
	require.NoError(t, err)

	// Now we should tolerate another threshold-1 failures.
	for i := 0; i < cfg.StuckThreshold-1; i++ {
		_, _, err := compactor(context.Background(), bigMessages, &stuckProvider{}, nil)
		require.NoError(t, err)
	}
}
