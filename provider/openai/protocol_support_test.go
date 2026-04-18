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
