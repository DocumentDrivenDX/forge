# AGENTS.md — agent coding conventions for AI agents

## Package layout

- Root package (`agent`) — core types only: `Request`, `Result`, `Provider`, `Tool`, `Message`, `Event`. No logic.
- `loop.go` — the agent loop (`Run`). All event emission lives here.
- `stream_consume.go` — `consumeStream` helper; called from `loop.go` when provider is a `StreamingProvider`.
- `compaction/` — conversation compaction as a standalone package; integrated via `Request.Compactor` callback.
- `provider/openai/`, `provider/anthropic/` — LLM backends implementing `agent.Provider` and `agent.StreamingProvider`.
- `internal/compactionctx/` — context helpers for non-persisted prefix token accounting during compaction.
- `internal/safefs/` — centralized wrappers for intentional filesystem reads/writes used to scope `gosec` suppressions.
- `telemetry/` — runtime telemetry scaffolding for invoke_agent/chat/execute_tool spans.
- `tool/` — built-in tools (read, write, edit, bash).
- `session/` — session log writer, replay renderer, and usage aggregation.
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
- `EventCompactionStart` — only when the compactor actually ran (produced a result or returned an error); pure no-ops are silent
- `EventCompactionEnd` — paired with Start; emitted together so callbacks stay balanced
- `EventLLMRequest` — before each provider call
- `EventLLMDelta` — per streaming chunk (inside `consumeStream`)
- `EventLLMResponse` — after each provider call
- `EventToolCall` — per tool execution
- `EventSessionEnd` — once at end of `Run`

## Compaction

`compaction.NewCompactor(cfg)` returns a `func(...)` matching `Request.Compactor`. The compactor is stateful (mutex-protected `previousSummary` and `previousFileOps`). After compaction, the summary message is placed **last** in the new message list so recent turns remain first in the retained history.

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

<!-- DDX-AGENTS:START -->
<!-- Managed by ddx init / ddx update. Edit outside these markers. -->

# DDx

This project uses [DDx](https://github.com/DocumentDrivenDX/ddx) for
document-driven development. Use the `ddx` skill for beads, work,
review, agents, and status — every skills-compatible harness (Claude
Code, OpenAI Codex, Gemini CLI, etc.) discovers it from
`.claude/skills/ddx/` and `.agents/skills/ddx/`.

## Files to commit

After modifying any of these paths, stage and commit them:

- `.ddx/beads.jsonl` — work item tracker
- `.ddx/config.yaml` — project configuration
- `.agents/skills/ddx/` — the ddx skill (shipped by ddx init)
- `.claude/skills/ddx/` — same skill, Claude Code location
- `docs/` — project documentation and artifacts

## Conventions

- Use `ddx bead` for work tracking (not custom issue files).
- Documents with `ddx:` frontmatter are tracked in the document graph.
- Run `ddx doctor` to check environment health.
- Run `ddx doc stale` to find documents needing review.

## Merge Policy

Branches containing `ddx agent execute-bead` or `execute-loop` commits
carry a per-attempt execution audit trail:

- `chore: update tracker (execute-bead <TIMESTAMP>)` — attempt heartbeats
- `Merge bead <bead-id> attempt <TIMESTAMP>- into <branch>` — successful lands
- `feat|fix|...: ... [ddx-<id>]` — substantive bead work

Bead records store `closing_commit_sha` pointers into this history. Any
SHA rewrite breaks the trail. **Never squash, rebase, or filter** these
branches. Use only:

- `git merge --ff-only` when the target is a strict ancestor, or
- `git merge --no-ff` when divergence exists

Forbidden on execute-bead branches: `gh pr merge --squash`,
`gh pr merge --rebase`, `git rebase -i` with fixup/squash/drop,
`git filter-branch`, `git filter-repo`, and `git commit --amend` on
any commit already in the trail.
<!-- DDX-AGENTS:END -->
