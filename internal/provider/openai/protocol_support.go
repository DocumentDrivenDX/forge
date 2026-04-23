package openai

// Protocol-level capability flags describe what the concrete provider can
// honor on the OpenAI-compatible wire: tool calls, streaming, and structured
// output modes. They are distinct from routing-layer capability (a
// benchmark-quality score used by smart routing). Callers use these flags to
// gate dispatch before sending unsupported request shapes.

// ProtocolCapabilities declares provider-owned protocol capability claims.
type ProtocolCapabilities struct {
	Tools            bool
	Stream           bool
	StructuredOutput bool
	// Thinking reports whether the provider accepts non-standard body fields
	// (openai-go WithJSONSet) used to control model-side reasoning.
	Thinking       bool
	ThinkingFormat ThinkingWireFormat
	// StrictThinkingModelMatch, when true, makes the openai layer return an
	// error if the request carries an explicit reasoning policy while the
	// model does not match the provider's wire format family (e.g. a Qwen
	// wire format with a non-Qwen model). Set true for providers that only
	// serve a single model family (OMLX → Qwen MLX). Providers that host
	// mixed model families (LM Studio can load Qwen, Gemma, Llama, etc.)
	// should leave this false so reasoning controls silently no-op on
	// non-matching models instead of failing the request.
	StrictThinkingModelMatch bool
}

type ThinkingWireFormat string

const (
	// ThinkingWireFormatThinkingMap sends `thinking: {type, budget_tokens}`.
	ThinkingWireFormatThinkingMap ThinkingWireFormat = "thinking_map"
	// ThinkingWireFormatQwen sends Qwen-family controls:
	// `enable_thinking` and `thinking_budget`.
	ThinkingWireFormatQwen ThinkingWireFormat = "qwen"
	// ThinkingWireFormatOpenRouter sends OpenRouter's nested `reasoning`
	// object with `effort`, `max_tokens`, or `exclude`.
	ThinkingWireFormatOpenRouter ThinkingWireFormat = "openrouter"
)

var (
	OpenAIProtocolCapabilities  = ProtocolCapabilities{Tools: true, Stream: true, StructuredOutput: true, Thinking: false}
	UnknownProtocolCapabilities ProtocolCapabilities
)

func (p *Provider) protocolCapabilities() ProtocolCapabilities {
	if p.capabilities != nil {
		return *p.capabilities
	}
	return OpenAIProtocolCapabilities
}

// SupportsTools reports whether the concrete provider accepts a `tools`
// field on `/v1/chat/completions` and returns structured `tool_calls` in the
// response.
func (p *Provider) SupportsTools() bool {
	return p.protocolCapabilities().Tools
}

// SupportsStream reports whether `stream: true` returns a well-formed SSE
// stream with incremental `choices[0].delta` chunks.
func (p *Provider) SupportsStream() bool {
	return p.protocolCapabilities().Stream
}

// SupportsStructuredOutput reports whether the provider honors
// `response_format: json_object` / tool-use-required semantics to produce a
// structured (JSON-shaped) response.
func (p *Provider) SupportsStructuredOutput() bool {
	return p.protocolCapabilities().StructuredOutput
}

// SupportsThinking reports whether the provider accepts non-standard request
// body fields used to cap or disable model-side reasoning. Providers returning
// false MUST have those fields stripped at serialization time.
func (p *Provider) SupportsThinking() bool {
	return p.protocolCapabilities().Thinking
}

func (p *Provider) thinkingWireFormat() ThinkingWireFormat {
	caps := p.protocolCapabilities()
	if !caps.Thinking {
		return ""
	}
	if caps.ThinkingFormat != "" {
		return caps.ThinkingFormat
	}
	return ThinkingWireFormatThinkingMap
}

func (p *Provider) strictThinkingModelMatch() bool {
	return p.protocolCapabilities().StrictThinkingModelMatch
}
