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
	// Thinking reports whether the flavor accepts the non-standard `thinking`
	// body field (openai-go WithJSONSet) that LM Studio and a few compatible
	// servers tolerate. See openai.go ~line 358 for the injection site.
	// Wire evidence (agent-04639431, DocumentDrivenDX/ddx ddx-6a5dfe35)
	// shows omlx opens an SSE stream then silently terminates after the
	// first delta when `thinking` is present — hard-off for omlx.
	Thinking bool
}{
	// OpenAI proper uses reasoning_effort, not `thinking`. Keep off.
	"openai":     {Tools: true, Stream: true, StructuredOutput: true, Thinking: false},
	"openrouter": {Tools: true, Stream: true, StructuredOutput: true, Thinking: false},
	// LM Studio: the field was originally added for this flavor (openai.go
	// comment "LM Studio and compatible servers recognise"). Keep on.
	"lmstudio": {Tools: true, Stream: true, StructuredOutput: true, Thinking: true},
	// omlx: Tools/Stream/StructuredOutput are documented. Thinking is NOT
	// — wire evidence (agent-04639431) shows silent SSE termination when
	// `thinking` is present. Strip it.
	"omlx":   {Tools: true, Stream: true, StructuredOutput: true, Thinking: false},
	"ollama": {Tools: true, Stream: true, StructuredOutput: false, Thinking: false},
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

// SupportsThinking reports whether the flavor accepts the non-standard
// `thinking` request-body field used to cap reasoning-token budgets on
// LM Studio and compatible servers. Flavors returning false MUST have the
// field stripped at serialization time — see openai.go for the injection
// gate. Omlx and openrouter return false because including `thinking`
// causes either a silent stream termination (omlx, agent-04639431) or a
// rejection / passthrough-to-unsupporting-backend (openrouter).
func (p *Provider) SupportsThinking() bool {
	return protocolCapabilities[p.DetectedFlavor()].Thinking
}
