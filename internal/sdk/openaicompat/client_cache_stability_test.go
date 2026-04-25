package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureServer returns an httptest server that records every request body it
// receives, plus a function to retrieve the captured bodies in arrival order.
// The server replies with a minimal valid Chat Completions JSON response so the
// openai-go client can decode without error.
func captureServer(t *testing.T) (*httptest.Server, func() [][]byte) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		_ = r.Body.Close()
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 0,
			"model":   "test-model",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
		})
	}))
	t.Cleanup(srv.Close)
	get := func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]byte, len(bodies))
		copy(out, bodies)
		return out
	}
	return srv, get
}

// fixtureTools returns a complex tool fixture: nested object schema, required
// fields, and per-property descriptions. This exercises any non-determinism in
// JSON-schema serialization at the openai-compat layer.
func fixtureTools() []agent.ToolDef {
	read := json.RawMessage(`{
		"type": "object",
		"description": "Read a file from disk.",
		"properties": {
			"path": {"type": "string", "description": "Absolute path to the file."},
			"offset": {"type": "integer", "description": "1-indexed line to start reading from."},
			"limit": {"type": "integer", "description": "Maximum number of lines to read."}
		},
		"required": ["path"]
	}`)
	write := json.RawMessage(`{
		"type": "object",
		"description": "Write content to a file on disk.",
		"properties": {
			"path": {"type": "string", "description": "Absolute path to the file."},
			"content": {"type": "string", "description": "Full file content to write."},
			"options": {
				"type": "object",
				"description": "Write options.",
				"properties": {
					"create_parents": {"type": "boolean", "description": "Create parent directories if missing."},
					"mode": {"type": "string", "description": "File mode in octal."}
				},
				"required": ["create_parents"]
			}
		},
		"required": ["path", "content"]
	}`)
	search := json.RawMessage(`{
		"type": "object",
		"description": "Search for a regex pattern across files.",
		"properties": {
			"pattern": {"type": "string", "description": "Regex pattern."},
			"glob": {"type": "string", "description": "Glob filter."},
			"max_results": {"type": "integer", "description": "Result limit."}
		},
		"required": ["pattern"]
	}`)
	return []agent.ToolDef{
		{Name: "read_file", Description: "Read a file from disk.", Parameters: read},
		{Name: "write_file", Description: "Write content to disk.", Parameters: write},
		{Name: "search", Description: "Search files via regex.", Parameters: search},
	}
}

