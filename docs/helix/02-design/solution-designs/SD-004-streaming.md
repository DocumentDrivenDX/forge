---
ddx:
  id: SD-004
  depends_on:
    - FEAT-001
    - SD-001
---
# Solution Design: SD-004 — Provider Streaming

**Requirement**: PRD P1-3 (Streaming callbacks)

## Problem

The current `Provider.Chat()` method is synchronous — it blocks until the full
response is available. For large responses or slow models (especially local),
this means no feedback until the model finishes generating. Callers can't show
partial text, can't detect early tool calls, and can't implement time-to-first-token
metrics.

## Design: Optional StreamingProvider Interface

### Approach: Interface Upgrade Pattern

Add a `StreamingProvider` interface alongside the existing `Provider`. The agent
loop checks at runtime whether the provider supports streaming. If it does, use
streaming; if not, fall back to the synchronous `Chat()` path. This preserves
backwards compatibility — existing `Provider` implementations continue to work
unchanged.

```go
// StreamDelta is a single chunk from a streaming response.
type StreamDelta struct {
    // Content is a text fragment (may be empty if this delta is a tool call chunk).
    Content string

    // ToolCallID is set when a new tool call starts.
    ToolCallID string
    // ToolCallName is set on the first delta of a tool call.
    ToolCallName string
    // ToolCallArgs is a fragment of the tool call's JSON arguments.
    ToolCallArgs string

    // Usage may be set on any delta, including before Done.
    // Providers can emit incremental usage updates; consumers should merge them.
    Usage *TokenUsage

    // FinishReason is set on the final delta.
    FinishReason string

    // Model is set on the first or final delta.
    Model string

    // Done signals the end of the stream.
    Done bool
}

// StreamingProvider extends Provider with streaming support.
// Providers that implement this interface will be used in streaming mode
// by the agent loop.
type StreamingProvider interface {
    Provider
    ChatStream(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (<-chan StreamDelta, error)
}
```

### Why a Channel

Go channels are the idiomatic way to represent a stream of values. The provider
goroutine reads SSE events from the HTTP response and sends `StreamDelta` values
on the channel. The consumer (agent loop) reads from the channel in a `for range`
loop. Context cancellation propagates naturally — the provider closes the channel
when the context is cancelled or the stream ends.

Alternatives considered:
- **Callback** (`func(StreamDelta)`) — simpler but harder to compose. Can't
  easily wrap, filter, or tee the stream.
- **Iterator** (`Next() (StreamDelta, error)`) — viable but more boilerplate.
  Channels integrate better with `select` for timeout/cancellation.
- **io.Reader** — too low-level. Would require the consumer to parse SSE.

### Agent Loop Changes

The loop in `loop.go` currently calls `req.Provider.Chat()`. With streaming:

```go
// Check if provider supports streaming
if sp, ok := req.Provider.(StreamingProvider); ok {
    resp, err = consumeStream(ctx, sp, messages, toolDefs, opts, req.Callback, sessionID, &seq)
} else {
    resp, err = req.Provider.Chat(ctx, messages, toolDefs, opts)
}
```

`consumeStream` reads deltas from the channel, assembles them into a complete
`Response`, merges repeated usage updates, and emits `EventLLMDelta` events via
the callback for real-time streaming to the caller.

### New Event Types

```go
EventLLMDelta EventType = "llm.delta"   // partial content/tool-call fragment
```

The `llm.delta` event carries a `StreamDelta` as its data payload. Callers
that want token-level streaming subscribe to this event type. Usage can arrive
in fragmented updates on any delta, so consumers should merge the snapshots
they receive. Callers that don't care about streaming just ignore it — they
still get the full `llm.response` event at the end.

### Provider Implementation

Both SDKs have `NewStreaming` methods:
- OpenAI: `client.Chat.Completions.NewStreaming()` → `Stream[ChatCompletionChunk]`
- Anthropic: `client.Messages.NewStreaming()` → `Stream[MessageStreamEventUnion]`

Each provider's `ChatStream` implementation:
1. Calls the SDK's streaming method
2. Spawns a goroutine that reads chunks and sends `StreamDelta` on the channel
3. Assembles tool call arguments from incremental fragments
4. Sends a final `StreamDelta{Done: true}` and closes the channel, often after
   one or more incremental usage snapshots have already been emitted

### Request-Level Opt-Out

Add a field to `Request`:

```go
type Request struct {
    // ...existing fields...

    // NoStream disables streaming even if the provider supports it.
    // Useful when the caller doesn't need partial results and wants
    // simpler event handling.
    NoStream bool
}
```

## Implementation Plan

### Dependency Graph

```
SD-004-1: StreamDelta type + StreamingProvider interface (agent.go)
    ↓
SD-004-2: Agent loop streaming support (loop.go)
    ↓              ↓
SD-004-3: OpenAI    SD-004-4: Anthropic
streaming provider  streaming provider
```

### Issue Breakdown

1. **Define StreamDelta and StreamingProvider interface**
   - Add types to agent.go
   - Add EventLLMDelta event type
   - Add NoStream field to Request
   - Unit test: StreamDelta JSON round-trip

2. **Implement stream consumption in agent loop**
   - consumeStream() reads channel, assembles Response, emits delta events
   - Falls back to Chat() when provider doesn't implement StreamingProvider
   - Unit test with mock StreamingProvider

3. **OpenAI provider ChatStream**
   - Use openai-go NewStreaming()
   - Assemble tool call deltas into complete ToolCalls
   - Integration test against LM Studio

4. **Anthropic provider ChatStream**
   - Use anthropic-sdk-go NewStreaming()
   - Handle content_block_delta and tool_use events
   - Integration test against Claude API (e2e tag)

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| LM Studio streaming SSE malformed | M | M | Graceful degradation: fall back to non-streaming on parse error |
| Channel backpressure | L | L | Consumer reads in tight loop; buffer size 1 is fine |
| Tool call delta assembly complex | M | M | Both SDKs handle similar assembly; follow their patterns |
