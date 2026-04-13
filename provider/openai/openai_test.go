package openai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/provider/openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamSSE writes a sequence of SSE data lines followed by a final [DONE] event.
func streamSSE(w http.ResponseWriter, events []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Transfer-Encoding", "chunked")
	flusher, _ := w.(http.Flusher)
	for _, ev := range events {
		fmt.Fprintf(w, "data: %s\n\n", ev)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// TestChatStream_ToolCallIndexIDMapping verifies that the OpenAI provider
// carries the tool call ID forward using the chunk index when OpenAI omits
// the ID on all but the first argument chunk.
func TestChatStream_ToolCallIndexIDMapping(t *testing.T) {
	// OpenAI streaming format: first chunk has id+name, subsequent chunks have
	// index but empty id, and carry argument fragments.
	chunks := []string{
		// chunk 0: tool call header — id and name present
		`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		// chunk 1: first arg fragment — no id
		`{"id":"chatcmpl-1","model":"","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		// chunk 2: second arg fragment — no id
		`{"id":"chatcmpl-1","model":"","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]},"finish_reason":null}]}`,
		// chunk 3: finish
		`{"id":"chatcmpl-1","model":"","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamSSE(w, chunks)
	}))
	defer srv.Close()

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "gpt-4o",
	})

	ch, err := p.ChatStream(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "call the read tool"},
	}, nil, agent.Options{})
	require.NoError(t, err)

	// Drain the channel and collect all ToolCallArgs by ID
	argsByID := make(map[string]string)
	idNames := make(map[string]string)
	for delta := range ch {
		if delta.Err != nil {
			t.Fatalf("unexpected stream error: %v", delta.Err)
		}
		if delta.ToolCallID != "" {
			argsByID[delta.ToolCallID] += delta.ToolCallArgs
			if delta.ToolCallName != "" {
				idNames[delta.ToolCallID] = delta.ToolCallName
			}
		}
	}

	require.Contains(t, argsByID, "call_abc", "tool call ID must be present on all arg deltas")
	assert.Equal(t, `{"path":"main.go"}`, argsByID["call_abc"], "arguments must be assembled from all chunks")
	assert.Equal(t, "read", idNames["call_abc"])
}

func TestChat_AttemptMetadataIncludesServerIdentityAndCacheUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":5,
				"total_tokens":17,
				"prompt_tokens_details":{"cached_tokens":3}
			}
		}`))
	}))
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	require.NoError(t, err)

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "gpt-4o",
	})

	resp, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	require.NoError(t, err)

	require.NotNil(t, resp.Attempt)
	assert.Equal(t, "openai-compat", resp.Attempt.ProviderName)
	assert.Equal(t, "local", resp.Attempt.ProviderSystem)
	assert.Equal(t, parsed.Hostname(), resp.Attempt.ServerAddress)
	assert.NotZero(t, resp.Attempt.ServerPort)
	assert.Equal(t, "gpt-4o", resp.Attempt.RequestedModel)
	assert.Equal(t, "gpt-4o", resp.Attempt.ResponseModel)
	assert.Equal(t, "gpt-4o", resp.Attempt.ResolvedModel)
	assert.Equal(t, 3, resp.Usage.CacheRead)
}

func TestChat_SingleAttemptPerCall(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "gpt-4o",
	})

	_, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requests))
}

func TestChatStream_PartialContentPreservedWhenStreamErrors(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"partial-response"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"oops"}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamSSE(w, chunks)
	}))
	defer srv.Close()

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "gpt-4o",
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

func TestChat_UnreachableEndpointFailsQuicklyAndMentionsEndpoint(t *testing.T) {
	baseURL := "http://127.0.0.1:1/v1"
	p := openai.New(openai.Config{
		BaseURL: baseURL,
		APIKey:  "test",
		Model:   "gpt-4o",
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
	assert.Contains(t, err.Error(), "openai:")
	assert.Contains(t, err.Error(), "127.0.0.1:1")
}

func TestChat_MissingAPIKeyFailsAtCallTime(t *testing.T) {
	var requests int32
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		Model:   "gpt-4o",
	})

	_, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requests), "constructor should not fail; request should fail at call time")
	assert.Equal(t, "Bearer not-needed", authHeader)
	assert.Contains(t, err.Error(), "401")
}

