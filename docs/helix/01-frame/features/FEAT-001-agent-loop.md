---
ddx:
  id: FEAT-001
  depends_on:
    - helix.prd
---
# Feature Specification: FEAT-001 — Agent Loop

**Feature ID**: FEAT-001
**Status**: Draft
**Priority**: P0
**Owner**: DDX Agent Team

## Overview

The agent loop is DDX Agent's core — a tool-calling LLM conversation loop that
sends a prompt, executes tool calls from the model's response, feeds results
back, and repeats until the model produces a final text response or limits are
reached. This implements PRD P0 requirements 1, 8, 10, and 11.

## Problem Statement

- **Current situation**: DDx dispatches agent work by spawning subprocess CLIs.
  Each manages its own conversation loop internally.
- **Pain points**: No programmatic control over the loop from Go. Can't
  inspect, pause, or redirect mid-conversation. No shared state.
- **Desired outcome**: A Go function that runs the full agent loop in-process,
  returning structured results with full tool-call history.

## Requirements

### Functional Requirements

1. `agent.Run(ctx, Request) (Result, error)` is the primary entry point
2. Request contains: prompt (string), system prompt (string), provider config,
   tool set, max iterations, working directory, callback (optional)
3. The loop sends messages to the configured LLM provider and processes the
   response
4. When the response contains tool calls, each tool is executed sequentially
   and results are appended to the conversation
5. When the response contains only text (no tool calls), the loop terminates
   with status=success
6. The loop terminates with status=iteration_limit when max_iterations is
   reached
7. The loop terminates with status=cancelled when ctx is cancelled
8. The loop terminates with status=error on provider errors (after retries)
9. All tool calls are recorded in the Result with inputs, outputs, duration,
   and errors
10. Token counts (input, output) are accumulated across all loop iterations
11. Total duration is measured from start to completion

### Non-Functional Requirements

- **Performance**: Loop overhead (excluding model inference and tool execution)
  < 1ms per iteration
- **Memory**: Conversation history is bounded by max_iterations × typical
  response size — no unbounded growth
- **Concurrency**: Multiple `agent.Run` calls can execute concurrently with
  independent state
- **Testability**: Provider interface is mockable for unit tests

## Edge Cases and Error Handling

- **Model returns empty response**: Treat as final response (status=success,
  empty output)
- **Tool call fails**: Include error in tool result, let model decide how to
  proceed
- **Provider returns rate limit**: Retry with backoff up to 3 times, then fail
- **Model hallucinates unknown tool**: Return error for that tool call, continue
- **Context cancelled mid-tool-call**: Interrupt tool (kill bash process, abort
  file I/O), return partial results with status=cancelled

## Success Metrics

- Agent loop can complete a multi-step task (read file → edit → verify) in a
  single `Run` call
- Loop correctly terminates on all exit conditions (success, limit, cancel, error)
- Token counts match provider-reported usage

## Acceptance Criteria

| ID | Criterion | Suggested Verification |
|----|-----------|------------------------|
| AC-FEAT-001-01 | A text-only or empty provider response terminates `Run()` with `status=success`, appends the assistant message to `Result.Messages`, and preserves provider-reported token totals in `Result.Tokens`. | `go test ./...` |
| AC-FEAT-001-02 | Tool-calling turns execute tool calls sequentially in provider order, record each call in `Result.ToolCalls`, feed tool results into the next provider request, and terminate successfully when a later turn returns text only. | `go test ./...` |
| AC-FEAT-001-03 | Iteration limits, context cancellation, transient-provider retry success, and retry exhaustion all terminate the loop with the documented status and without issuing extra provider calls beyond the runtime retry policy. | `go test ./...` |
| AC-FEAT-001-04 | Session lifecycle events are emitted in `seq` order with `session.start`, `llm.request`, optional `llm.delta`, `llm.response`, optional `tool.call`, and `session.end`; correlation metadata, accumulated usage, and known-vs-unknown cost semantics are preserved in emitted event payloads. | `go test ./...` |
| AC-FEAT-001-05 | Streaming providers support delta assembly, `NoStream` fallback, attempt metadata propagation, and timing capture for request start, first token, and completion without counting callback latency toward provider timing windows. | `go test ./...` |
| AC-FEAT-001-06 | Concurrent `Run()` calls do not share mutable state, and compaction no-fit paths fail closed without issuing an over-budget provider call. | `go test ./...` |
| AC-FEAT-001-07 | When accumulated `reasoning_content` exceeds the configurable byte limit (default 256 KB, `Request.ReasoningByteLimit`) with no `content` or `tool_call` delta seen, the stream is aborted with `ErrReasoningOverflow`. The error message includes the model name and the current threshold. Setting the limit to 0 disables the check. | `TestConsumeStream_ReasoningOverflow`, `TestConsumeStream_ReasoningUnlimited`, `TestConsumeStream_ReasoningOverflowErrorMessage` |
| AC-FEAT-001-08 | When only `reasoning_content` deltas arrive for longer than the configurable stall timeout (default 300 s, `Request.ReasoningStallTimeout`) with no `content` or `tool_call` delta, the stream is aborted with `ErrReasoningStall`. The error message includes the model name and the current threshold. Setting the timeout to 0 disables the check. | `TestConsumeStream_ReasoningStall`, `TestConsumeStream_ReasoningStallErrorMessage` |
| AC-FEAT-001-10 | Both reasoning thresholds are configurable via `config.yaml` (`reasoning_byte_limit`, `reasoning_stall_timeout`) and via `Request` pointer fields. `config.yaml` value `0` means unlimited for the byte limit; `"0s"` means unlimited for the stall timeout. Invalid duration strings are rejected at config load time. | `TestReasoningByteLimit_*`, `TestReasoningStallTimeout_*`, `TestResolveReasoningLimits` |
| AC-FEAT-001-09 | When the agent produces identical tool calls (same name + args fingerprint) for 3 or more consecutive turns, the loop exits with a non-retryable `ErrToolCallLoop` error. | `TestRun_ToolCallLoopDetection` |

## Constraints and Assumptions

- The caller provides a fully configured provider — DDX Agent does not manage API
  keys or provider selection (that's model routing, FEAT-004)
- Tool set is fixed at compile time for P0; extensible tool registration is P2

## Dependencies

- **Other features**: FEAT-002 (tools), FEAT-003 (providers)
- **External services**: An LLM provider (local or cloud)
- **PRD requirements**: P0-1, P0-8, P0-9, P0-10, P0-11

## Out of Scope

- Interactive/streaming output to a terminal (headless only)
- Conversation persistence across `Run` calls (P1 session continuity)
- Parallel tool execution (tools execute sequentially)
