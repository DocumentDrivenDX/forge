package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	agent "github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/compaction"
	openaiprovider "github.com/DocumentDrivenDX/agent/provider/openai"
	"github.com/DocumentDrivenDX/agent/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type externalMockProvider struct {
	responses []agent.Response
	callCount int
}

func (p *externalMockProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}
	if p.callCount >= len(p.responses) {
		return agent.Response{Content: "no more responses"}, nil
	}
	resp := p.responses[p.callCount]
	p.callCount++
	return resp, nil
}

type oversizedOutputTool struct {
	output string
}

func (t oversizedOutputTool) Name() string { return "oversized-output" }

func (t oversizedOutputTool) Description() string {
	return "returns a large payload"
}

func (t oversizedOutputTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t oversizedOutputTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	return t.output, nil
}
func (t oversizedOutputTool) Parallel() bool { return false }

func TestRun_FailedOpenAICompatibleChatSpansIncludeServerIdentity(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tel := telemetry.New(telemetry.Config{TracerProvider: tp})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(parsed.Port())
	require.NoError(t, err)

	provider := openaiprovider.New(openaiprovider.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "gpt-4o",
	})

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:    "trigger failure",
		Provider:  provider,
		Telemetry: tel,
		NoStream:  true,
	})
	require.Error(t, err)
	assert.Equal(t, agent.StatusError, result.Status)

	ended := recorder.Ended()
	chatSpans := spansWithOperation(ended, "chat")
	require.Len(t, chatSpans, 5)

	for _, span := range chatSpans {
		assert.Equal(t, "openai-compat", attrString(span.Attributes(), telemetry.KeyProviderName))
		assert.Equal(t, "local", attrString(span.Attributes(), telemetry.KeyProviderSystem))
		assert.Equal(t, parsed.Hostname(), attrString(span.Attributes(), telemetry.KeyServerAddress))
		assert.Equal(t, int64(port), attrInt(span.Attributes(), telemetry.KeyServerPort))
	}
}

func TestRun_FailsClosedForNoFitPrefixCompaction(t *testing.T) {
	provider := &externalMockProvider{
		responses: []agent.Response{
			{Content: "done", Usage: agent.TokenUsage{Total: 10}},
		},
	}

	cfg := compaction.DefaultConfig()
	cfg.ContextWindow = 80
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 20
	cfg.EffectivePercent = 100

	systemPrompt := strings.Repeat("P", 260)
	var events []agent.Event

	result, err := agent.Run(context.Background(), agent.Request{
		History: []agent.Message{
			{Role: agent.RoleUser, Content: strings.Repeat("A", 120)},
			{Role: agent.RoleAssistant, Content: strings.Repeat("B", 120)},
		},
		Prompt:       "DO-THE-THING",
		SystemPrompt: systemPrompt,
		Provider:     provider,
		Compactor:    compaction.NewCompactor(cfg),
		Callback: func(e agent.Event) {
			events = append(events, e)
		},
	})
	require.ErrorIs(t, err, agent.ErrCompactionNoFit)
	assert.Equal(t, agent.StatusError, result.Status)
	assert.Equal(t, 0, provider.callCount)

	var startEvent, endEvent *agent.Event
	for i := range events {
		switch events[i].Type {
		case agent.EventCompactionStart:
			startEvent = &events[i]
		case agent.EventCompactionEnd:
			endEvent = &events[i]
		}
	}

	require.NotNil(t, startEvent, "compaction start event should be emitted")
	require.NotNil(t, endEvent, "compaction end event should be emitted")
	assert.Less(t, startEvent.Seq, endEvent.Seq, "compaction end must follow compaction start")

	var endPayload map[string]any
	require.NoError(t, json.Unmarshal(endEvent.Data, &endPayload))
	assert.Equal(t, false, endPayload["success"])
	assert.Equal(t, true, endPayload["no_compaction"])
	assert.Equal(t, float64(3), endPayload["messages_before"])
	assert.Equal(t, float64(3), endPayload["messages_after"])
}

func TestRun_FailsClosedWhenToolOutputMakesCompactionNoFit(t *testing.T) {
	provider := &externalMockProvider{
		responses: []agent.Response{
			{
				ToolCalls: []agent.ToolCall{{
					ID:        "tool-1",
					Name:      "oversized-output",
					Arguments: json.RawMessage(`{}`),
				}},
				Usage: agent.TokenUsage{Total: 10},
			},
			{Content: "should not be reached", Usage: agent.TokenUsage{Total: 10}},
		},
	}

	compactionCalls := 0
	compactor := func(ctx context.Context, messages []agent.Message, provider agent.Provider, toolCalls []agent.ToolCallLog) ([]agent.Message, *agent.CompactionResult, error) {
		compactionCalls++
		if compactionCalls == 2 {
			return messages, nil, agent.ErrCompactionNoFit
		}
		return messages, nil, nil
	}

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:   "use the tool",
		Provider: provider,
		Tools: []agent.Tool{
			oversizedOutputTool{output: strings.Repeat("O", 600)},
		},
		Compactor: compactor,
	})
	require.ErrorIs(t, err, agent.ErrCompactionNoFit)
	assert.Equal(t, agent.StatusError, result.Status)
	assert.Equal(t, 1, provider.callCount, "run should stop before a second provider call")
	assert.Equal(t, 2, compactionCalls, "compaction should run once before the iteration and once after tool output")
}

func spansWithOperation(spans []sdktrace.ReadOnlySpan, operation string) []sdktrace.ReadOnlySpan {
	var filtered []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if value, ok := attrStringOk(span.Attributes(), telemetry.KeyOperationName); ok && value == operation {
			filtered = append(filtered, span)
		}
	}
	return filtered
}

func attrString(attrs []attribute.KeyValue, key string) string {
	value, _ := attrStringOk(attrs, key)
	return value
}

func attrStringOk(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}

func attrInt(attrs []attribute.KeyValue, key string) int64 {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsInt64()
		}
	}
	return 0
}
