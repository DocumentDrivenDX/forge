---
ddx:
  id: FEAT-003
  depends_on:
    - helix.prd
---
# Feature Specification: FEAT-003 — LLM Providers

**Feature ID**: FEAT-003
**Status**: Draft
**Priority**: P0
**Owner**: DDX Agent Team

## Overview

DDX Agent supports multiple LLM backends through a common interface, with two
built-in implementations: an OpenAI-compatible provider (covers LM Studio,
Ollama, OpenAI, Azure, Groq, Together, OpenRouter) and an Anthropic provider
(Claude). This implements PRD P0 requirements 3-4.

## Problem Statement

- **Current situation**: DDx harnesses each implement provider-specific CLI
  invocation. Adding a new provider means modifying the registry and writing
  invocation glue.
- **Pain points**: No unified Go API for calling different providers. LM Studio
  and Ollama both speak OpenAI-compatible API but DDx treats them as separate
  harnesses.
- **Desired outcome**: A `Provider` interface in Go with two implementations
  that cover all major backends. Configure by base URL — same code talks to
  LM Studio, Ollama, and OpenAI.

## Requirements

### Functional Requirements

#### Provider Interface

1. Common interface: `Chat(ctx, []Message, []Tool, Options) (Response, error)`
2. Messages include role (system/user/assistant/tool), content, and optional
   tool-call metadata
3. Tools are described as JSON Schema function definitions
4. Options include: model name, temperature, max tokens, stop sequences
5. Response includes: content (text), tool calls (if any), token usage
   (input/output), model ID, finish reason

#### OpenAI-Compatible Provider

6. Single implementation covers LM Studio, Ollama, OpenAI, Azure, Groq,
   Together, OpenRouter — anything that speaks the OpenAI chat completions API
7. Configured by: base URL, API key (optional for local), model name
8. Default base URLs: LM Studio `http://localhost:1234/v1`, Ollama
   `http://localhost:11434/v1`
9. Sends tools as OpenAI-format function definitions
10. Parses tool calls from response (including streaming delta assembly)
11. Reports token usage from response body
12. Supports streaming via SSE
13. Uses `github.com/openai/openai-go` with `option.WithBaseURL()`

#### Anthropic Provider

14. Connects to Anthropic's Messages API
15. Sends tools in Anthropic's tool-use format
16. Handles Anthropic-specific response structure (content blocks)
17. Reports token usage from response
18. Uses `github.com/anthropics/anthropic-sdk-go`

### Non-Functional Requirements

- **Performance**: Provider overhead (request serialization, response parsing)
  < 10ms beyond network round-trip
- **Reliability**: Retry with exponential backoff for transient errors (429,
  500, 503). Max 3 retries.
- **Observability**: Each provider call logs model, token counts, latency,
  and any error

## Edge Cases and Error Handling

- **Local server not running**: Return clear error with URL attempted — don't
  hang. Connection timeout of 5s.
- **Model not loaded**: Return provider error as-is (LM Studio and Ollama
  return meaningful errors for this)
- **Tool calling not supported by model**: Let it fail naturally — model will
  return text instead of tool calls, agent loop handles it
- **Streaming interrupted**: Return partial response with error
- **API key missing for cloud provider**: Return error at call time, not at
  provider construction (allows constructing providers speculatively)

## Success Metrics

- Same prompt completes successfully via LM Studio, Ollama, and Anthropic
  providers
- Token counts are accurately reported for all providers
- Provider swap is a base-URL change — no code changes

## Constraints and Assumptions

- LM Studio and Ollama both speak OpenAI-compatible API well enough for a
  single implementation. Edge cases handled by adapter if needed.
- Anthropic needs its own provider due to fundamentally different wire format
- Models are pre-loaded in LM Studio/Ollama — DDX Agent does not manage model
  lifecycle

## Dependencies

- **Other features**: FEAT-001 (agent loop uses providers)
- **External services**: LM Studio, Ollama, Anthropic API, OpenAI API
- **PRD requirements**: P0-3, P0-4

## Out of Scope

- Google Gemini native API (use via OpenAI-compat or OpenRouter)
- Provider-side prompt caching
- Model lifecycle management (load/unload/pull)
- Health checking or availability probing (caller's responsibility)
