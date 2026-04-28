package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/provider/conformance"
)

type openAICompatDescriptor struct {
	name              string
	providerType      string
	model             string
	supportsThinking  bool
	supportsToolCalls bool
}

func TestConformance_OpenAICompatShapedDoubles(t *testing.T) {
	descriptors := []openAICompatDescriptor{
		{name: "omlx", providerType: "omlx", model: "qwen3-omlx", supportsToolCalls: true},
		{name: "lmstudio", providerType: "lmstudio", model: "qwen3-lmstudio", supportsThinking: true, supportsToolCalls: true},
		{name: "openrouter", providerType: "openrouter", model: "openai/gpt-4o-mini", supportsToolCalls: true},
		{name: "openai", providerType: "openai", model: "gpt-4o-mini", supportsToolCalls: true},
		{name: "ollama", providerType: "ollama", model: "llama3.2", supportsToolCalls: true},
	}

	for _, desc := range descriptors {
		desc := desc
		t.Run(desc.name, func(t *testing.T) {
			conformance.Run(t, func(t *testing.T) conformance.Subject {
				t.Helper()
				srv, _ := newOpenAICompatConformanceServer(t, desc)
				t.Cleanup(srv.Close)

				p := New(Config{
					BaseURL:        srv.URL + "/v1",
					APIKey:         "test-key",
					ProviderName:   desc.name,
					ProviderSystem: desc.providerType,
					Capabilities:   protocolCapabilitiesForDescriptor(desc),
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
						if err := conformance.HTTPStatusError(resp.StatusCode); err != nil {
							return err
						}
						return nil
					},
					ListModels: func(ctx context.Context) ([]string, error) {
						if err := p.EnsureDiscovered(ctx); err != nil {
							return nil, err
						}
						models := p.DiscoveredModels()
						ids := make([]string, 0, len(models))
						for _, model := range models {
							ids = append(ids, model.ID)
						}
						return ids, nil
					},
				}
			}, conformance.Capabilities{
				Name:              desc.name,
				ExpectedModels:    []string{desc.model},
				SupportsStreaming: true,
				SupportsThinking:  desc.supportsThinking,
				SupportsToolCalls: desc.supportsToolCalls,
				// Shaped-double fixture emits these deterministically.
				ChatContains:      "pong",
				StreamContains:    "stream-pong",
				ReasoningContains: "reasoning",
			})
		})
	}
}

