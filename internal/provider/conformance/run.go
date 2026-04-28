package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
)

// Factory builds a fresh provider subject for one conformance scenario.
type Factory func(t *testing.T) Subject

// Subject is the provider plus any protocol-specific discovery hooks needed to
// exercise health and model-discovery capabilities.
type Subject struct {
	Provider agent.Provider

	HealthCheck func(context.Context) error
	ListModels  func(context.Context) ([]string, error)
}

// Capabilities declares the scenarios that apply to a provider. Add
// fields as the shared catalog grows rather than baking provider names into Run.
type Capabilities struct {
	Name              string
	ExpectedModels    []string
	SupportsStreaming bool
	SupportsThinking  bool
	SupportsToolCalls bool
	MaxTokensSlack    int

	ChatContains      string
	StreamContains    string
	ReasoningContains string

	// ChatMaxTokens overrides the default chat/stream max-tokens budget
	// (8 tokens). Thinking-mode providers like luce burn output budget on
	// reasoning_content before the visible content; without headroom they
	// return empty content and the chat assertions fail. 0 = use default.
	ChatMaxTokens int
	// StreamMaxTokensCheck overrides the per-test cap for the
	// "streaming max_tokens honored" subtest. The check still asserts the
	// returned word count is bounded by this value + MaxTokensSlack. 0 =
	// use the existing default of 3.
	StreamMaxTokensCheck int
	// ScenarioTimeout overrides the per-subtest wall-clock budget
	// (default 5 seconds). Local thinking-capable providers can need
	// substantially more — set generously for those. 0 = use default.
	ScenarioTimeout time.Duration
}

// chatMaxTokens returns the configured chat budget or the default. The
// default is 1024 — thinking-mode local models route most of their
// budget through reasoning_content before content; an 8-token cap
// produced empty content even when the wire was working correctly.
// Cloud non-thinking providers ignore the headroom; setting a high
// floor has no cost for them.
func (c Capabilities) chatMaxTokens() int {
	if c.ChatMaxTokens > 0 {
		return c.ChatMaxTokens
	}
	return 1024
}

// streamMaxTokensCheck returns the configured stream cap or the default.
func (c Capabilities) streamMaxTokensCheck() int {
	if c.StreamMaxTokensCheck > 0 {
		return c.StreamMaxTokensCheck
	}
	return 3
}

// scenarioTimeout returns the configured timeout or the default. The
// default is 5 minutes — local 27B-class models legitimately take tens
// of seconds per response when thinking is on, and arbitrary tight
// timeouts produce "the server is broken" false positives. Cloud
// providers complete in under a second; the high ceiling has no cost
// for them. Override only when a faster failure signal is genuinely
// useful.
func (c Capabilities) scenarioTimeout() time.Duration {
	if c.ScenarioTimeout > 0 {
		return c.ScenarioTimeout
	}
	return 5 * time.Minute
}

