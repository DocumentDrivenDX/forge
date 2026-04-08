package compaction

import (
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
)

func TestFindCutPoint(t *testing.T) {
	t.Run("keeps recent messages", func(t *testing.T) {
		msgs := make([]agent.Message, 10)
		for i := range msgs {
			msgs[i] = agent.Message{Role: agent.RoleUser, Content: "message " + string(rune('0'+i))}
		}
		// With a tiny budget, should keep only the last few
		cut := FindCutPoint(msgs, 20)
		assert.Greater(t, cut, 0)
		assert.Less(t, cut, len(msgs))
	})

	t.Run("never cuts at tool result", func(t *testing.T) {
		msgs := []agent.Message{
			{Role: agent.RoleUser, Content: "do something"},
			{Role: agent.RoleAssistant, Content: "thinking..."},
			{Role: agent.RoleTool, Content: "tool output", ToolCallID: "tc1"},
			{Role: agent.RoleUser, Content: "thanks"},
		}
		// Force cut near the tool result
		cut := FindCutPoint(msgs, 10)
		if cut > 0 && cut < len(msgs) {
			assert.NotEqual(t, agent.RoleTool, msgs[cut].Role,
				"cut point should not be at a tool result")
		}
	})

	t.Run("empty messages", func(t *testing.T) {
		assert.Equal(t, 0, FindCutPoint(nil, 1000))
	})

	t.Run("budget exceeds total", func(t *testing.T) {
		msgs := []agent.Message{
			{Role: agent.RoleUser, Content: "short"},
		}
		// Huge budget — should keep everything (cut at 0)
		cut := FindCutPoint(msgs, 100000)
		assert.Equal(t, 0, cut)
	})
}

func TestIsCompactionSummary(t *testing.T) {
	summary := agent.Message{
		Role:    agent.RoleUser,
		Content: "The conversation history before this point was compacted into the following summary:\n\n<summary>\n## Goal\nDo stuff\n</summary>",
	}
	assert.True(t, IsCompactionSummary(summary))

	regular := agent.Message{
		Role:    agent.RoleUser,
		Content: "Read main.go please",
	}
	assert.False(t, IsCompactionSummary(regular))
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, 8192, cfg.ContextWindow)
	assert.Equal(t, 8192, cfg.ReserveTokens)
	assert.Equal(t, 95, cfg.EffectivePercent)
	assert.Equal(t, 2000, cfg.MaxToolResultChars)
}