func TestConformance_OpenAICompatLive(t *testing.T) {
	descriptors := []struct {
		name              string
		providerType      string
		urlEnv            string
		apiKeyEnv         string
		modelEnv          string
		defaultBaseURL    string
		defaultModel      string
		supportsThinking  bool
		supportsToolCalls bool
		// chatMaxTokens / scenarioTimeout override conformance.Capabilities
		// defaults for slow / thinking-mode local providers. Zero falls
		// back to the conformance package's defaults.
		chatMaxTokens   int
		scenarioTimeout time.Duration
	}{
		{name: "omlx", providerType: "omlx", urlEnv: "OMLX_URL", modelEnv: "OMLX_MODEL", supportsToolCalls: false},
		{name: "lmstudio", providerType: "lmstudio", urlEnv: "LMSTUDIO_URL", modelEnv: "LMSTUDIO_MODEL", supportsToolCalls: false},
		// lucebox serves Qwen3 with thinking-mode-on by default; defaults
		// (1024 tokens / 5min timeout) already accommodate this. Set
		// supportsThinking so the reasoning subtest runs.
		{name: "lucebox", providerType: "lucebox", urlEnv: "LUCEBOX_URL", modelEnv: "LUCEBOX_MODEL", supportsThinking: true, supportsToolCalls: true},
		{name: "vllm", providerType: "vllm", urlEnv: "VLLM_URL", modelEnv: "VLLM_MODEL", supportsToolCalls: true},
		{name: "openrouter", providerType: "openrouter", apiKeyEnv: "OPENROUTER_API_KEY", modelEnv: "OPENROUTER_MODEL", defaultBaseURL: "https://openrouter.ai/api/v1", defaultModel: "openai/gpt-4o-mini", supportsToolCalls: true},
		{name: "openai", providerType: "openai", apiKeyEnv: "OPENAI_API_KEY", modelEnv: "OPENAI_MODEL", defaultBaseURL: "https://api.openai.com/v1", defaultModel: "gpt-4o-mini", supportsToolCalls: true},
		{name: "ollama", providerType: "ollama", urlEnv: "OLLAMA_URL", modelEnv: "OLLAMA_MODEL", defaultBaseURL: "http://localhost:11434/v1", supportsToolCalls: false},
	}

	for _, desc := range descriptors {
		desc := desc
		t.Run(desc.name, func(t *testing.T) {
			baseURL := desc.defaultBaseURL
			if desc.urlEnv != "" {
				baseURL = strings.TrimSpace(os.Getenv(desc.urlEnv))
				if baseURL == "" {
					t.Skipf("%s is not set", desc.urlEnv)
				}
				baseURL = ensureV1BaseURL(baseURL)
			}
			apiKey := ""
			if desc.apiKeyEnv != "" {
				apiKey = strings.TrimSpace(os.Getenv(desc.apiKeyEnv))
				if apiKey == "" {
					t.Skipf("%s is not set", desc.apiKeyEnv)
				}
			}
			model := strings.TrimSpace(os.Getenv(desc.modelEnv))
			if model == "" {
				model = desc.defaultModel
			}
			if model == "" {
				discovered, err := DiscoverModels(context.Background(), baseURL, apiKey)
				if err != nil {
					t.Fatalf("discover %s live models: %v", desc.name, err)
				}
				if len(discovered) == 0 {
					t.Fatalf("%s live endpoint returned no models", desc.name)
				}
				model = discovered[0]
			}

			p := New(Config{
				BaseURL:        baseURL,
				APIKey:         apiKey,
				Model:          model,
				ProviderName:   desc.name,
				ProviderSystem: desc.providerType,
				Capabilities: &ProtocolCapabilities{
					Tools:            true,
					Stream:           true,
					StructuredOutput: desc.providerType != "ollama",
					Thinking:         desc.supportsThinking,
				},
			})
			if desc.providerType == "omlx" && p.SupportsThinking() {
				t.Fatal("omlx live capability contract requires SupportsThinking()==false")
			}

			conformance.Run(t, func(t *testing.T) conformance.Subject {
				t.Helper()
				return conformance.Subject{
					Provider: p,
					HealthCheck: func(ctx context.Context) error {
						_, err := DiscoverModels(ctx, baseURL, apiKey)
						return err
					},
					ListModels: func(ctx context.Context) ([]string, error) {
						return DiscoverModels(ctx, baseURL, apiKey)
					},
				}
			}, conformance.Capabilities{
				Name:              desc.name + "-live",
				ExpectedModels:    []string{model},
				SupportsStreaming: true,
				SupportsThinking:  desc.supportsThinking,
				SupportsToolCalls: desc.supportsToolCalls,
				ChatMaxTokens:     desc.chatMaxTokens,
				ScenarioTimeout:   desc.scenarioTimeout,
			})
		})
	}
}

func protocolCapabilitiesForDescriptor(desc openAICompatDescriptor) *ProtocolCapabilities {
	caps := OpenAIProtocolCapabilities
	caps.Thinking = desc.supportsThinking
	if desc.providerType == "ollama" {
		caps.StructuredOutput = false
	}
	return &caps
}

