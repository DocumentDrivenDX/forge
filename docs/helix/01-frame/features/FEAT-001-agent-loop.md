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
**Owner**: Forge Team

## Overview

The agent loop is Forge's core — a tool-calling LLM conversation loop that
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

1. `forge.Run(ctx, Request) (Result, error)` is the primary entry point
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
- **Concurrency**: Multiple `forge.Run` calls can execute concurrently with
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

## Constraints and Assumptions

- The caller provides a fully configured provider — Forge does not manage API
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
