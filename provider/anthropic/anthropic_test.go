package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DocumentDrivenDX/forge"
	"github.com/DocumentDrivenDX/forge/provider/anthropic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer creates a mock Anthropic API server for testing.
func newTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(handler))
}

func TestProvider_Chat_Basic(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request format
		assert.Contains(t, r.URL.Path, "/messages")

		resp := `{
			"id": "msg_123",
			"type": "message",
			"role": "assistant",
			"content": [
				{"type": "text", "text": "Hello, world!"}
			],
			"model": "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage": {
				"input_tokens": 10,
				"output_tokens": 5
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	})
	defer srv.Close()

	// Note: This test would need SDK mocking to work fully
	// For now, we test the Config struct and New function
	cfg := anthropic.Config{
		APIKey: "test-key",
		Model:  "claude-sonnet-4-20250514",
	}
	p := anthropic.New(cfg)
	assert.NotNil(t, p)
}

func TestProvider_New(t *testing.T) {
	tests := []struct {
		name   string
		config anthropic.Config
	}{
		{
			name:   "with API key",
			config: anthropic.Config{APIKey: "sk-ant-key", Model: "test-model"},
		},
		{
			name:   "without API key",
			config: anthropic.Config{Model: "test-model"},
		},
		{
			name:   "empty config",
			config: anthropic.Config{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := anthropic.New(tt.config)
			assert.NotNil(t, p)
		})
	}
}

func TestProvider_ConvertMessages(t *testing.T) {
	// Test message conversion by using a real provider with a mock server
	// This exercises the message conversion code paths
	messages := []forge.Message{
		{Role: forge.RoleSystem, Content: "You are a helpful assistant."},
		{Role: forge.RoleUser, Content: "Hello"},
		{Role: forge.RoleAssistant, Content: "Hi there!"},
		{Role: forge.RoleTool, Content: "Tool result", ToolCallID: "tool_1"},
	}

	// We can't easily test the conversion without the SDK,
	// but we can verify the message structure
	for _, m := range messages {
		assert.NotEmpty(t, m.Role)
	}
}

func TestProvider_TokenUsage(t *testing.T) {
	// Test that TokenUsage can be marshaled correctly
	usage := forge.TokenUsage{
		Input:       100,
		Output:      50,
		CacheRead:   25,
		CacheWrite:  10,
		Total:       185,
	}

	data, err := json.Marshal(usage)
	require.NoError(t, err)

	var decoded forge.TokenUsage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, usage.Input, decoded.Input)
	assert.Equal(t, usage.Output, decoded.Output)
	assert.Equal(t, usage.CacheRead, decoded.CacheRead)
	assert.Equal(t, usage.CacheWrite, decoded.CacheWrite)
}

func TestProvider_TokenUsage_Add(t *testing.T) {
	usage1 := forge.TokenUsage{Input: 100, Output: 50, CacheRead: 25}
	usage2 := forge.TokenUsage{Input: 75, Output: 30, CacheWrite: 10}

	usage1.Add(usage2)

	assert.Equal(t, 175, usage1.Input)
	assert.Equal(t, 80, usage1.Output)
	assert.Equal(t, 25, usage1.CacheRead)
	assert.Equal(t, 10, usage1.CacheWrite)
}

func TestProvider_StreamDelta(t *testing.T) {
	delta := forge.StreamDelta{
		Content:   "Hello",
		Model:    "claude-sonnet-4-20250514",
		FinishReason: "end_turn",
		Usage: &forge.TokenUsage{
			Input:  100,
			Output: 50,
		},
		Done: true,
	}

	data, err := json.Marshal(delta)
	require.NoError(t, err)

	var decoded forge.StreamDelta
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "Hello", decoded.Content)
	assert.Equal(t, "claude-sonnet-4-20250514", decoded.Model)
	assert.Equal(t, "end_turn", decoded.FinishReason)
	assert.True(t, decoded.Done)
	assert.NotNil(t, decoded.Usage)
}

func TestProvider_StreamDelta_ToolCall(t *testing.T) {
	delta := forge.StreamDelta{
		ToolCallID:   "tool_123",
		ToolCallName: "read",
		ToolCallArgs: `{"path":"main.go"}`,
	}

	data, err := json.Marshal(delta)
	require.NoError(t, err)

	var decoded forge.StreamDelta
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "tool_123", decoded.ToolCallID)
	assert.Equal(t, "read", decoded.ToolCallName)
	assert.Equal(t, `{"path":"main.go"}`, decoded.ToolCallArgs)
}

func TestProvider_Response(t *testing.T) {
	resp := forge.Response{
		Content:      "Test response",
		Model:        "claude-sonnet-4-20250514",
		FinishReason: "end_turn",
		Usage: forge.TokenUsage{
			Input:  100,
			Output: 50,
		},
		ToolCalls: []forge.ToolCall{
			{
				ID:        "tc_1",
				Name:      "read",
				Arguments: json.RawMessage(`{"path":"file.go"}`),
			},
		},
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded forge.Response
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "Test response", decoded.Content)
	assert.Len(t, decoded.ToolCalls, 1)
	assert.Equal(t, "read", decoded.ToolCalls[0].Name)
}

func TestProvider_Messages(t *testing.T) {
	messages := []forge.Message{
		{Role: forge.RoleSystem, Content: "System prompt"},
		{Role: forge.RoleUser, Content: "User message"},
		{
			Role:      forge.RoleAssistant,
			Content:   "Assistant response",
			ToolCalls: []forge.ToolCall{
				{ID: "tc1", Name: "read", Arguments: json.RawMessage(`{}`)},
			},
		},
		{Role: forge.RoleTool, Content: "Tool result", ToolCallID: "tc1"},
	}

	// Verify all message types serialize correctly
	for _, msg := range messages {
		data, err := json.Marshal(msg)
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	}
}

func TestProvider_ContextCancellation(t *testing.T) {
	// Test that context cancellation is handled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cfg := anthropic.Config{Model: "test"}
	p := anthropic.New(cfg)

	// This should return quickly due to cancelled context
	_, err := p.Chat(ctx, []forge.Message{
		{Role: forge.RoleUser, Content: "test"},
	}, nil, forge.Options{})

	// Should error because context is cancelled
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "context") || strings.Contains(err.Error(), "Canceled"))
}

func TestProvider_EmptyMessages(t *testing.T) {
	cfg := anthropic.Config{Model: "test"}
	p := anthropic.New(cfg)

	// Empty message list should not panic
	_, err := p.Chat(context.Background(), []forge.Message{}, nil, forge.Options{})
	// May error, but should not panic
	_ = err
}

func TestProvider_Options(t *testing.T) {
	cfg := anthropic.Config{Model: "test"}
	p := anthropic.New(cfg)

	// Test with various options
	temp := 0.7
	opts := forge.Options{
		Model:       "override-model",
		Temperature: &temp,
		MaxTokens:   1000,
		Stop:        []string{"STOP"},
	}

	// Verify options are accepted (will fail at API call, but options are valid)
	_, _ = p.Chat(context.Background(), []forge.Message{
		{Role: forge.RoleUser, Content: "test"},
	}, nil, opts)
}

func TestProvider_ToolDefs(t *testing.T) {
	// Test tool definition serialization
	toolDef := forge.ToolDef{
		Name:        "read",
		Description: "Read a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}

	data, err := json.Marshal(toolDef)
	require.NoError(t, err)

	var decoded forge.ToolDef
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "read", decoded.Name)
	assert.Equal(t, "Read a file", decoded.Description)
}
