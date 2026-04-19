package compaction

import (
	"encoding/json"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
)

func TestEstimateTokens(t *testing.T) {
	assert.Equal(t, 0, EstimateTokens(""))
	assert.Equal(t, 1, EstimateTokens("hi"))                         // 2 chars -> ceil(2/4) = 1
	assert.Equal(t, 3, EstimateTokens("hello world"))                // 11 chars -> ceil(11/4) = 3
	assert.Equal(t, 250, EstimateTokens(string(make([]byte, 1000)))) // 1000/4 = 250
}

func TestEstimateMessageTokens(t *testing.T) {
	msg := agent.Message{
		Role:    agent.RoleAssistant,
		Content: "I'll read that file.",
		ToolCalls: []agent.ToolCall{
			{Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		},
	}
	tokens := EstimateMessageTokens(msg)
	assert.Greater(t, tokens, 0)

	// Should be more than just content
	contentOnly := EstimateTokens("I'll read that file.")
	assert.Greater(t, tokens, contentOnly)
}

func TestEstimateConversationTokens(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "Read main.go"},
		{Role: agent.RoleAssistant, Content: "Here is the content."},
	}
	tokens := EstimateConversationTokens(msgs)
	assert.Greater(t, tokens, 0)
}

func TestEffectiveTokenCount(t *testing.T) {
	usage := agent.TokenUsage{
		Input:      1000,
		Output:     500,
		CacheRead:  200,
		CacheWrite: 100,
	}
	assert.Equal(t, 1800, EffectiveTokenCount(usage))
}

func TestShouldCompact(t *testing.T) {
	// 32K window, 95%, 8K reserve => effective = 30720, threshold = 22528
	assert.False(t, ShouldCompact(20000, 32768, 95, 8192))
	assert.True(t, ShouldCompact(25000, 32768, 95, 8192))

	// Edge cases
	assert.False(t, ShouldCompact(100, 0, 95, 8192))    // zero window
	assert.False(t, ShouldCompact(100, 32768, 0, 8192)) // zero percent
	assert.False(t, ShouldCompact(100, 100, 95, 10000)) // reserve > effective
}

func TestTruncateToolResult(t *testing.T) {
	short := "hello"
	assert.Equal(t, "hello", TruncateToolResult(short, 2000))

	long := string(make([]byte, 3000))
	truncated := TruncateToolResult(long, 2000)
	assert.Contains(t, truncated, "1000 more characters truncated")
	assert.Less(t, len(truncated), 3000)

	// Zero maxChars = no truncation
	assert.Equal(t, long, TruncateToolResult(long, 0))
}