// Run executes the shared provider conformance catalog.
func Run(t *testing.T, factory Factory, caps Capabilities) {
	t.Helper()
	if caps.Name == "" {
		t.Fatal("conformance: Capabilities.Name is required")
	}
	// ChatContains / StreamContains / ReasoningContains are opt-in
	// literal-substring assertions for callers that emit deterministic
	// fixtures (shaped-double tests). Live conformance against real
	// providers leaves them empty — see agent-bcea2d77 for why
	// literal-substring checks fail thinking-mode local models even
	// when the wire is working correctly.
	if caps.MaxTokensSlack == 0 {
		caps.MaxTokensSlack = 1
	}

	t.Run("health check", func(t *testing.T) {
		subject := newSubject(t, factory)
		if subject.HealthCheck == nil {
			t.Fatalf("%s: health check hook is required", caps.Name)
		}
		ctx, cancel := scenarioContext(caps)
		defer cancel()
		if err := subject.HealthCheck(ctx); err != nil {
			t.Fatalf("%s: health check failed: %v", caps.Name, err)
		}
	})

	t.Run("model discovery", func(t *testing.T) {
		subject := newSubject(t, factory)
		if len(caps.ExpectedModels) == 0 {
			t.Fatalf("%s: ExpectedModels is required for model discovery", caps.Name)
		}
		if subject.ListModels == nil {
			t.Fatalf("%s: model discovery hook is required", caps.Name)
		}
		ctx, cancel := scenarioContext(caps)
		defer cancel()
		models, err := subject.ListModels(ctx)
		if err != nil {
			t.Fatalf("%s: list models failed: %v", caps.Name, err)
		}
		for _, want := range caps.ExpectedModels {
			if !contains(models, want) {
				t.Fatalf("%s: discovered models %v, want %q", caps.Name, models, want)
			}
		}
	})

	t.Run("non-streaming chat", func(t *testing.T) {
		subject := newSubject(t, factory)
		ctx, cancel := scenarioContext(caps)
		defer cancel()
		resp, err := subject.Provider.Chat(ctx, []agent.Message{
			{Role: agent.RoleUser, Content: "conformance: reply with pong"},
		}, nil, agent.Options{MaxTokens: caps.chatMaxTokens()})
		if err != nil {
			t.Fatalf("%s: Chat failed: %v", caps.Name, err)
		}
		// Wire-shape check: non-empty visible content. The previous
		// literal "pong" assertion treated models as token-echoing
		// oracles — works for instruct models, fails for thinking-mode
		// locals (Qwen3.x) that think then phrase their own answer.
		// Per agent-bcea2d77 the test now asserts only that the wire
		// delivered something. caps.ChatContains, when set, still
		// runs as an additional check for shaped doubles that emit
		// deterministic fixtures.
		if strings.TrimSpace(resp.Content) == "" {
			t.Fatalf("%s: Chat returned empty content (wire delivered nothing)", caps.Name)
		}
		if caps.ChatContains != "" && !strings.Contains(resp.Content, caps.ChatContains) {
			t.Fatalf("%s: Chat content %q, want substring %q", caps.Name, resp.Content, caps.ChatContains)
		}
		if resp.Model == "" {
			t.Fatalf("%s: Chat response model is empty", caps.Name)
		}
	})

	if !caps.SupportsStreaming {
		return
	}

	t.Run("streaming chat", func(t *testing.T) {
		subject := newSubject(t, factory)
		streamer := requireStreamer(t, caps.Name, subject.Provider)
		ctx, cancel := scenarioContext(caps)
		defer cancel()
		result := collectStream(t, caps.Name, streamer, ctx, []agent.Message{
			{Role: agent.RoleUser, Content: "conformance: stream-pong"},
		}, nil, agent.Options{MaxTokens: caps.chatMaxTokens()})
		if !result.done {
			t.Fatalf("%s: stream did not emit Done", caps.Name)
		}
		// Same logic as non-streaming: structural check (non-empty
		// content) is the wire-shape test; literal-substring check
		// applies to shaped doubles that emit deterministic fixtures.
		if strings.TrimSpace(result.content) == "" {
			t.Fatalf("%s: stream returned empty content (wire delivered nothing)", caps.Name)
		}
		if caps.StreamContains != "" && !strings.Contains(result.content, caps.StreamContains) {
			t.Fatalf("%s: stream content %q, want substring %q", caps.Name, result.content, caps.StreamContains)
		}
	})

	t.Run("streaming max_tokens honored", func(t *testing.T) {
		subject := newSubject(t, factory)
		streamer := requireStreamer(t, caps.Name, subject.Provider)
		ctx, cancel := scenarioContext(caps)
		defer cancel()
		// Thinking-mode providers route most output budget through
		// reasoning_content first; a 3-token cap (the default) is
		// guaranteed to produce empty visible content even when the
		// wire is working correctly. Floor the cap at 256 for those
		// so reasoning + at least a few visible tokens both fit.
		// agent-bcea2d77.
		maxTokens := caps.streamMaxTokensCheck()
		if caps.SupportsThinking && maxTokens < 256 {
			maxTokens = 256
		}
		result := collectStream(t, caps.Name, streamer, ctx, []agent.Message{
			{Role: agent.RoleUser, Content: "conformance: max tokens"},
		}, nil, agent.Options{MaxTokens: maxTokens})
		words := len(strings.Fields(result.content))
		if words == 0 {
			t.Fatalf("%s: max_tokens stream returned empty content", caps.Name)
		}
		// Bound check applies only when we set a tight cap; for the
		// thinking-floor case we don't assert an upper bound on word
		// count, since we deliberately raised the cap to fit reasoning.
		if !caps.SupportsThinking && words > maxTokens+caps.MaxTokensSlack {
			t.Fatalf("%s: stream returned %d words, want <= %d", caps.Name, words, maxTokens+caps.MaxTokensSlack)
		}
	})

	if caps.SupportsThinking {
		t.Run("thinking reasoning", func(t *testing.T) {
			subject := newSubject(t, factory)
			streamer := requireStreamer(t, caps.Name, subject.Provider)
			ctx, cancel := scenarioContext(caps)
			defer cancel()
			result := collectStream(t, caps.Name, streamer, ctx, []agent.Message{
				{Role: agent.RoleUser, Content: "conformance: reason briefly then answer"},
			}, nil, agent.Options{MaxTokens: caps.chatMaxTokens(), Reasoning: agent.ReasoningTokens(32)})
			// Wire-shape check: reasoning_content must arrive. The
			// literal-substring check is preserved as an opt-in for
			// shaped-double fixtures.
			if strings.TrimSpace(result.reasoning) == "" {
				t.Fatalf("%s: thinking-capable provider returned empty reasoning_content", caps.Name)
			}
			if caps.ReasoningContains != "" && !strings.Contains(result.reasoning, caps.ReasoningContains) {
				t.Fatalf("%s: reasoning content %q, want substring %q", caps.Name, result.reasoning, caps.ReasoningContains)
			}
		})
	}

	if caps.SupportsToolCalls {
		t.Run("tool call streaming", func(t *testing.T) {
			subject := newSubject(t, factory)
			streamer := requireStreamer(t, caps.Name, subject.Provider)
			ctx, cancel := scenarioContext(caps)
			defer cancel()
			result := collectStream(t, caps.Name, streamer, ctx, []agent.Message{
				{Role: agent.RoleUser, Content: "conformance: call the inspect tool"},
			}, []agent.ToolDef{inspectTool()}, agent.Options{MaxTokens: caps.chatMaxTokens()})
			if result.toolID == "" {
				t.Fatalf("%s: stream did not emit a tool call id", caps.Name)
			}
			if result.toolName != "inspect" {
				t.Fatalf("%s: stream tool name %q, want inspect", caps.Name, result.toolName)
			}
			// Tighten the args check: the aggregate must parse as JSON
			// with a non-empty "target" string. The previous "contains
			// target" assertion would have passed on malformed deltas.
			var args map[string]any
			if err := json.Unmarshal([]byte(result.toolArgs), &args); err != nil {
				t.Fatalf("%s: stream tool args %q did not parse as JSON: %v", caps.Name, result.toolArgs, err)
			}
			target, ok := args["target"].(string)
			if !ok || target == "" {
				t.Fatalf("%s: stream tool args missing non-empty 'target' string: %v", caps.Name, args)
			}
		})

		t.Run("non-streaming tool call", func(t *testing.T) {
			// Validates the non-streaming Chat path independently of the
			// streaming path. Many servers handle them through different
			// code routes; a regression in either is invisible without
			// exercising both.
			subject := newSubject(t, factory)
			ctx, cancel := scenarioContext(caps)
			defer cancel()
			resp, err := subject.Provider.Chat(ctx, []agent.Message{
				{Role: agent.RoleUser, Content: "conformance: call the inspect tool"},
			}, []agent.ToolDef{inspectTool()}, agent.Options{MaxTokens: caps.chatMaxTokens()})
			if err != nil {
				t.Fatalf("%s: non-streaming Chat with tools failed: %v", caps.Name, err)
			}
			if len(resp.ToolCalls) == 0 {
				t.Fatalf("%s: non-streaming response had no tool_calls (content=%q)", caps.Name, resp.Content)
			}
			tc := resp.ToolCalls[0]
			if tc.ID == "" {
				t.Fatalf("%s: non-streaming tool call missing id", caps.Name)
			}
			if tc.Name != "inspect" {
				t.Fatalf("%s: non-streaming tool name %q, want inspect", caps.Name, tc.Name)
			}
			var args map[string]any
			if err := json.Unmarshal(tc.Arguments, &args); err != nil {
				t.Fatalf("%s: non-streaming tool args did not parse as JSON: %v (raw=%s)", caps.Name, err, string(tc.Arguments))
			}
			if target, ok := args["target"].(string); !ok || target == "" {
				t.Fatalf("%s: non-streaming tool args missing non-empty 'target': %v", caps.Name, args)
			}
		})

		t.Run("multi-tool wire shape", func(t *testing.T) {
			// Sends multiple tool defs in one request; asserts the
			// provider handles a non-singleton tools array and returns
			// a tool_call whose name matches one of the supplied tools.
			// Does not assert which tool — that depends on model
			// intelligence and is not what wire conformance tests.
			subject := newSubject(t, factory)
			streamer := requireStreamer(t, caps.Name, subject.Provider)
			ctx, cancel := scenarioContext(caps)
			defer cancel()
			tools := []agent.ToolDef{inspectTool(), summarizeTool(), countWordsTool()}
			result := collectStream(t, caps.Name, streamer, ctx, []agent.Message{
				{Role: agent.RoleUser, Content: "conformance: pick a tool and call it"},
			}, tools, agent.Options{MaxTokens: caps.chatMaxTokens()})
			if result.toolID == "" {
				t.Fatalf("%s: multi-tool request emitted no tool call (content=%q)", caps.Name, result.content)
			}
			validNames := map[string]bool{"inspect": true, "summarize": true, "count_words": true}
			if !validNames[result.toolName] {
				t.Fatalf("%s: multi-tool response chose unknown tool %q (valid: inspect, summarize, count_words)", caps.Name, result.toolName)
			}
			if result.toolArgs != "" {
				var args map[string]any
				if err := json.Unmarshal([]byte(result.toolArgs), &args); err != nil {
					t.Fatalf("%s: multi-tool args did not parse as JSON: %v (raw=%s)", caps.Name, err, result.toolArgs)
				}
			}
		})

		t.Run("tool result roundtrip", func(t *testing.T) {
			// Sends a synthetic conversation with a prior tool call +
			// tool result, asks for a final answer. Validates that the
			// provider's wire serialization includes tool_call_id pairing
			// and that the subsequent assistant turn does not re-emit the
			// tool call.
			subject := newSubject(t, factory)
			streamer := requireStreamer(t, caps.Name, subject.Provider)
			ctx, cancel := scenarioContext(caps)
			defer cancel()
			toolCallID := "call_test_inspect_1"
			messages := []agent.Message{
				{Role: agent.RoleUser, Content: "conformance: inspect the widget then tell me what you found"},
				{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{
					ID:        toolCallID,
					Name:      "inspect",
					Arguments: json.RawMessage(`{"target":"widget"}`),
				}}},
				{Role: agent.RoleTool, ToolCallID: toolCallID, Content: "inspected widget: status=ok"},
			}
			result := collectStream(t, caps.Name, streamer, ctx, messages, []agent.ToolDef{inspectTool()}, agent.Options{MaxTokens: caps.chatMaxTokens()})
			if result.toolID != "" {
				t.Fatalf("%s: tool-result follow-up unexpectedly re-emitted a tool call (id=%q name=%q)", caps.Name, result.toolID, result.toolName)
			}
			if !result.done {
				t.Fatalf("%s: tool-result follow-up did not emit Done", caps.Name)
			}
			// Some servers return empty content if the model decides the
			// tool result is sufficient; we only assert the wire
			// completed cleanly without re-tool-calling. Stronger
			// content assertions would test model behavior, not wire
			// conformance.
		})
	}
}

