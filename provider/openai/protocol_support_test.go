package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProtocolSupport_OpenRouter_AllTrue(t *testing.T) {
	p := New(Config{BaseURL: "https://openrouter.ai/api/v1", Flavor: "openrouter"})
	assert.True(t, p.SupportsTools())
	assert.True(t, p.SupportsStream())
	assert.True(t, p.SupportsStructuredOutput())
}

func TestProtocolSupport_Omlx_AllTrue(t *testing.T) {
	p := New(Config{BaseURL: "http://vidar:1235/v1", Flavor: "omlx"})
	assert.True(t, p.SupportsTools())
	assert.True(t, p.SupportsStream())
	assert.True(t, p.SupportsStructuredOutput())
}

func TestProtocolSupport_Ollama_StructuredOutputFalse(t *testing.T) {
	p := New(Config{BaseURL: "http://localhost:11434/v1", Flavor: "ollama"})
	assert.True(t, p.SupportsTools())
	assert.True(t, p.SupportsStream())
	assert.False(t, p.SupportsStructuredOutput(), "ollama structured output varies; flavor-level says false")
}

func TestProtocolSupport_UnknownFlavor_AllFalseConservative(t *testing.T) {
	// Server returns 404 to both probes; probe resolves to empty, falls back
	// to providerSystem. For a non-well-known URL that evaluates as "local",
	// the capability lookup returns zero struct → all false (conservative).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1"})
	assert.False(t, p.SupportsTools(), "unknown flavor must return false conservatively")
	assert.False(t, p.SupportsStream())
	assert.False(t, p.SupportsStructuredOutput())
}

func TestProtocolSupport_ExplicitFlavorOverridesProbe(t *testing.T) {
	// Server is detectable as omlx via probe. Explicit Flavor=ollama must win
	// (caller-set flavor takes precedence, per DetectedFlavor docstring).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models/status" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Flavor: "ollama"})
	// ollama table: Tools=true, Stream=true, StructuredOutput=false.
	assert.True(t, p.SupportsTools())
	assert.False(t, p.SupportsStructuredOutput(), "ollama override must win over omlx probe")
}

// TestSupportsThinking_PerFlavor is the regression for agent-04639431
// (DocumentDrivenDX/ddx ddx-6a5dfe35). Wire evidence showed that omlx
// silently terminates the SSE stream after the first delta when the
// `thinking` body field is present. The serializer in openai.go must
// strip `thinking` for any flavor whose SupportsThinking() is false.
func TestSupportsThinking_PerFlavor(t *testing.T) {
	cases := []struct {
		flavor string
		want   bool
	}{
		{"lmstudio", true},        // original target; field was added for this
		{"omlx", false},           // wire-proved to terminate silently
		{"openrouter", false},     // passthrough to backends that don't know it
		{"openai", false},         // uses reasoning_effort, not `thinking`
		{"ollama", false},         // doesn't support it
		{"unknown-flavor", false}, // zero-value fallback (conservative)
		{"", false},               // empty flavor fallback
	}
	for _, tc := range cases {
		t.Run(tc.flavor, func(t *testing.T) {
			p := New(Config{BaseURL: "http://example.test/v1", Flavor: tc.flavor})
			got := p.SupportsThinking()
			assert.Equal(t, tc.want, got,
				"SupportsThinking() for flavor=%q; wire evidence or vendor docs must justify any change", tc.flavor)
		})
	}
}
