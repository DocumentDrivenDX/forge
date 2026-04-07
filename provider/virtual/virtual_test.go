package virtual

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anthropics/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_InlineResponses(t *testing.T) {
	p := New(Config{
		InlineResponses: []InlineResponse{
			{
				PromptMatch: "hello",
				Response: forge.Response{
					Content: "Hello back!",
					Usage:   forge.TokenUsage{Input: 5, Output: 3, Total: 8},
					Model:   "virtual",
				},
			},
			{
				PromptMatch: `/read.*file/`,
				Response: forge.Response{
					Content: "I'll read that for you.",
					ToolCalls: []forge.ToolCall{
						{ID: "tc1", Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
					},
					Usage: forge.TokenUsage{Input: 10, Output: 5, Total: 15},
				},
			},
		},
	})

	t.Run("substring match", func(t *testing.T) {
		msgs := []forge.Message{{Role: forge.RoleUser, Content: "hello world"}}
		resp, err := p.Chat(context.Background(), msgs, nil, forge.Options{})
		require.NoError(t, err)
		assert.Equal(t, "Hello back!", resp.Content)
		assert.Equal(t, 8, resp.Usage.Total)
	})

	t.Run("regex match with tool calls", func(t *testing.T) {
		msgs := []forge.Message{{Role: forge.RoleUser, Content: "please read my file"}}
		resp, err := p.Chat(context.Background(), msgs, nil, forge.Options{})
		require.NoError(t, err)
		assert.Equal(t, "I'll read that for you.", resp.Content)
		require.Len(t, resp.ToolCalls, 1)
		assert.Equal(t, "read", resp.ToolCalls[0].Name)
	})

	t.Run("no match returns error", func(t *testing.T) {
		msgs := []forge.Message{{Role: forge.RoleUser, Content: "something unmatched"}}
		_, err := p.Chat(context.Background(), msgs, nil, forge.Options{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no matching inline response")
	})
}

func TestProvider_DictionaryLookup(t *testing.T) {
	dir := t.TempDir()

	// Record an entry
	msgs := []forge.Message{
		{Role: forge.RoleUser, Content: "What is the package name?"},
	}
	resp := forge.Response{
		Content: "The package name is main.",
		Usage:   forge.TokenUsage{Input: 20, Output: 10, Total: 30},
		Model:   "recorded-model",
	}
	err := RecordEntry(dir, msgs, resp, nil)
	require.NoError(t, err)

	// Look it up
	p := New(Config{DictDir: dir})
	result, err := p.Chat(context.Background(), msgs, nil, forge.Options{})
	require.NoError(t, err)
	assert.Equal(t, "The package name is main.", result.Content)
	assert.Equal(t, 30, result.Usage.Total)
	assert.Equal(t, "recorded-model", result.Model)
}

func TestProvider_DictionaryMiss(t *testing.T) {
	dir := t.TempDir()
	p := New(Config{DictDir: dir})

	msgs := []forge.Message{{Role: forge.RoleUser, Content: "not recorded"}}
	_, err := p.Chat(context.Background(), msgs, nil, forge.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no recorded response")
}

func TestProvider_NormalizePatterns(t *testing.T) {
	dir := t.TempDir()

	patterns := []NormalizePattern{
		{Pattern: `/tmp/[a-zA-Z0-9]+`, Replace: "/tmp/NORMALIZED"},
	}

	// Record with a specific temp path
	msgs1 := []forge.Message{{Role: forge.RoleUser, Content: "Read /tmp/abc123/main.go"}}
	resp := forge.Response{Content: "package main"}
	err := RecordEntry(dir, msgs1, resp, patterns)
	require.NoError(t, err)

	// Look up with a different temp path — should match after normalization
	p := New(Config{DictDir: dir, NormalizePatterns: patterns})
	msgs2 := []forge.Message{{Role: forge.RoleUser, Content: "Read /tmp/xyz789/main.go"}}
	result, err := p.Chat(context.Background(), msgs2, nil, forge.Options{})
	require.NoError(t, err)
	assert.Equal(t, "package main", result.Content)
}

func TestPromptHash(t *testing.T) {
	h1 := PromptHash("hello")
	h2 := PromptHash("hello")
	h3 := PromptHash("world")

	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h1, h3)
	assert.Len(t, h1, 16) // 8 bytes = 16 hex chars
}

func TestRecordEntry(t *testing.T) {
	dir := t.TempDir()

	msgs := []forge.Message{{Role: forge.RoleUser, Content: "test prompt"}}
	resp := forge.Response{Content: "test response"}

	err := RecordEntry(dir, msgs, resp, nil)
	require.NoError(t, err)

	// Verify file exists and is valid
	hash := PromptHash("test prompt")
	path := filepath.Join(dir, hash+".json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var entry Entry
	require.NoError(t, json.Unmarshal(data, &entry))
	assert.Equal(t, hash, entry.PromptHash)
	assert.Equal(t, "test response", entry.Response.Content)
	assert.NotEmpty(t, entry.RecordedAt)
}

func TestMatchPattern(t *testing.T) {
	assert.True(t, matchPattern("hello", "say hello world"))
	assert.False(t, matchPattern("goodbye", "say hello world"))
	assert.True(t, matchPattern(`/hel+o/`, "say hello world"))
	assert.False(t, matchPattern(`/^hello$/`, "say hello world"))
}