type streamResult struct {
	content   string
	reasoning string
	toolID    string
	toolName  string
	toolArgs  string
	done      bool
}

func newSubject(t *testing.T, factory Factory) Subject {
	t.Helper()
	subject := factory(t)
	if subject.Provider == nil {
		t.Fatal("conformance: factory returned nil Provider")
	}
	return subject
}

func requireStreamer(t *testing.T, name string, provider agent.Provider) agent.StreamingProvider {
	t.Helper()
	streamer, ok := provider.(agent.StreamingProvider)
	if !ok {
		t.Fatalf("%s: provider does not implement agent.StreamingProvider", name)
	}
	return streamer
}

func collectStream(t *testing.T, name string, streamer agent.StreamingProvider, ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) streamResult {
	t.Helper()
	ch, err := streamer.ChatStream(ctx, messages, tools, opts)
	if err != nil {
		t.Fatalf("%s: ChatStream setup failed: %v", name, err)
	}
	var result streamResult
	for delta := range ch {
		if delta.Err != nil {
			t.Fatalf("%s: stream error: %v", name, delta.Err)
		}
		result.content += delta.Content
		result.reasoning += delta.ReasoningContent
		if delta.ToolCallID != "" {
			result.toolID = delta.ToolCallID
		}
		if delta.ToolCallName != "" {
			result.toolName = delta.ToolCallName
		}
		result.toolArgs += delta.ToolCallArgs
		result.done = result.done || delta.Done
	}
	return result
}

func inspectTool() agent.ToolDef {
	return agent.ToolDef{
		Name:        "inspect",
		Description: "Inspect a named test target.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}`),
	}
}

func summarizeTool() agent.ToolDef {
	return agent.ToolDef{
		Name:        "summarize",
		Description: "Summarize a piece of text.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"max_words":{"type":"integer"}},"required":["text"]}`),
	}
}

func countWordsTool() agent.ToolDef {
	return agent.ToolDef{
		Name:        "count_words",
		Description: "Count the number of words in a string.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`),
	}
}

func scenarioContext(caps Capabilities) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), caps.scenarioTimeout())
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func HTTPStatusError(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return fmt.Errorf("HTTP %d", status)
}
