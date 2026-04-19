package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func streamSSECostTest(w http.ResponseWriter, events []string) {
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

func TestChat_OpenRouterUsageCostPreserved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"anthropic/claude-sonnet-4",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":5,
				"total_tokens":17,
				"cost":0.00321
			}
		}`))
	}))
	defer srv.Close()

	p := New(Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "anthropic/claude-sonnet-4",
	})
	p.providerSystem = "openrouter"

	resp, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	require.NoError(t, err)
	require.NotNil(t, resp.Attempt)
	require.NotNil(t, resp.Attempt.Cost)
	require.NotNil(t, resp.Attempt.Cost.Amount)
	assert.Equal(t, agent.CostSourceGatewayReported, resp.Attempt.Cost.Source)
	assert.Equal(t, "USD", resp.Attempt.Cost.Currency)
	assert.Equal(t, "openrouter/usage.cost", resp.Attempt.Cost.PricingRef)
	assert.InDelta(t, 0.00321, *resp.Attempt.Cost.Amount, 1e-12)
	assert.JSONEq(t, `{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"cost":0.00321}`, string(resp.Attempt.Cost.Raw))
}

func TestChat_OpenRouterUsageCostMissingRemainsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"anthropic/claude-sonnet-4",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":5,
				"total_tokens":17
			}
		}`))
	}))
	defer srv.Close()

	p := New(Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "anthropic/claude-sonnet-4",
	})
	p.providerSystem = "openrouter"

	resp, err := p.Chat(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	require.NoError(t, err)
	require.NotNil(t, resp.Attempt)
	require.NotNil(t, resp.Attempt.Cost)
	assert.Equal(t, agent.CostSourceUnknown, resp.Attempt.Cost.Source)
	assert.Nil(t, resp.Attempt.Cost.Amount)
}

func TestChatStream_OpenRouterUsageCostPreserved(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"cost":0.00321}}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamSSECostTest(w, chunks)
	}))
	defer srv.Close()

	p := New(Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "anthropic/claude-sonnet-4",
	})
	p.providerSystem = "openrouter"

	ch, err := p.ChatStream(context.Background(), []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
	}, nil, agent.Options{})
	require.NoError(t, err)

	var finalAttempt *agent.AttemptMetadata
	for delta := range ch {
		if delta.Done {
			finalAttempt = delta.Attempt
		}
	}

	require.NotNil(t, finalAttempt)
	require.NotNil(t, finalAttempt.Cost)
	require.NotNil(t, finalAttempt.Cost.Amount)
	assert.Equal(t, agent.CostSourceGatewayReported, finalAttempt.Cost.Source)
	assert.Equal(t, "USD", finalAttempt.Cost.Currency)
	assert.InDelta(t, 0.00321, *finalAttempt.Cost.Amount, 1e-12)
}

func TestRun_OpenRouterGatewayReportedCostFlowsToResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"anthropic/claude-sonnet-4",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":5,
				"total_tokens":17,
				"cost":0.00321
			}
		}`))
	}))
	defer srv.Close()

	p := New(Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test",
		Model:   "anthropic/claude-sonnet-4",
	})
	p.providerSystem = "openrouter"

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:   "hello",
		Provider: p,
		NoStream: true,
	})
	require.NoError(t, err)
	assert.InDelta(t, 0.00321, result.CostUSD, 1e-12)
}
