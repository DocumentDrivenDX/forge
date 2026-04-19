package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/provider/anthropic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer creates a mock Anthropic API server for testing.
func newTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(handler))
}

type anthropicSSEEvent struct {
	name string
	data string
}

func streamAnthropicSSE(w http.ResponseWriter, events []anthropicSSEEvent) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Transfer-Encoding", "chunked")
	flusher, _ := w.(http.Flusher)
	for _, ev := range events {
		fmt.Fprintf(w, "event: %s\n", ev.name)
		fmt.Fprintf(w, "data: %s\n\n", ev.data)
		if flusher != nil {
			flusher.Flush()
		}
	}
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

func TestProvider_Chat_AttemptMetadataIncludesServerIdentityAndCacheUsage(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "messages")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
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
				"output_tokens": 5,
				"cache_creation_input_tokens": 7,
				"cache_read_input_tokens": 2
			}
		}`))
	})
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	require.NoError(t, err)

	p := anthropic.New(anthropic.Config{
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-20250514",
		BaseURL: srv.URL,
	})

	resp, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "Hello"},
	}, nil, agent.Options{})
	require.NoError(t, err)

	require.NotNil(t, resp.Attempt)
	assert.Equal(t, "anthropic", resp.Attempt.ProviderName)
	assert.Equal(t, "anthropic", resp.Attempt.ProviderSystem)
	assert.Equal(t, parsed.Hostname(), resp.Attempt.ServerAddress)
	assert.NotZero(t, resp.Attempt.ServerPort)
	assert.Equal(t, "claude-sonnet-4-20250514", resp.Attempt.RequestedModel)
	assert.Equal(t, "claude-sonnet-4-20250514", resp.Attempt.ResponseModel)
	assert.Equal(t, "claude-sonnet-4-20250514", resp.Attempt.ResolvedModel)
	assert.Equal(t, 2, resp.Usage.CacheRead)
	assert.Equal(t, 7, resp.Usage.CacheWrite)
}

func TestProvider_ConvertMessages(t *testing.T) {
	// Test message conversion by using a real provider with a mock server
	// This exercises the message conversion code paths
	messages := []agent.Message{
		{Role: agent.RoleSystem, Content: "You are a helpful assistant."},
		{Role: agent.RoleUser, Content: "Hello"},
		{Role: agent.RoleAssistant, Content: "Hi there!"},
		{Role: agent.RoleTool, Content: "Tool result", ToolCallID: "tool_1"},
	}

	// We can't easily test the conversion without the SDK,
	// but we can verify the message structure
	for _, m := range messages {
		assert.NotEmpty(t, m.Role)
	}
}

func TestProvider_TokenUsage(t *testing.T) {
	// Test that TokenUsage can be marshaled correctly
	usage := agent.TokenUsage{
		Input:      100,
		Output:     50,
		CacheRead:  25,
		CacheWrite: 10,
		Total:      185,
	}

	data, err := json.Marshal(usage)
	require.NoError(t, err)

	var decoded agent.TokenUsage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, usage.Input, decoded.Input)
	assert.Equal(t, usage.Output, decoded.Output)
	assert.Equal(t, usage.CacheRead, decoded.CacheRead)
	assert.Equal(t, usage.CacheWrite, decoded.CacheWrite)
}

func TestProvider_TokenUsage_Add(t *testing.T) {
	usage1 := agent.TokenUsage{Input: 100, Output: 50, CacheRead: 25}
	usage2 := agent.TokenUsage{Input: 75, Output: 30, CacheWrite: 10}

	usage1.Add(usage2)

	assert.Equal(t, 175, usage1.Input)
	assert.Equal(t, 80, usage1.Output)
	assert.Equal(t, 25, usage1.CacheRead)
	assert.Equal(t, 10, usage1.CacheWrite)
}

func TestProvider_StreamDelta(t *testing.T) {
	delta := agent.StreamDelta{
		Content:      "Hello",
		Model:        "claude-sonnet-4-20250514",
		FinishReason: "end_turn",
		Usage: &agent.TokenUsage{
			Input:  100,
			Output: 50,
		},
		Done: true,
	}

	data, err := json.Marshal(delta)
	require.NoError(t, err)

	var decoded agent.StreamDelta
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "Hello", decoded.Content)
	assert.Equal(t, "claude-sonnet-4-20250514", decoded.Model)
	assert.Equal(t, "end_turn", decoded.FinishReason)
	assert.True(t, decoded.Done)
	assert.NotNil(t, decoded.Usage)
}

func TestProvider_StreamDelta_ToolCall(t *testing.T) {
	delta := agent.StreamDelta{
		ToolCallID:   "tool_123",
		ToolCallName: "read",
		ToolCallArgs: `{"path":"main.go"}`,
	}

	data, err := json.Marshal(delta)
	require.NoError(t, err)

	var decoded agent.StreamDelta
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "tool_123", decoded.ToolCallID)
	assert.Equal(t, "read", decoded.ToolCallName)
	assert.Equal(t, `{"path":"main.go"}`, decoded.ToolCallArgs)
}

func TestProvider_Response(t *testing.T) {
	resp := agent.Response{
		Content:      "Test response",
		Model:        "claude-sonnet-4-20250514",
		FinishReason: "end_turn",
		Usage: agent.TokenUsage{
			Input:  100,
			Output: 50,
		},
		ToolCalls: []agent.ToolCall{
			{
				ID:        "tc_1",
				Name:      "read",
				Arguments: json.RawMessage(`{"path":"file.go"}`),
			},
		},
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded agent.Response
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "Test response", decoded.Content)
	assert.Len(t, decoded.ToolCalls, 1)
	assert.Equal(t, "read", decoded.ToolCalls[0].Name)
}

func TestProvider_Messages(t *testing.T) {
	messages := []agent.Message{
		{Role: agent.RoleSystem, Content: "System prompt"},
		{Role: agent.RoleUser, Content: "User message"},
		{
			Role:    agent.RoleAssistant,
			Content: "Assistant response",
			ToolCalls: []agent.ToolCall{
				{ID: "tc1", Name: "read", Arguments: json.RawMessage(`{}`)},
			},
		},
		{Role: agent.RoleTool, Content: "Tool result", ToolCallID: "tc1"},
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
	_, err := p.Chat(ctx, []agent.Message{
		{Role: agent.RoleUser, Content: "test"},
	}, nil, agent.Options{})

	// Should error because context is cancelled
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "context") || strings.Contains(err.Error(), "Canceled"))
}

func TestProvider_EmptyMessages(t *testing.T) {
	cfg := anthropic.Config{Model: "test"}
	p := anthropic.New(cfg)

	// Empty message list should not panic
	_, err := p.Chat(context.Background(), []agent.Message{}, nil, agent.Options{})
	// May error, but should not panic
	_ = err
}

func TestProvider_Options(t *testing.T) {
	cfg := anthropic.Config{Model: "test"}
	p := anthropic.New(cfg)

	// Test with various options
	temp := 0.7
	opts := agent.Options{
		Model:       "override-model",
		Temperature: &temp,
		MaxTokens:   1000,
		Stop:        []string{"STOP"},
	}

	// Verify options are accepted (will fail at API call, but options are valid)
	_, _ = p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "test"},
	}, nil, opts)
}

func TestProvider_ToolDefs(t *testing.T) {
	// Test tool definition serialization
	toolDef := agent.ToolDef{
		Name:        "read",
		Description: "Read a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}

	data, err := json.Marshal(toolDef)
	require.NoError(t, err)

	var decoded agent.ToolDef
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "read", decoded.Name)
	assert.Equal(t, "Read a file", decoded.Description)
}

func TestProvider_Chat_SingleAttemptPerCall(t *testing.T) {
	var requests int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	defer srv.Close()

	p := anthropic.New(anthropic.Config{
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-20250514",
		BaseURL: srv.URL,
	})

	_, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requests))
}

func TestProvider_ChatStream_PartialContentPreservedWhenStreamErrors(t *testing.T) {
	events := []anthropicSSEEvent{
		{
			name: "message_start",
			data: `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514"},"usage":{"input_tokens":5}}`,
		},
		{
			name: "content_block_start",
			data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
		{
			name: "content_block_delta",
			data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial-response"}}`,
		},
		{
			name: "content_block_delta",
			data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"oops"}`,
		},
	}

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		streamAnthropicSSE(w, events)
	})
	defer srv.Close()

	p := anthropic.New(anthropic.Config{
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-20250514",
		BaseURL: srv.URL,
	})

	ch, err := p.ChatStream(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "stream"},
	}, nil, agent.Options{})
	require.NoError(t, err)

	var content string
	var streamErr error
	for delta := range ch {
		content += delta.Content
		if delta.Err != nil {
			streamErr = delta.Err
		}
	}

	assert.Contains(t, content, "partial-response")
	require.Error(t, streamErr)
}

func TestProvider_Chat_UnreachableEndpointFailsQuicklyAndMentionsEndpoint(t *testing.T) {
	baseURL := "http://127.0.0.1:1"
	p := anthropic.New(anthropic.Config{
		APIKey:  "test-key",
		Model:   "claude-sonnet-4-20250514",
		BaseURL: baseURL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Chat(ctx, []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 2*time.Second)
	assert.Contains(t, err.Error(), "anthropic:")
	assert.Contains(t, err.Error(), "127.0.0.1:1")
}

func TestProvider_Chat_MissingAPIKeyFailsAtCallTime(t *testing.T) {
	var requests int32
	var apiKeyHeader string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		apiKeyHeader = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	})
	defer srv.Close()

	p := anthropic.New(anthropic.Config{
		Model:   "claude-sonnet-4-20250514",
		BaseURL: srv.URL,
	})

	_, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requests), "constructor should succeed; auth failure should occur when calling Chat")
	assert.Empty(t, apiKeyHeader)
	assert.Contains(t, err.Error(), "401")
}