// commonPrefix returns the length of the longest shared byte prefix between a
// and b.
func commonPrefix(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// TestOpenAIRequestPrefixIsByteStableAcrossTurns is the primary cache-stability
// regression. OpenAI's automatic prefix caching keys on byte-identical request
// prefixes; any drift in tool serialization, system message encoding, or
// conversation prefix encoding silently invalidates the cache. This test asserts
// at the wire level — not at marshal-equality level — that two calls sharing
// everything except the trailing user message produce HTTP bodies that are
// byte-identical up to the boundary of that trailing message.
func TestOpenAIRequestPrefixIsByteStableAcrossTurns(t *testing.T) {
	srv, getBodies := captureServer(t)
	client := NewClient(Config{BaseURL: srv.URL + "/v1"})

	tools := fixtureTools()
	systemMsg := agent.Message{Role: agent.RoleSystem, Content: "You are a careful assistant. Use tools when helpful."}
	turn1User := agent.Message{Role: agent.RoleUser, Content: "What does the function foo() do?"}
	assistantReply := agent.Message{Role: agent.RoleAssistant, Content: "Let me check."}

	// Call 1: first turn — system + user.
	msgs1 := []agent.Message{systemMsg, turn1User}
	// Call 2: identical prefix [system, user, assistant], differing trailing user.
	msgs2a := []agent.Message{systemMsg, turn1User, assistantReply, {Role: agent.RoleUser, Content: "AAAAAAAAAAAAAAAAAAAA"}}
	msgs2b := []agent.Message{systemMsg, turn1User, assistantReply, {Role: agent.RoleUser, Content: "BBBBBBBBBBBBBBBBBBBB"}}

	model := "test-model"
	opts := RequestOptions{MaxTokens: 16}

	_, err := client.Chat(context.Background(), model, msgs2a, tools, opts)
	require.NoError(t, err)
	_, err = client.Chat(context.Background(), model, msgs2b, tools, opts)
	require.NoError(t, err)

	bodies := getBodies()
	require.Len(t, bodies, 2, "expected two captured request bodies")

	// Sanity: the bodies must differ somewhere — the trailing user message
	// content is different by construction.
	require.NotEqual(t, bodies[0], bodies[1], "fixture broken: trailing message change should produce different bodies")

	// Prefix stability: every byte before the differing trailing user message
	// must be identical. We locate the boundary by searching for the divergent
	// content marker in each body.
	idxA := bytes.Index(bodies[0], []byte("AAAAAAAAAAAAAAAAAAAA"))
	idxB := bytes.Index(bodies[1], []byte("BBBBBBBBBBBBBBBBBBBB"))
	require.NotEqual(t, -1, idxA, "trailing user content not found in first body")
	require.NotEqual(t, -1, idxB, "trailing user content not found in second body")
	require.Equal(t, idxA, idxB, "differing trailing message must appear at the same byte offset in both bodies")

	prefixLen := idxA
	assert.Equal(t, bodies[0][:prefixLen], bodies[1][:prefixLen],
		"OpenAI auto prefix-cache requires byte-identical request prefixes; the openai-compat layer produced divergent prefixes for the same tools+system+history")

	// Belt-and-suspenders: also assert against the smallest pure-prefix the
	// first call (which lacks the assistant+trailing-user turn) and the second
	// share. Both must contain the system message and tools serialized
	// identically up to whatever messages they have in common.
	_, err = client.Chat(context.Background(), model, msgs1, tools, opts)
	require.NoError(t, err)
	bodies = getBodies()
	require.Len(t, bodies, 3)
	// Up to the first user message marker, calls 1 and 2 must agree.
	marker := []byte("What does the function foo() do?")
	idx0 := bytes.Index(bodies[0], marker)
	idx2 := bytes.Index(bodies[2], marker)
	require.NotEqual(t, -1, idx0)
	require.NotEqual(t, -1, idx2)
	assert.Equal(t, idx0, idx2, "system+tools prefix must serialize to the same length across calls")
	assert.Equal(t, bodies[0][:idx0], bodies[2][:idx2],
		"system+tools prefix bytes must be identical regardless of subsequent messages")
}

// TestOpenAIRequestPreservesToolOrder is a negative test: swapping two tools in
// the input slice must produce a different wire body. This proves that tool
// order is preserved (so callers control prefix stability by controlling input
// order) — and incidentally rules out any silent re-sorting at the openai-compat
// layer that would mask the prefix-stability assertion above.
func TestOpenAIRequestPreservesToolOrder(t *testing.T) {
	cases := []struct {
		name   string
		swapAt [2]int
	}{
		{name: "swap_first_two", swapAt: [2]int{0, 1}},
		{name: "swap_last_two", swapAt: [2]int{1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, getBodies := captureServer(t)
			client := NewClient(Config{BaseURL: srv.URL + "/v1"})

			tools := fixtureTools()
			swapped := append([]agent.ToolDef(nil), tools...)
			swapped[tc.swapAt[0]], swapped[tc.swapAt[1]] = swapped[tc.swapAt[1]], swapped[tc.swapAt[0]]

			msgs := []agent.Message{
				{Role: agent.RoleSystem, Content: "system"},
				{Role: agent.RoleUser, Content: "hi"},
			}
			opts := RequestOptions{MaxTokens: 16}

			_, err := client.Chat(context.Background(), "test-model", msgs, tools, opts)
			require.NoError(t, err)
			_, err = client.Chat(context.Background(), "test-model", msgs, swapped, opts)
			require.NoError(t, err)

			bodies := getBodies()
			require.Len(t, bodies, 2)

			require.NotEqual(t, bodies[0], bodies[1],
				"swapping tool order must produce a different wire body; openai-compat layer is reordering or dropping ordering information")

			// The divergence must occur at or before the first swapped tool's
			// name appears in either body. Compute the common-prefix length and
			// the position of the earlier-of-the-swapped tool name; the
			// divergence point must be at most that position.
			cp := commonPrefix(bodies[0], bodies[1])
			earlierName := []byte(tools[tc.swapAt[0]].Name)
			pos0 := bytes.Index(bodies[0], earlierName)
			pos1 := bytes.Index(bodies[1], earlierName)
			require.NotEqual(t, -1, pos0)
			require.NotEqual(t, -1, pos1)
			minPos := pos0
			if pos1 < minPos {
				minPos = pos1
			}
			assert.LessOrEqual(t, cp, minPos+len(earlierName),
				"divergence between bodies must be in the tools region near the swap, not somewhere unrelated")
		})
	}
}
