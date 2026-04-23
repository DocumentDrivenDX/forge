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

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/provider/openai"
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

// writeRawSSE lets a test emit arbitrary SSE framing including `:` comment
// frames (keep-alive probes) and inter-frame sleeps. `frames` are written in
// order; each string is written verbatim (the caller provides terminators),
// followed by a flush. A positive `sleep` inserts a wall-clock delay between
// frames so tests can reproduce the "long silence then data" shape that
// reasoning-model warmup produces.
func writeRawSSE(w http.ResponseWriter, frames []string, sleep time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Transfer-Encoding", "chunked")
	flusher, _ := w.(http.Flusher)
	for _, f := range frames {
		_, _ = io.WriteString(w, f)
		if flusher != nil {
			flusher.Flush()
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
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

// TestChatStream_SurvivesSSECommentFramesAndLongSilence reproduces the
// omlx/reasoning-model streaming defect tracked by bead agent-f237e07b.
//
// The real failure mode is:
//  1. Server sends a `: keep-alive\n\n` SSE comment frame while the reasoning
//     model warms up (several seconds before the first content frame arrives).
//  2. openai-go's ssestream decoder treats that comment's trailing blank line
//     as an event dispatch with empty Data. Stream.Next then tries to
//     json.Unmarshal empty bytes and surfaces "unexpected end of JSON input",
//     which propagates up as a user-visible error — even though the wire
//     stream is well-formed per the SSE spec (which requires empty-data
//     events to be silently ignored).
//
// This test reproduces the exact frame shape captured against a vidar-omlx
// server: a keep-alive comment first, then the role delta, then (after a
// silence) the first content delta. It asserts that the stream completes
// without error and delivers the content.
func TestChatStream_SurvivesSSECommentFramesAndLongSilence(t *testing.T) {
	// Frames mirror the wire capture from /tmp/vidar-omlx-wire2.jsonl:
	// ": keep-alive" comment, then a role delta, then content.
	frames := []string{
		": keep-alive\n\n",
		`data: {"id":"chatcmpl-1","model":"qwen3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
		": keep-alive\n\n",
		`data: {"id":"chatcmpl-1","model":"qwen3","choices":[{"index":0,"delta":{"content":"warmup-done"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-1","model":"qwen3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}` + "\n\n",
		"data: [DONE]\n\n",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A short inter-frame sleep is enough to exercise the per-chunk
		// arrival shape; we do not need a full 9s warmup to trigger the
		// decoder bug because the empty-event dispatch happens on the
		// first keep-alive frame regardless of timing.
		writeRawSSE(w, frames, 10*time.Millisecond)
	}))
	defer srv.Close()

	p := openai.New(openai.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "qwen3",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := p.ChatStream(ctx, []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	require.NoError(t, err)

	var content string
	var streamErr error
	for delta := range ch {
		if delta.Err != nil {
			streamErr = delta.Err
		}
		content += delta.Content
	}

	require.NoError(t, streamErr, "keep-alive SSE comment frames must not corrupt stream parsing")
	assert.Contains(t, content, "warmup-done", "content delta that follows a keep-alive frame must still be delivered")
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
	assert.Equal(t, "openai", resp.Attempt.ProviderName)
	assert.Equal(t, "openai", resp.Attempt.ProviderSystem)
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

func TestThinkingSerializationReasoningPolicy(t *testing.T) {
	tests := []struct {
		name              string
		configReasoning   agent.Reasoning
		opts              agent.Options
		wantThinking      bool
		wantBudget        int
		wantErr           bool
		wantNoHTTPRequest bool
	}{
		{
			name:            "unset preserves provider config",
			configReasoning: agent.ReasoningTokens(8192),
			wantThinking:    true,
			wantBudget:      8192,
		},
		{
			name:            "explicit off suppresses provider config",
			configReasoning: agent.ReasoningTokens(8192),
			opts:            agent.Options{Reasoning: agent.ReasoningOff},
		},
		{
			name:            "numeric zero suppresses provider config",
			configReasoning: agent.ReasoningTokens(8192),
			opts:            agent.Options{Reasoning: agent.ReasoningTokens(0)},
		},
		{
			name:            "explicit request wins over provider default",
			configReasoning: agent.ReasoningTokens(8192),
			opts:            agent.Options{Reasoning: agent.ReasoningTokens(1234)},
			wantThinking:    true,
			wantBudget:      1234,
		},
		{
			name:         "low maps to portable budget",
			opts:         agent.Options{Reasoning: agent.ReasoningLow},
			wantThinking: true,
			wantBudget:   2048,
		},
		{
			name:         "medium maps to portable budget",
			opts:         agent.Options{Reasoning: agent.ReasoningMedium},
			wantThinking: true,
			wantBudget:   8192,
		},
		{
			name:         "high maps to portable budget",
			opts:         agent.Options{Reasoning: agent.ReasoningHigh},
			wantThinking: true,
			wantBudget:   32768,
		},
		{
			name:         "numeric tokens pass through",
			opts:         agent.Options{Reasoning: agent.ReasoningTokens(4321)},
			wantThinking: true,
			wantBudget:   4321,
		},
		{
			name:              "unsupported extended value fails before request",
			opts:              agent.Options{Reasoning: agent.ReasoningXHigh},
			wantErr:           true,
			wantNoHTTPRequest: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/chat", func(t *testing.T) {
			body, err := captureOpenAIChatBody(t, "thinking-map", tt.configReasoning, tt.opts)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantNoHTTPRequest {
					assert.Nil(t, body)
				}
				return
			}
			require.NoError(t, err)
			assertReasoningWireBudget(t, body, tt.wantThinking, tt.wantBudget)
		})
		t.Run(tt.name+"/stream", func(t *testing.T) {
			body, err := captureOpenAIStreamBody(t, "thinking-map", tt.configReasoning, tt.opts)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantNoHTTPRequest {
					assert.Nil(t, body)
				}
				return
			}
			require.NoError(t, err)
			assertReasoningWireBudget(t, body, tt.wantThinking, tt.wantBudget)
		})
	}
}

func TestReasoningSerializationUnsupportedProviders(t *testing.T) {
	for _, providerType := range []string{"openai", "ollama"} {
		t.Run(providerType+"/default provider budget drops", func(t *testing.T) {
			body, err := captureOpenAIChatBody(t, providerType, agent.ReasoningTokens(8192), agent.Options{})
			require.NoError(t, err)
			assertReasoningWireBudget(t, body, false, 0)
		})
		t.Run(providerType+"/explicit request fails before serialization", func(t *testing.T) {
			body, err := captureOpenAIChatBody(t, providerType, "", agent.Options{Reasoning: agent.ReasoningLow})
			require.Error(t, err)
			assert.Nil(t, body)
		})
	}
}

func TestOpenRouterReasoningSerialization(t *testing.T) {
	tests := []struct {
		name          string
		opts          agent.Options
		wantEffort    string
		wantMaxTokens int
	}{
		{
			name:       "medium maps to nested effort",
			opts:       agent.Options{Reasoning: agent.ReasoningMedium},
			wantEffort: "medium",
		},
		{
			name:       "explicit off maps to effort none",
			opts:       agent.Options{Reasoning: agent.ReasoningOff},
			wantEffort: "none",
		},
		{
			name:       "max maps to xhigh effort",
			opts:       agent.Options{Reasoning: agent.ReasoningMax},
			wantEffort: "xhigh",
		},
		{
			name:          "numeric budget maps to max_tokens",
			opts:          agent.Options{Reasoning: agent.ReasoningTokens(4321)},
			wantMaxTokens: 4321,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/chat", func(t *testing.T) {
			body, err := captureOpenAIChatBody(t, "openrouter", "", tt.opts)
			require.NoError(t, err)
			assertOpenRouterReasoningWire(t, body, tt.wantEffort, tt.wantMaxTokens)
		})
		t.Run(tt.name+"/stream", func(t *testing.T) {
			body, err := captureOpenAIStreamBody(t, "openrouter", "", tt.opts)
			require.NoError(t, err)
			assertOpenRouterReasoningWire(t, body, tt.wantEffort, tt.wantMaxTokens)
		})
	}
}

func TestQwenReasoningSerialization(t *testing.T) {
	tests := []struct {
		name              string
		configReasoning   agent.Reasoning
		opts              agent.Options
		wantEnabled       bool
		wantBudget        int
		wantAbsent        bool
		wantErr           bool
		wantNoHTTPRequest bool
	}{
		{
			name:       "unset omits qwen reasoning fields",
			wantAbsent: true,
		},
		{
			name:        "low maps to qwen thinking budget",
			opts:        agent.Options{Reasoning: agent.ReasoningLow},
			wantEnabled: true,
			wantBudget:  2048,
		},
		{
			name:            "provider default sends qwen thinking budget",
			configReasoning: agent.ReasoningMedium,
			wantEnabled:     true,
			wantBudget:      8192,
		},
		{
			name:        "high maps to qwen thinking budget",
			opts:        agent.Options{Reasoning: agent.ReasoningHigh},
			wantEnabled: true,
			wantBudget:  32768,
		},
		{
			name:        "numeric budget maps to qwen thinking budget",
			opts:        agent.Options{Reasoning: agent.ReasoningTokens(4321)},
			wantEnabled: true,
			wantBudget:  4321,
		},
		{
			name:            "explicit off disables qwen thinking",
			configReasoning: agent.ReasoningMedium,
			opts:            agent.Options{Reasoning: agent.ReasoningOff},
			wantEnabled:     false,
			wantBudget:      0,
		},
		{
			name:              "unsupported extended value fails before request",
			opts:              agent.Options{Reasoning: agent.ReasoningXHigh},
			wantErr:           true,
			wantNoHTTPRequest: true,
		},
	}
	for _, providerType := range []string{"omlx", "lmstudio"} {
		for _, tt := range tests {
			t.Run(providerType+"/"+tt.name+"/chat", func(t *testing.T) {
				body, err := captureOpenAIChatBody(t, providerType, tt.configReasoning, tt.opts)
				if tt.wantErr {
					require.Error(t, err)
					if tt.wantNoHTTPRequest {
						assert.Nil(t, body)
					}
					return
				}
				require.NoError(t, err)
				if tt.wantAbsent {
					assertNoQwenReasoningWire(t, body)
					return
				}
				assertQwenReasoningWireBudget(t, body, tt.wantEnabled, tt.wantBudget)
			})
			t.Run(providerType+"/"+tt.name+"/stream", func(t *testing.T) {
				body, err := captureOpenAIStreamBody(t, providerType, tt.configReasoning, tt.opts)
				if tt.wantErr {
					require.Error(t, err)
					if tt.wantNoHTTPRequest {
						assert.Nil(t, body)
					}
					return
				}
				require.NoError(t, err)
				if tt.wantAbsent {
					assertNoQwenReasoningWire(t, body)
					return
				}
				assertQwenReasoningWireBudget(t, body, tt.wantEnabled, tt.wantBudget)
			})
		}
	}
}

// TestQwenReasoningSerializationRejectsNonQwenModels covers strict providers
// (OMLX): a Qwen-wire provider that only hosts Qwen models must fail the
// request when an explicit reasoning policy is sent against a non-Qwen
// model, so misconfiguration surfaces loudly instead of silently sending a
// control the template will ignore.
func TestQwenReasoningSerializationRejectsNonQwenModels(t *testing.T) {
	for _, opts := range []agent.Options{
		{Reasoning: agent.ReasoningMedium},
		{Reasoning: agent.ReasoningOff},
	} {
		t.Run(string(opts.Reasoning)+"/chat", func(t *testing.T) {
			body, err := captureOpenAIChatBodyWithModel(t, "omlx", "gpt-oss-20b-MXFP4-Q8", "", opts)
			require.Error(t, err)
			assert.Nil(t, body)
			assert.Contains(t, err.Error(), "qwen reasoning control")
			assert.Contains(t, err.Error(), "gpt-oss-20b")
		})
		t.Run(string(opts.Reasoning)+"/stream", func(t *testing.T) {
			body, err := captureOpenAIStreamBodyWithModel(t, "omlx", "gpt-oss-20b-MXFP4-Q8", "", opts)
			require.Error(t, err)
			assert.Nil(t, body)
			assert.Contains(t, err.Error(), "qwen reasoning control")
			assert.Contains(t, err.Error(), "gpt-oss-20b")
		})
	}
}

// TestQwenReasoningSerializationLenientOnNonStrictProviders covers mixed-family
// providers (LM Studio): a Qwen-wire provider that may host non-Qwen models
// must silently strip Qwen-specific fields for those models rather than
// rejecting the request. This preserves pre-existing CLI behavior where a
// catalog reasoning default can flow through to any LM Studio-hosted model.
func TestQwenReasoningSerializationLenientOnNonStrictProviders(t *testing.T) {
	for _, opts := range []agent.Options{
		{Reasoning: agent.ReasoningMedium},
		{Reasoning: agent.ReasoningOff},
		{Reasoning: agent.ReasoningTokens(4321)},
	} {
		t.Run(string(opts.Reasoning)+"/chat", func(t *testing.T) {
			body, err := captureOpenAIChatBodyWithModel(t, "lmstudio", "google/gemma-3-27b", "", opts)
			require.NoError(t, err)
			assertNoQwenReasoningWire(t, body)
		})
		t.Run(string(opts.Reasoning)+"/stream", func(t *testing.T) {
			body, err := captureOpenAIStreamBodyWithModel(t, "lmstudio", "google/gemma-3-27b", "", opts)
			require.NoError(t, err)
			assertNoQwenReasoningWire(t, body)
		})
	}
}

func TestSamplingOptionsSerialization(t *testing.T) {
	temperature := 0.25
	opts := agent.Options{Temperature: &temperature, Seed: 12345}

	t.Run("chat", func(t *testing.T) {
		body, err := captureOpenAIChatBody(t, "openai", "", opts)
		require.NoError(t, err)
		assertSamplingWireOptions(t, body, temperature, 12345)
	})

	t.Run("stream", func(t *testing.T) {
		body, err := captureOpenAIStreamBody(t, "openai", "", opts)
		require.NoError(t, err)
		assertSamplingWireOptions(t, body, temperature, 12345)
	})
}

func captureOpenAIChatBody(t *testing.T, providerType string, providerReasoning agent.Reasoning, opts agent.Options) ([]byte, error) {
	return captureOpenAIChatBodyWithModel(t, providerType, testModelForProvider(providerType), providerReasoning, opts)
}

func captureOpenAIChatBodyWithModel(t *testing.T, providerType string, model string, providerReasoning agent.Reasoning, opts agent.Options) ([]byte, error) {
	t.Helper()
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
		BaseURL:        srv.URL + "/v1",
		APIKey:         "test",
		Model:          model,
		ProviderSystem: providerType,
		Capabilities:   capabilitiesForTestProvider(providerType),
		Reasoning:      providerReasoning,
	})
	_, err := p.Chat(context.Background(), []agent.Message{{Role: agent.RoleUser, Content: "hello"}}, nil, opts)
	return capturedBody, err
}

func captureOpenAIStreamBody(t *testing.T, providerType string, providerReasoning agent.Reasoning, opts agent.Options) ([]byte, error) {
	return captureOpenAIStreamBodyWithModel(t, providerType, testModelForProvider(providerType), providerReasoning, opts)
}

func captureOpenAIStreamBodyWithModel(t *testing.T, providerType string, model string, providerReasoning agent.Reasoning, opts agent.Options) ([]byte, error) {
	t.Helper()
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
		BaseURL:        srv.URL + "/v1",
		APIKey:         "test",
		Model:          model,
		ProviderSystem: providerType,
		Capabilities:   capabilitiesForTestProvider(providerType),
		Reasoning:      providerReasoning,
	})
	ch, err := p.ChatStream(context.Background(), []agent.Message{{Role: agent.RoleUser, Content: "hello"}}, nil, opts)
	if err != nil {
		return capturedBody, err
	}
	for delta := range ch {
		if delta.Err != nil {
			return capturedBody, delta.Err
		}
	}
	return capturedBody, nil
}

func testModelForProvider(providerType string) string {
	switch providerType {
	case "omlx":
		return "Qwen3.6-27B-MLX-8bit"
	case "lmstudio":
		return "qwen/qwen3.6-35b-a3b"
	case "thinking-map":
		return "anthropic-compat-claude"
	}
	return "gpt-4o"
}

func capabilitiesForTestProvider(providerType string) *openai.ProtocolCapabilities {
	caps := openai.OpenAIProtocolCapabilities
	switch providerType {
	case "lmstudio":
		caps.Thinking = true
		caps.ThinkingFormat = openai.ThinkingWireFormatQwen
	case "omlx":
		caps.Thinking = true
		caps.ThinkingFormat = openai.ThinkingWireFormatQwen
		caps.StrictThinkingModelMatch = true
	case "openrouter":
		caps.Thinking = true
		caps.ThinkingFormat = openai.ThinkingWireFormatOpenRouter
	case "ollama":
		caps.StructuredOutput = false
	case "thinking-map":
		caps.Thinking = true
		caps.ThinkingFormat = openai.ThinkingWireFormatThinkingMap
	}
	return &caps
}

func assertReasoningWireBudget(t *testing.T, body []byte, wantThinking bool, wantBudget int) {
	t.Helper()
	require.NotNil(t, body)
	var reqBody map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &reqBody))
	thinking, ok := reqBody["thinking"].(map[string]interface{})
	if !wantThinking {
		assert.False(t, ok, "request body must not include thinking: %s", string(body))
		return
	}
	require.True(t, ok, "request body must include thinking: %s", string(body))
	assert.Equal(t, "enabled", thinking["type"])
	assert.Equal(t, float64(wantBudget), thinking["budget_tokens"])
}

func assertOpenRouterReasoningWire(t *testing.T, body []byte, wantEffort string, wantMaxTokens int) {
	t.Helper()
	require.NotNil(t, body)
	var reqBody map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &reqBody))
	reasoning, ok := reqBody["reasoning"].(map[string]interface{})
	require.True(t, ok, "request body must include reasoning: %s", string(body))
	if wantEffort != "" {
		assert.Equal(t, wantEffort, reasoning["effort"])
		assert.NotContains(t, reasoning, "max_tokens")
	} else {
		assert.Equal(t, float64(wantMaxTokens), reasoning["max_tokens"])
		assert.NotContains(t, reasoning, "effort")
	}
	assert.NotContains(t, reqBody, "thinking")
	assert.NotContains(t, reqBody, "reasoning_effort")
}

func assertQwenReasoningWireBudget(t *testing.T, body []byte, wantEnabled bool, wantBudget int) {
	t.Helper()
	require.NotNil(t, body)
	var reqBody map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &reqBody))
	assert.Equal(t, wantEnabled, reqBody["enable_thinking"])
	assert.Equal(t, float64(wantBudget), reqBody["thinking_budget"])
	if _, ok := reqBody["thinking"]; ok {
		t.Fatalf("qwen reasoning controls must not use thinking map: %s", string(body))
	}
}

func assertNoQwenReasoningWire(t *testing.T, body []byte) {
	t.Helper()
	require.NotNil(t, body)
	var reqBody map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &reqBody))
	assert.NotContains(t, reqBody, "enable_thinking")
	assert.NotContains(t, reqBody, "thinking_budget")
	assert.NotContains(t, reqBody, "thinking")
	assert.NotContains(t, reqBody, "reasoning")
}

func assertSamplingWireOptions(t *testing.T, body []byte, wantTemperature float64, wantSeed int64) {
	t.Helper()
	require.NotNil(t, body)
	var reqBody map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &reqBody))
	assert.Equal(t, wantTemperature, reqBody["temperature"])
	assert.Equal(t, float64(wantSeed), reqBody["seed"])
}

func TestNew_BaseURLControlsEndpointMetadataOnly(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		host    string
		port    int
	}{
		{
			name:    "lmstudio default local endpoint",
			baseURL: "http://localhost:1234/v1",
			host:    "localhost",
			port:    1234,
		},
		{
			name:    "ollama compatible endpoint",
			baseURL: "http://127.0.0.1:11434/v1",
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
			assert.Equal(t, "openai", system)
			assert.Equal(t, tt.host, host)
			assert.Equal(t, tt.port, port)
		})
	}
}

func TestNew_ExplicitProviderSystemControlsIdentity(t *testing.T) {
	p := openai.New(openai.Config{
		BaseURL:        "http://vidar:1234/v1",
		Model:          "qwen",
		ProviderName:   "studio",
		ProviderSystem: "lmstudio",
	})

	system, host, port := p.ChatStartMetadata()
	assert.Equal(t, "lmstudio", system)
	assert.Equal(t, "vidar", host)
	assert.Equal(t, 1234, port)
}
