package openai

// Protocol-level capability flags describe what the resolved
// provider+flavor can actually honor on the wire: tool calls, streaming, and
// structured-output modes. They are distinct from routing-layer capability
// (a benchmark-quality score used by smart routing). Callers use these flags
// to gate dispatch — refusing to send a tool-using prompt at a flavor that
// does not translate tools is cheaper than dispatching and failing mid-stream.

// protocolCapabilities is the static flavor → capability table. Values reflect
// what each flavor is claimed (by its vendor docs) to support at the HTTP
// surface. Unknown flavors default to the zero value (all false), which is the
// conservative choice — routing rejects rather than dispatches.
//
// When wire evidence (see ddx-6a5dfe35) contradicts a claim, revise the table
// here rather than adding per-model overrides. Per-model capability is
// explicitly out of scope for this surface (see agent-767549c7 out-of-scope
// section).
var protocolCapabilities = map[string]struct {
	Tools            bool
	Stream           bool
	StructuredOutput bool
}{
	"openai":     {Tools: true, Stream: true, StructuredOutput: true},
	"openrouter": {Tools: true, Stream: true, StructuredOutput: true},
	"lmstudio":   {Tools: true, Stream: true, StructuredOutput: true},
	// omlx: server-side tool-call translation is documented. Streaming and
	// structured output are documented. Revisit if ddx-6a5dfe35 surfaces
	// wire evidence against any of these.
	"omlx":   {Tools: true, Stream: true, StructuredOutput: true},
	"ollama": {Tools: true, Stream: true, StructuredOutput: false},
	// "local" and "" fall through to zero-value (all false) by design.
}

// SupportsTools reports whether the resolved provider+flavor accepts a `tools`
// field on `/v1/chat/completions` and returns structured `tool_calls` in the
// response.
func (p *Provider) SupportsTools() bool {
	return protocolCapabilities[p.DetectedFlavor()].Tools
}

// SupportsStream reports whether `stream: true` returns a well-formed SSE
// stream with incremental `choices[0].delta` chunks.
func (p *Provider) SupportsStream() bool {
	return protocolCapabilities[p.DetectedFlavor()].Stream
}

// SupportsStructuredOutput reports whether the provider honors
// `response_format: json_object` / tool-use-required semantics to produce a
// structured (JSON-shaped) response.
func (p *Provider) SupportsStructuredOutput() bool {
	return protocolCapabilities[p.DetectedFlavor()].StructuredOutput
}
