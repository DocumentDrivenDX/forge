package compaction

import (
	"context"
	"encoding/json"
	"fmt"
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
