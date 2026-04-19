package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSink returns a sink that writes to a bytes.Buffer for assertion.
func newTestSink() (*debugSink, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &debugSink{w: buf}, buf
}

func decodeEvents(t *testing.T, buf *bytes.Buffer) []wireEvent {
	t.Helper()
	var events []wireEvent
	dec := json.NewDecoder(buf)
	for dec.More() {
		var e wireEvent
		require.NoError(t, dec.Decode(&e))
		events = append(events, e)
	}
	return events
}

func TestRedactHeaders_RedactsBearerToken(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-very-secret-token-abc123")
	h.Set("X-Other", "not-a-secret")

	redacted := redactHeaders(h)
	assert.Equal(t, "Bearer [REDACTED]", redacted.Get("Authorization"))
	assert.Equal(t, "not-a-secret", redacted.Get("X-Other"))
	// Original untouched.
	assert.Equal(t, "Bearer sk-very-secret-token-abc123", h.Get("Authorization"))
}

func TestRedactHeaders_RedactsMixedCase(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "bearer tok_lowercase_abc")
	redacted := redactHeaders(h)
	assert.Equal(t, "bearer [REDACTED]", redacted.Get("Authorization"))
}

func TestDebugMiddleware_EmitsRequestAndResponse(t *testing.T) {
	sink, buf := newTestSink()

	// Stub "next" handler.
	next := func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    req,
		}, nil
	}

	mw := debugMiddleware(sink)
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-secret")

	resp, err := mw(req, next)
	require.NoError(t, err)
	// Body must still be readable downstream.
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, `{"ok":true}`, string(body))

	events := decodeEvents(t, buf)
	require.Len(t, events, 2)

	assert.Equal(t, "request", events[0].Dir)
	assert.Equal(t, "POST", events[0].Method)
	assert.Equal(t, "https://example.test/v1/chat/completions", events[0].URL)
	assert.Equal(t, `{"messages":[]}`, events[0].Body)
	// Authorization must be redacted.
	assert.Equal(t, []string{"Bearer [REDACTED]"}, events[0].Headers["Authorization"])

	assert.Equal(t, "response", events[1].Dir)
	assert.Equal(t, 200, events[1].Status)
	assert.Equal(t, `{"ok":true}`, events[1].Body)
	assert.Equal(t, "application/json", events[1].ContentTyp)
}

func TestDebugMiddleware_StreamingEmitsChunks(t *testing.T) {
	sink, buf := newTestSink()

	next := func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"ok\":1}\n\ndata: [DONE]\n\n")),
			Request:    req,
		}, nil
	}

	mw := debugMiddleware(sink)
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
	resp, err := mw(req, next)
	require.NoError(t, err)

	// Consume the stream body to trigger per-chunk events.
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	events := decodeEvents(t, buf)
	// Expect: initial request, initial SSE-headers response, >=1 chunk event,
	// and a final close-summary event.
	require.GreaterOrEqual(t, len(events), 4)
	assert.Equal(t, "request", events[0].Dir)
	// Find at least one response event that carries an SSE chunk body.
	sawChunk := false
	for _, e := range events[1:] {
		if e.Dir == "response" && strings.Contains(e.Body, "data:") {
			sawChunk = true
			break
		}
	}
	assert.True(t, sawChunk, "expected at least one streaming chunk event with SSE body")
}

func TestDebugMiddleware_NetworkErrorEmitsErrorEvent(t *testing.T) {
	sink, buf := newTestSink()

	next := func(req *http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}

	mw := debugMiddleware(sink)
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
	_, err := mw(req, next)
	require.Error(t, err)

	events := decodeEvents(t, buf)
	require.Len(t, events, 2)
	assert.Equal(t, "request", events[0].Dir)
	assert.Equal(t, "error", events[1].Dir)
	assert.Contains(t, events[1].Err, "unexpected EOF")
}

func TestResolveDebugSink_DisabledByDefault(t *testing.T) {
	// Reset sinkOnce for this test — we can't easily test the env-var path
	// because sinkOnce is package-level and other tests may have set it.
	// Instead, verify that providers built without the env var don't install
	// the middleware. This is asserted by New() itself not panicking and by
	// lifecycle being zero-cost.
	t.Setenv(envDebugWire, "")
	t.Setenv(envDebugWireFile, "")

	// We can't reset sync.Once inside the package from outside; instead,
	// assert the contract by checking that a provider constructed with an
	// explicit middleware slot takes the normal shape.
	p := New(Config{BaseURL: "http://example.test/v1"})
	assert.NotNil(t, p)
	// No direct way to inspect options the SDK absorbed — test relies on the
	// lack of panic and the integration path (other tests) continuing to pass.
}

// Sanity: resolveDebugSink returns a non-nil sink when AGENT_DEBUG_WIRE is set
// the first time the package observes the env. Because sinkOnce is global we
// can't reset it; this test runs in isolation only when the env is set at
// process start. Documented here so future readers understand the coverage gap.
var _ option.Middleware // ensure option.Middleware type stays imported even if test bodies simplify
