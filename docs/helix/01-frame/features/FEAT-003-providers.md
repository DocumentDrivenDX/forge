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
omlx, Ollama, OpenAI, Azure, Groq, Together, OpenRouter) and an Anthropic
provider (Claude). This implements PRD P0 requirements 3-4.

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

6. Single implementation covers LM Studio, omlx, Ollama, OpenAI, Azure, Groq,
   Together, OpenRouter — anything that speaks the OpenAI chat completions API
7. Configured by: base URL, API key (optional for local), model name
8. Default base URLs: LM Studio `http://localhost:1234/v1`, omlx
   `http://localhost:1235/v1`, Ollama `http://localhost:11434/v1`
9. Sends tools as OpenAI-format function definitions
10. Parses tool calls from response (including streaming delta assembly)
11. Reports token usage from response body
12. Supports streaming via SSE
13. Uses `github.com/openai/openai-go` with `option.WithBaseURL()`

#### Provider Flavor Detection

14. Each configured provider has an identified "flavor" (lmstudio, omlx,
    openrouter, ollama, openai, or generic) that determines which
    provider-specific extensions are available
15. Flavor resolution order:
    a. Explicit `flavor` field in config — used as-is, no network probe fired
    b. URL heuristics: `openrouter.ai` → openrouter; port 11434 → ollama;
       port 1234 → lmstudio; port 1235 → omlx
    c. Probe-based detection when URL is ambiguous ("local" or generic):
       concurrent probes to `/v1/models/status` (omlx) and `/api/v0/models`
       (lmstudio) with a 3-second timeout; first successful response wins
16. omlx is a local inference runtime with an OpenAI-compatible API plus an
    extension endpoint `GET /v1/models/status` that returns per-model
    `max_context_window` and `max_tokens`

#### Model Auto-Discovery

17. When `model` is empty in config, the provider queries `GET /v1/models`,
    ranks the returned IDs, and auto-selects the top-ranked one
18. Ranking tiers (highest first):
    - Tier 3: catalog-recognized model IDs
    - Tier 2: pattern-matched via `model_pattern` regex config field
    - Tier 1: uncategorized (any remaining model)
19. Within a tier, selection is deterministic (e.g., lexicographic) so the
    chosen model does not change across restarts unless the server's model list
    changes

#### Context and Token Limit Discovery

20. `LookupModelLimits` resolves context window size and max output tokens for
    the active model via a three-step cascade:
    a. Explicit config fields (`context_window` / `max_tokens`) — used directly
       if non-zero
    b. Live API probe against the provider's flavor-specific endpoint (see
       below) — used if the probe succeeds and returns non-zero values
    c. Zero — caller uses compaction defaults
21. Per-flavor probe endpoints:
    - lmstudio: `GET /api/v0/models/{model}` → `loaded_context_length`
      (prefers loaded context over theoretical maximum)
    - omlx: `GET /v1/models/status` → `max_context_window` and `max_tokens`
      per model entry
    - openrouter: `GET https://openrouter.ai/api/v1/models` →
      `context_length` and `top_provider.max_completion_tokens`
    - Other flavors: no probe; falls through to zero

#### Thinking / Reasoning Configuration

22. Per-provider config accepts two optional fields for extended-reasoning
    models (e.g., Qwen3, DeepSeek-R1):
    - `thinking_budget: int` — explicit max reasoning token budget; takes
      precedence when non-zero
    - `thinking_level: string` — named level (off / low / medium / high)
      resolved to a budget when `thinking_budget` is zero
23. A `thinking_level` of `off` disables reasoning tokens entirely; `low`,
    `medium`, and `high` map to provider-tuned budget values

#### Protocol Capability Introspection

24. Providers expose protocol-capability accessors that report what the
    server+flavor combination can actually honor, so callers can gate dispatch
    on supported features rather than dispatch-and-fail:
    - `SupportsTools() bool` — `/v1/chat/completions` accepts a `tools` field
      and returns structured `tool_calls`
    - `SupportsStream() bool` — `stream: true` returns a well-formed SSE
      stream with incremental `choices[0].delta` chunks
    - `SupportsStructuredOutput() bool` — honors `response_format: json_object`
      or equivalent JSON-mode / tool-use-required semantics
25. Capability flags are flavor-keyed (lmstudio / omlx / openrouter / ollama /
    openai). Unknown flavors return `false` conservatively so routing rejects
    rather than dispatches-and-fails.
26. Protocol capability is distinct from routing capability (the benchmark-
    quality score used by smart-routing scoring). These axes do not interact.

#### Debug and Observability

