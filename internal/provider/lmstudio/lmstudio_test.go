package lmstudio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lmStudioServer(loaded, max int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v0/models/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                    strings.TrimPrefix(r.URL.Path, "/api/v0/models/"),
			"loaded_context_length": loaded,
			"max_context_length":    max,
		})
	}))
}

func TestLookupModelLimits_PrefersLoadedContextLength(t *testing.T) {
	srv := lmStudioServer(100_000, 131_072)
	defer srv.Close()

	got := LookupModelLimits(context.Background(), srv.URL+"/v1", "qwen3.5-27b")
	assert.Equal(t, 100_000, got.ContextLength)
	assert.Equal(t, 0, got.MaxCompletionTokens)
}

func TestLookupModelLimits_FallsBackToMaxContextLength(t *testing.T) {
	srv := lmStudioServer(0, 131_072)
	defer srv.Close()

	got := LookupModelLimits(context.Background(), srv.URL+"/v1", "qwen3.5-27b")
	assert.Equal(t, 131_072, got.ContextLength)
}

func TestProtocolCapabilities(t *testing.T) {
	p := New(Config{BaseURL: "http://localhost:1234/v1"})
	assert.True(t, p.SupportsTools())
	assert.True(t, p.SupportsStream())
	assert.True(t, p.SupportsStructuredOutput())
	assert.True(t, p.SupportsThinking())
}

// TestProtocolCapabilities_UsesQwenWireFormat pins the reasoning wire shape
// that LM Studio advertises. Non-Qwen LM Studio models have reasoning fields
// stripped by the openai layer; for Qwen models the Qwen family controls are
// sent even though the Bragi-hosted `qwen/qwen3.6-35b-a3b` GGUF chat template
// does not honor them (see scripts/beadbench/README.md for evidence).
func TestProtocolCapabilities_UsesQwenWireFormat(t *testing.T) {
	assert.Equal(t, openai.ThinkingWireFormatQwen, ProtocolCapabilities.ThinkingFormat)
}

// bodyCapturingServer returns an httptest server that records the last
// /chat/completions request body and replies with a minimal success payload.
func bodyCapturingServer(t *testing.T, captured *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*captured = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"qwen/qwen3.6-35b-a3b",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`))
	}))
}

// TestReasoningSerialization_QwenModelSendsQwenControls verifies the bead's AC
// that ReasoningOff emits an actual disable signal on the wire
// (`enable_thinking=false`, `thinking_budget=0`) and that a non-off budget
// emits `enable_thinking=true` with the requested budget. LM Studio is
// configured for Qwen wire format so these fields are present for Qwen
// models only.
func TestReasoningSerialization_QwenModelSendsQwenControls(t *testing.T) {
	cases := []struct {
		name        string
		reasoning   agent.Reasoning
		wantEnabled bool
		wantBudget  int
		wantAbsent  bool
	}{
		{name: "off sends disable signal", reasoning: agent.ReasoningOff, wantEnabled: false, wantBudget: 0},
		{name: "low maps to qwen budget", reasoning: agent.ReasoningLow, wantEnabled: true, wantBudget: 2048},
		{name: "medium maps to qwen budget", reasoning: agent.ReasoningMedium, wantEnabled: true, wantBudget: 8192},
		{name: "numeric tokens pass through", reasoning: agent.ReasoningTokens(321), wantEnabled: true, wantBudget: 321},
		{name: "unset omits qwen fields", reasoning: agent.Reasoning(""), wantAbsent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured []byte
			srv := bodyCapturingServer(t, &captured)
			defer srv.Close()

			p := New(Config{
				BaseURL: srv.URL + "/v1",
				APIKey:  "test",
				Model:   "qwen/qwen3.6-35b-a3b",
			})
			opts := agent.Options{Reasoning: tc.reasoning}
			_, err := p.Chat(context.Background(), []agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, opts)
			require.NoError(t, err)
			require.NotNil(t, captured)

			var body map[string]any
			require.NoError(t, json.Unmarshal(captured, &body))

			if tc.wantAbsent {
				assert.NotContains(t, body, "enable_thinking")
				assert.NotContains(t, body, "thinking_budget")
				assert.NotContains(t, body, "thinking")
				return
			}
			assert.Equal(t, tc.wantEnabled, body["enable_thinking"], "enable_thinking must match: %s", string(captured))
			assert.Equal(t, float64(tc.wantBudget), body["thinking_budget"], "thinking_budget must match: %s", string(captured))
			_, thinkingPresent := body["thinking"]
			assert.False(t, thinkingPresent, "qwen wire must not send thinking map: %s", string(captured))
		})
	}
}

// TestReasoningSerialization_NonQwenModelStripsQwenControls verifies that
// non-Qwen models served by LM Studio do not carry Qwen-specific reasoning
// fields. This preserves the invariant that Qwen wire controls are only
// emitted for Qwen-family models.
func TestReasoningSerialization_NonQwenModelStripsQwenControls(t *testing.T) {
	var captured []byte
	srv := bodyCapturingServer(t, &captured)
	defer srv.Close()

	p := New(Config{
		BaseURL:   srv.URL + "/v1",
		APIKey:    "test",
		Model:     "google/gemma-3-27b",
		Reasoning: reasoning.ReasoningMedium,
	})
	_, err := p.Chat(context.Background(), []agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.Options{})
	require.NoError(t, err)
	require.NotNil(t, captured)

	var body map[string]any
	require.NoError(t, json.Unmarshal(captured, &body))
	assert.NotContains(t, body, "enable_thinking")
	assert.NotContains(t, body, "thinking_budget")
	assert.NotContains(t, body, "thinking")
}