func newOpenAICompatConformanceServer(t *testing.T, desc openAICompatDescriptor) (*httptest.Server, *int32) {
	t.Helper()
	var statusHits int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			writeJSON(w, map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": desc.model, "object": "model"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models/status":
			atomic.AddInt32(&statusHits, 1)
			writeJSON(w, map[string]interface{}{
				"models": []map[string]interface{}{
					{"id": desc.model, "max_context_window": 262144, "max_tokens": 32768},
				},
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v0/models/"):
			writeJSON(w, map[string]interface{}{
				"id":                    desc.model,
				"loaded_context_length": 65536,
				"max_context_length":    262144,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			handleOpenAICompatChat(w, r, desc)
		default:
			http.NotFound(w, r)
		}
	})
	return httptest.NewServer(handler), &statusHits
}

func handleOpenAICompatChat(w http.ResponseWriter, r *http.Request, desc openAICompatDescriptor) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Stream    bool              `json:"stream"`
		MaxTokens int               `json:"max_tokens"`
		Messages  []openAIChatMsg   `json:"messages"`
		Tools     []json.RawMessage `json:"tools"`
		Thinking  json.RawMessage   `json:"thinking"`
	}
	_ = json.Unmarshal(body, &req)

	// Tool-result roundtrip: when the conversation already contains an
	// assistant tool_call followed by a tool message, the shaped double
	// returns final assistant content without re-emitting a tool call.
	// Mirrors what real servers do once the tool_call_id pairing closes.
	historyHasToolResult := false
	for _, m := range req.Messages {
		if m.Role == "tool" {
			historyHasToolResult = true
			break
		}
	}

	prompt := lastOpenAIPrompt(req.Messages)
	if !req.Stream {
		// Non-streaming tool call: when tools[] is present and history
		// has no prior tool result, return a tool_calls message instead
		// of plain content. Validates the non-streaming tool path.
		if len(req.Tools) > 0 && !historyHasToolResult {
			writeJSON(w, map[string]interface{}{
				"id":    "chatcmpl-conformance",
				"model": desc.model,
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []map[string]interface{}{{
							"id":   "call_inspect_nonstream",
							"type": "function",
							"function": map[string]string{
								"name":      "inspect",
								"arguments": `{"target":"fixture"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": openAIUsage(desc, 4, 3),
			})
			return
		}
		writeJSON(w, map[string]interface{}{
			"id":      "chatcmpl-conformance",
			"model":   desc.model,
			"choices": []map[string]interface{}{{"index": 0, "message": map[string]string{"role": "assistant", "content": "pong"}, "finish_reason": "stop"}},
			"usage":   openAIUsage(desc, 4, 1),
		})
		return
	}

	// Streaming + tool_result roundtrip: emit visible content only, no
	// tool_calls. The tool_call_id round-trip already happened on the
	// previous turn; this validates the follow-up pathway closes cleanly.
	if historyHasToolResult {
		writeOpenAISSE(w, desc, []string{
			openAIChunk(desc, "tool result acknowledged", "", "", nil),
			openAIChunk(desc, "", "", "stop", openAIUsage(desc, 6, 3)),
		})
		return
	}

	if len(req.Tools) > 0 {
		writeOpenAISSE(w, desc, []string{
			`{"id":"chatcmpl-conformance","model":"` + desc.model + `","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_inspect","type":"function","function":{"name":"inspect","arguments":""}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-conformance","model":"","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"target\":"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-conformance","model":"","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"fixture\"}"}}]},"finish_reason":null}]}`,
			openAIChunk(desc, "", "", "tool_calls", openAIUsage(desc, 7, 3)),
		})
		return
	}

	content := "stream-pong"
	if strings.Contains(prompt, "max tokens") {
		content = "one two three"
	}
	reasoning := ""
	if len(req.Thinking) > 0 {
		reasoning = "reasoning tokens"
	}
	writeOpenAISSE(w, desc, []string{
		openAIChunk(desc, content, reasoning, "", nil),
		openAIChunk(desc, "", "", "stop", openAIUsage(desc, 5, len(strings.Fields(content)))),
	})
}

type openAIChatMsg struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

func lastOpenAIPrompt(messages []openAIChatMsg) string {
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

func writeOpenAISSE(w http.ResponseWriter, desc openAICompatDescriptor, chunks []string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	if desc.providerType == "omlx" {
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
	for _, chunk := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
		if desc.providerType == "omlx" {
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func openAIChunk(desc openAICompatDescriptor, content, reasoning, finishReason string, usage map[string]interface{}) string {
	chunk := map[string]interface{}{
		"id":    "chatcmpl-conformance",
		"model": desc.model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]string{
					"content":           content,
					"reasoning_content": reasoning,
				},
				"finish_reason": nullableString(finishReason),
			},
		},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	data, _ := json.Marshal(chunk)
	return string(data)
}

func openAIUsage(desc openAICompatDescriptor, promptTokens, completionTokens int) map[string]interface{} {
	usage := map[string]interface{}{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
	if desc.providerType == "openrouter" {
		usage["cost"] = 0.000001
	}
	return usage
}

func nullableString(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func ensureV1BaseURL(raw string) string {
	raw = strings.TrimRight(raw, "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" || strings.HasSuffix(parsed.Path, "/v1") {
		return raw
	}
	return raw + "/v1"
}