27. A process-wide opt-in debug mode (`AGENT_DEBUG_WIRE=1`) dumps every HTTP
    request and response at the openai-go transport boundary to stderr (or a
    file via `AGENT_DEBUG_WIRE_FILE=<path>`). Default off, zero cost when
    disabled. Authorization Bearer tokens are redacted before any event is
    written. Complements session events (`EventLLMRequest`/`EventLLMResponse`)
    which capture the logical view; wire dump captures the HTTP view.

#### Anthropic Provider

28. Connects to Anthropic's Messages API
29. Sends tools in Anthropic's tool-use format
30. Handles Anthropic-specific response structure (content blocks)
31. Reports token usage from response
32. Uses `github.com/anthropics/anthropic-sdk-go`

### Non-Functional Requirements

- **Performance**: Provider overhead (request serialization, response parsing)
  < 10ms beyond network round-trip
- **Reliability**: The runtime owns retry with exponential backoff for
  transient errors (429, 500, 503). Providers execute one request attempt per
  call and surface enough metadata for attempt-scoped observability. Max 3
  runtime retries.
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

- Same prompt completes successfully via LM Studio, omlx, Ollama, and
  Anthropic providers
- Token counts are accurately reported for all providers
- Provider swap is a base-URL change — no code changes
- When `model` is unset, auto-discovery selects a working model without
  operator intervention
- `LookupModelLimits` returns non-zero values for LM Studio, omlx, and
  OpenRouter when their servers are reachable

## Acceptance Criteria

| ID | Criterion | Suggested Verification |
|----|-----------|------------------------|
| AC-FEAT-003-01 | OpenAI-compatible and Anthropic providers each perform exactly one upstream request attempt per `Chat()` call, return token usage and response model data, and surface attempt metadata needed by runtime retries and telemetry. | `go test ./provider/... ./...` |
| AC-FEAT-003-02 | Streaming provider paths assemble partial text and tool-call fragments into the same logical response shape as synchronous calls, and interrupted streams preserve any partial response while still surfacing an error. | `go test ./provider/... ./...` |
| AC-FEAT-003-03 | Unreachable local endpoints fail within the documented bounded timeout and include the attempted endpoint/base URL in the surfaced error so operators can distinguish routing from model behavior problems. | `go test ./provider/... ./...` |
| AC-FEAT-003-04 | Missing cloud credentials fail at call time rather than constructor time, and default local OpenAI-compatible base URLs remain constructible without extra configuration. | `go test ./provider/... ./...` |
| AC-FEAT-003-05 | Build-tagged integration coverage exercises the same prompt path against LM Studio/OpenAI-compatible local inference and Anthropic/cloud-backed providers when the corresponding test environment is available. | `go test -tags=integration ./...`; `go test -tags=e2e ./...` |
| AC-FEAT-003-06 | `LookupModelLimits` returns the correct context window and max-token values for LM Studio (via `/api/v0/models/{model}`), omlx (via `/v1/models/status`), and OpenRouter (via their public models endpoint); explicit config fields override live probe results; unreachable endpoints fall through to zero without error. | `go test ./provider/... ./...` |
| AC-FEAT-003-07 | Flavor detection resolves correctly for each method: explicit `flavor` config skips all probes; URL heuristics identify openrouter/ollama/lmstudio/omlx from well-known hosts or ports; ambiguous URLs fire concurrent probes and resolve to the first responding flavor within the 3-second timeout. | `go test ./provider/... ./...` |
| AC-FEAT-003-08 | When `model` is empty, auto-discovery selects the highest-ranked available model according to the three-tier ranking (catalog → pattern → uncategorized) and the selection is deterministic across repeated calls with the same model list. | `go test ./provider/... ./...` |

## Constraints and Assumptions

- LM Studio and Ollama both speak OpenAI-compatible API well enough for a
  single implementation. Edge cases handled by adapter if needed.
- Anthropic needs its own provider due to fundamentally different wire format
- Models are pre-loaded in LM Studio/Ollama — DDX Agent does not manage model
  lifecycle

## Dependencies

- **Other features**: FEAT-001 (agent loop uses providers)
- **External services**: LM Studio, omlx, Ollama, Anthropic API, OpenAI API
- **PRD requirements**: P0-3, P0-4

## Out of Scope

- Google Gemini native API (use via OpenAI-compat or OpenRouter)
- Provider-side prompt caching
- Model lifecycle management (load/unload/pull)
- Availability health checking (e.g., readiness/liveness polling — caller's
  responsibility); flavor-detection probes are one-shot identification, not
  ongoing health monitoring