func TestChat_ToolDefinitionsAreSentToAPI(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}
		}`))
	}))
	defer srv.Close()

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "gpt-4o",
	})

	toolDefs := []agent.ToolDef{
		{
			Name:        "read",
			Description: "Read file contents",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		},
		{
			Name:        "bash",
			Description: "Run shell commands",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		},
	}

	_, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "read the file"},
	}, toolDefs, agent.Options{})
	require.NoError(t, err)

	var reqBody map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &reqBody))

	tools, ok := reqBody["tools"].([]interface{})
	require.True(t, ok, "request must include 'tools' array")
	assert.Len(t, tools, 2)

	first := tools[0].(map[string]interface{})["function"].(map[string]interface{})
	assert.Equal(t, "read", first["name"])
	assert.Equal(t, "Read file contents", first["description"])

	second := tools[1].(map[string]interface{})["function"].(map[string]interface{})
	assert.Equal(t, "bash", second["name"])
}

func TestChatStream_ToolDefinitionsAreSentToAPI(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body
		streamSSE(w, []string{
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
		})
	}))
	defer srv.Close()

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "gpt-4o",
	})

	toolDefs := []agent.ToolDef{
		{
			Name:        "read",
			Description: "Read file contents",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
	}

	ch, err := p.ChatStream(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "read the file"},
	}, toolDefs, agent.Options{})
	require.NoError(t, err)
	for range ch { /* drain */
	}

	var reqBody map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &reqBody))

	tools, ok := reqBody["tools"].([]interface{})
	require.True(t, ok, "streaming request must include 'tools' array")
	assert.Len(t, tools, 1)

	fn := tools[0].(map[string]interface{})["function"].(map[string]interface{})
	assert.Equal(t, "read", fn["name"])
	assert.Equal(t, "Read file contents", fn["description"])
}

func TestNew_LocalOpenAICompatibleBaseURLsResolveProviderIdentity(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		system  string
		host    string
		port    int
	}{
		{
			name:    "lmstudio default local endpoint",
			baseURL: "http://localhost:1234/v1",
			system:  "lmstudio",
			host:    "localhost",
			port:    1234,
		},
		{
			name:    "ollama openai-compatible endpoint",
			baseURL: "http://127.0.0.1:11434/v1",
			system:  "ollama",
			host:    "127.0.0.1",
			port:    11434,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := openai.New(openai.Config{
				BaseURL: tt.baseURL,
				Model:   "gpt-4o",
			})
			system, host, port := p.ChatStartMetadata()
			assert.Equal(t, tt.system, system)
			assert.Equal(t, tt.host, host)
			assert.Equal(t, tt.port, port)
		})
	}
}

func TestOpenAIIdentity_CloudAndPortHeuristics(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		system  string
	}{
		{
			name:    "minimax cloud endpoint",
			baseURL: "https://api.minimaxi.chat/v1",
			system:  "minimax",
		},
		{
			name:    "qwen/dashscope cloud endpoint",
			baseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			system:  "qwen",
		},
		{
			name:    "z.ai cloud endpoint",
			baseURL: "https://api.z.ai/v1",
			system:  "zai",
		},
		{
			name:    "named host port 11434 treated as ollama",
			baseURL: "http://vidar:11434/v1",
			system:  "ollama",
		},
		{
			name:    "named host port 1234 treated as lmstudio",
			baseURL: "http://vidar:1234/v1",
			system:  "lmstudio",
		},
		{
			name:    "named host non-standard port treated as local",
			baseURL: "http://vidar:8080/v1",
			system:  "local",
		},
		{
			name:    "unknown host standard HTTPS port falls through to openai",
			baseURL: "https://api.example.com/v1",
			system:  "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := openai.New(openai.Config{
				BaseURL: tt.baseURL,
				Model:   "gpt-4o",
			})
			system, _, _ := p.ChatStartMetadata()
			assert.Equal(t, tt.system, system)
		})
	}
}
