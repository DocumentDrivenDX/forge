package openai_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
