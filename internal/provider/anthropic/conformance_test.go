package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/provider/conformance"
)

func TestConformance_AnthropicShapedDouble(t *testing.T) {
	const model = "claude-sonnet-4-20250514"

	conformance.Run(t, func(t *testing.T) conformance.Subject {
		t.Helper()
		srv := newAnthropicConformanceServer(model)
		t.Cleanup(srv.Close)
		p := New(Config{
			APIKey:  "test-key",
			Model:   model,
			BaseURL: srv.URL,
		})
		return conformance.Subject{
			Provider: p,
			HealthCheck: func(ctx context.Context) error {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/models", nil)
				if err != nil {
					return err
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				return conformance.HTTPStatusError(resp.StatusCode)
			},
			ListModels: func(ctx context.Context) ([]string, error) {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/models", nil)
				if err != nil {
					return nil, err
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return nil, err
				}
				defer resp.Body.Close()
				if err := conformance.HTTPStatusError(resp.StatusCode); err != nil {
					return nil, err
				}
				var parsed struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
					return nil, err
				}
				models := make([]string, 0, len(parsed.Data))
				for _, entry := range parsed.Data {
					models = append(models, entry.ID)
				}
				return models, nil
			},
		}
	}, conformance.Capabilities{
		Name:              "anthropic",
		ExpectedModels:    []string{model},
		SupportsStreaming: true,
		SupportsToolCalls: true,
		// Shaped-double fixture emits these literals deterministically.
		ChatContains:   "pong",
		StreamContains: "stream-pong",
	})
}

func TestConformance_AnthropicLive(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY is not set")
	}
	model := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL"))
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	conformance.Run(t, func(t *testing.T) conformance.Subject {
		t.Helper()
		p := New(Config{
			APIKey: apiKey,
			Model:  model,
		})
		return conformance.Subject{
			Provider: p,
			HealthCheck: func(ctx context.Context) error {
				_, err := p.Chat(ctx, []agent.Message{{Role: agent.RoleUser, Content: "ping"}}, nil, agent.Options{MaxTokens: 1})
				return err
			},
			ListModels: func(context.Context) ([]string, error) {
				return []string{model}, nil
			},
		}
	}, conformance.Capabilities{
		Name:              "anthropic-live",
		ExpectedModels:    []string{model},
		SupportsStreaming: true,
		SupportsToolCalls: true,
	})
}

func newAnthropicConformanceServer(model string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			writeAnthropicJSON(w, map[string]interface{}{
				"data": []map[string]string{{"id": model, "type": "model"}},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "messages"):
			handleAnthropicConformanceMessage(w, r, model)
		default:
			http.NotFound(w, r)
		}
	}))
}

func handleAnthropicConformanceMessage(w http.ResponseWriter, r *http.Request, model string) {
	var req struct {
		Stream    bool              `json:"stream"`
		MaxTokens int               `json:"max_tokens"`
		Messages  []anthropicMsg    `json:"messages"`
		Tools     []json.RawMessage `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Tool-result roundtrip: Anthropic delivers tool results as user
	// messages with content blocks of type "tool_result". When the
	// conversation already contains one, the shaped double returns final
	// assistant text without re-emitting a tool_use block — mirrors what
	// the real API does once the tool_use_id pairing closes.
	historyHasToolResult := false
	for _, m := range req.Messages {
		blocks, ok := m.Content.([]interface{})
		if !ok {
			continue
		}
		for _, block := range blocks {
			obj, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := obj["type"].(string); t == "tool_result" {
				historyHasToolResult = true
				break
			}
		}
		if historyHasToolResult {
			break
		}
	}

	prompt := lastAnthropicPrompt(req.Messages)
	if !req.Stream {
		// Non-streaming tool call: when tools[] is present and history
		// has no prior tool result, return a tool_use content block.
		if len(req.Tools) > 0 && !historyHasToolResult {
			writeAnthropicJSON(w, map[string]interface{}{
				"id":   "msg_conformance",
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{{
					"type":  "tool_use",
					"id":    "toolu_inspect_nonstream",
					"name":  "inspect",
					"input": map[string]string{"target": "fixture"},
				}},
				"model":         model,
				"stop_reason":   "tool_use",
				"stop_sequence": nil,
				"usage":         map[string]int{"input_tokens": 4, "output_tokens": 3},
			})
			return
		}
		writeAnthropicJSON(w, map[string]interface{}{
			"id":            "msg_conformance",
			"type":          "message",
			"role":          "assistant",
			"content":       []map[string]string{{"type": "text", "text": "pong"}},
			"model":         model,
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": 4, "output_tokens": 1},
		})
		return
	}

	if len(req.Tools) > 0 && !historyHasToolResult {
		writeAnthropicSSE(w, []anthropicEvent{
			{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_conformance","type":"message","role":"assistant","model":%q},"usage":{"input_tokens":5}}`, model)},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_inspect","name":"inspect","input":{}}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"target\":\"fixture\"}"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":3}}`},
			{"message_stop", `{"type":"message_stop"}`},
		})
		return
	}

	content := "stream-pong"
	if strings.Contains(prompt, "max tokens") {
		content = "one two three"
	}
	writeAnthropicSSE(w, []anthropicEvent{
		{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_conformance","type":"message","role":"assistant","model":%q},"usage":{"input_tokens":5}}`, model)},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, content)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`},
		{"message_stop", `{"type":"message_stop"}`},
	})
}

type anthropicMsg struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

func lastAnthropicPrompt(messages []anthropicMsg) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		switch content := messages[i].Content.(type) {
		case string:
			return content
		case []interface{}:
			var parts []string
			for _, part := range content {
				if obj, ok := part.(map[string]interface{}); ok {
					if text, ok := obj["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return ""
}

type anthropicEvent struct {
	name string
	data string
}

func writeAnthropicSSE(w http.ResponseWriter, events []anthropicEvent) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for _, event := range events {
		fmt.Fprintf(w, "event: %s\n", event.name)
		fmt.Fprintf(w, "data: %s\n\n", event.data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func writeAnthropicJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
