# AGENTS.md — agent coding conventions for AI agents

## Package layout

- Root package (`agent`) — core types only: `Request`, `Result`, `Provider`, `Tool`, `Message`, `Event`. No logic.
- `loop.go` — the agent loop (`Run`). All event emission lives here.
- `stream_consume.go` — `consumeStream` helper; called from `loop.go` when provider is a `StreamingProvider`.
- `compaction/` — conversation compaction as a standalone package; integrated via `Request.Compactor` callback.
- `provider/openai/`, `provider/anthropic/` — LLM backends implementing `agent.Provider` and `agent.StreamingProvider`.
- `tool/` — built-in tools (read, write, edit, bash).
- `session/` — session log writer.
- `config/` — multi-provider YAML config.

## Provider interface pattern

Providers implement `agent.Provider` (synchronous). Streaming is opt-in via `agent.StreamingProvider`. The agent loop detects streaming at runtime with a type assertion:

```go
if sp, ok := req.Provider.(StreamingProvider); ok && !req.NoStream {
    resp, err = consumeStream(...)
} else {
    resp, err = req.Provider.Chat(...)
}
```

Do not add streaming logic to `Provider`. Do not change `ChatStream` signatures without updating both providers and `consumeStream`.

## Event emission

All events flow through `emitCallback(req.Callback, Event{...})`. Sequence numbers are tracked by the `seq *int` pointer threaded through the call chain. Never emit events outside `loop.go` except inside `consumeStream` (for `EventLLMDelta`).

Defined event types (all must be emitted at the right time):
- `EventSessionStart` — once at start of `Run`
- `EventCompactionStart` — before calling `req.Compactor` (not yet implemented; tracked as hx-378d7a02)
- `EventCompactionEnd` — after compaction, on both success and error paths
- `EventLLMRequest` — before each provider call
- `EventLLMDelta` — per streaming chunk (inside `consumeStream`)
- `EventLLMResponse` — after each provider call
- `EventToolCall` — per tool execution
- `EventSessionEnd` — once at end of `Run`

## Compaction

`compaction.NewCompactor(cfg)` returns a `func(...)` matching `Request.Compactor`. The compactor is stateful (mutex-protected `previousSummary` and `previousFileOps`). After compaction, the summary message is placed **first** in the new message list (implementation note: SD-006 specifies LAST — see agent-1e5e2c60).

`IsCompactionSummary` detects summary messages by checking for `<summary>` tags.

## Issue tracker

Use `ddx bead` commands. Common workflow:
```
ddx bead ready --json          # list available work
ddx bead show <id>             # inspect an issue
ddx bead update <id> --claim   # claim before starting
ddx bead close <id>            # close after verification
```

## Test conventions

- Unit tests live next to the code they test (e.g., `stream_consume_test.go` alongside `stream_consume.go`).
- Mock types (`mockProvider`, `mockStreamingProvider`) are defined in `*_test.go` files in `package agent`.
- Compaction integration tests are in `compaction/compaction_test.go` (internal `package compaction`).
- Provider packages have no unit tests (integration-only); virtual provider in `provider/virtual/` serves as a test double.
- All tests must pass before committing: `go test ./...`
