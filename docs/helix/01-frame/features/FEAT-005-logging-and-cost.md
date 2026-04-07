---
ddx:
  id: FEAT-005
  depends_on:
    - helix.prd
    - FEAT-001
    - FEAT-003
---
# Feature Specification: FEAT-005 — Logging, Replay, and Cost Tracking

**Feature ID**: FEAT-005
**Status**: Draft
**Priority**: P0
**Owner**: Forge Team

## Overview

Every LLM interaction and tool call in Forge is logged to a structured session
log. Sessions can be replayed to understand exactly what happened. Cost is
tracked per-session using a model pricing table. This implements PRD P0
requirements 10-11.

Patterned on DDx's agent session logging (`SessionEntry` JSONL, `agent.Pricing`
table, `ddx agent usage` reporting) but with deeper granularity — DDx logs
one entry per subprocess invocation; Forge logs every turn within the
conversation loop.

## Problem Statement

- **Current situation**: DDx logs one `SessionEntry` per agent subprocess
  invocation — prompt in, response out, tokens, cost. This is the outer
  envelope. What happened inside the agent (which files it read, what edits
  it made, how many LLM turns it took) is opaque.
- **Pain points**: When an agent task fails or produces unexpected results,
  there's no way to see the intermediate steps. Debugging requires re-running
  the task. Cost is estimated from the final token count, not tracked per-turn.
- **Desired outcome**: A session log that captures every LLM turn and tool
  call with full bodies, enabling replay and debugging. Cost tracked per-turn
  and accumulated per-session.

## Requirements

### Functional Requirements

#### Session Logging

1. Each `forge.Run()` call creates a session with a unique ID
2. The session log captures events in order:
   - `session.start`: timestamp, config (provider, model, working dir, max
     iterations), prompt
   - `llm.request`: messages sent to provider, tools offered
   - `llm.response`: model response (text and/or tool calls), token usage,
     latency, cost estimate
   - `tool.call`: tool name, inputs, outputs, duration, error (if any)
   - `session.end`: final status, total tokens, total cost, total duration,
     final output
3. Events are written as JSONL — one JSON object per line, appendable
4. Each event includes: `session_id`, `seq` (sequence number), `type`,
   `timestamp`, and type-specific fields
5. Full prompt and response bodies are stored in the event (not external
   files, at least for P0)
6. Log directory is configurable. Default: `.forge/sessions/`
7. The caller can provide correlation metadata (bead_id, workflow, etc.)
   that is stored on `session.start` and `session.end` events

#### Replay

8. Given a session log file, Forge can reconstruct and display the full
   conversation: system prompt, each user/assistant turn, each tool call
   with inputs and outputs, token counts per turn, cost per turn
9. Replay is a read-only operation on the log file
10. Replay output is human-readable text (not JSON) — suitable for terminal
    display or piping to a pager

#### Cost Tracking

11. Model pricing table maps model IDs to per-million-token input/output
    prices (patterned on DDx `agent.Pricing`)
12. Cost is estimated per `llm.response` event using the pricing table
13. Local models (LM Studio, Ollama) have $0 cost — pricing table entry
    with zero values
14. Cost is accumulated across all turns and reported in `session.end`
15. `Result.CostUSD` reflects the total session cost
16. Pricing table is built-in with common models; extensible via config

#### Usage Reporting (P1 — Standalone CLI)

17. `forge usage` aggregates session logs: per-provider/model token counts
    and cost, with time-window filtering (today, 7d, 30d, date range)
18. Output formats: table (default), JSON, CSV — patterned on
    `ddx agent usage`

### Non-Functional Requirements

- **Performance**: Logging overhead < 1ms per event (async write, buffered)
- **Reliability**: Log writes are best-effort — logging failure must not
  block the agent loop. Partial logs are still useful.
- **Storage**: Session logs grow at ~10-100KB per session. No automatic
  rotation in P0.
- **Compatibility**: Log format should be forward-compatible — new event
  types can be added without breaking replay of old logs

## Edge Cases and Error Handling

- **Log directory not writable**: Warn once, continue without logging.
  `Result` still has token counts and cost.
- **Session interrupted (context cancelled)**: Write `session.end` with
  status=cancelled and whatever data was collected
- **Model not in pricing table**: CostUSD = -1 (unknown), not 0 (free).
  Distinguishes "unknown" from "local/free".
- **Very large tool output (>1MB)**: Truncate in log with marker, store
  byte count of original

## Success Metrics

- Every completed session has a log with all events
- `forge replay <id>` reproduces the conversation accurately
- Cost estimates for cloud models match actual API billing within 5%
- Log files are valid JSONL readable by `jq`

## Constraints and Assumptions

- P0 uses JSONL as the sole logging format. OTel integration is P1 — if
  span overhead is acceptable, emit OTel spans alongside JSONL. If not,
  JSONL remains the primary observability surface.
- Log format is Forge-specific but designed to be consumable by DDx's
  session inspection tooling with a thin adapter
- No log rotation or retention policy in P0

## Dependencies

- **Other features**: FEAT-001 (loop emits events), FEAT-003 (provider
  reports token usage)
- **External services**: None (logging is local)
- **PRD requirements**: P0-10, P0-11

## Out of Scope

- Log shipping to external systems (Grafana, DataDog, etc.)
- Real-time log streaming to a UI
- Automatic log rotation or retention policies
- Budget enforcement (stopping the agent when cost exceeds a threshold) —
  the caller can do this via context cancellation based on streaming
  cost callbacks
