---
ddx:
  id: helix.prd
---
# Product Requirements Document — DDX Agent

## Summary

DDX Agent is a Go library that implements a coding agent runtime — a tool-calling
LLM loop with file read/write, shell execution, navigation helpers, task
tracking, and structured I/O — designed
to be embedded in build orchestrators and related tooling. It prioritizes local
model inference via LM Studio and Ollama, with transparent escalation to cloud
providers when local models are insufficient. DDX Agent provides an in-process
alternative to subprocess-based agent dispatch, eliminating process overhead,
enabling direct state sharing, and providing native cost control. Following the
ghostty model — great library, proven by a usable app — DDX Agent ships as a Go
package plus a thin standalone CLI that showcases the library and serves as an
embeddable harness backend (see CONTRACT-003). DDX Agent also owns a reusable
shared model catalog and updateable manifest so callers and related tooling can
resolve aliases, tiers/profiles, canonical policy targets, and deprecations without
copying model policy into each consumer. Every LLM interaction and tool call is
logged and replayable, with per-model cost tracking built in. Success means
orchestrators can run a HELIX build pass where 70%+ of routine tasks use local
models at near-zero cost, the operator can replay any session to understand
exactly what happened, and downstream tools consume agent's model catalog rather
than duplicating release-policy tables.

## Problem and Goals

### Problem

Orchestrators dispatching work to AI agents typically shell out to standalone
CLIs (claude, codex, pi, opencode). Each invocation spawns a process, re-reads
the codebase, re-establishes context, and returns unstructured text that must be
parsed. This is slow (~2-5s overhead per invocation), expensive (full cloud
pricing on every call including context re-establishment), and lossy (no shared
state between invocations). Local models are theoretically supported but require
running a separate agent CLI that may not support LM Studio or may not handle
tool calling reliably.

In a typical HELIX build pass, 70% of agent tasks are mechanical — reading
files, applying templated edits, running tests, scaffolding boilerplate. These
don't need a $15/MTok cloud model. But the current architecture treats every
task the same: spawn a process, send to cloud, parse the result.

### Goals

1. **Embed the agent loop in Go** — provide a `agent.Run(ctx, prompt, opts)`
   API that callers invoke in-process, eliminating subprocess overhead
2. **Local-model-first** — native LM Studio and Ollama support with tool
   calling, making local models the default for routine tasks
3. **Structured I/O** — accept prompts and structured envelopes, return structured results
   with status, output, token usage, and timing
4. **Full observability** — every LLM turn and tool call logged, replayable,
   cost-tracked via JSONL session logging (per-session detail)
5. **Prove it with an app** — standalone `ddx-agent` CLI that showcases the library
   and serves as an embeddable harness, following the ghostty pattern
6. **Own reusable model policy** — provide a agent-owned shared model catalog,
   publishable updateable manifest, and explicit refresh workflow so aliases,
   tiers/profiles, canonical policy targets, and deprecations are maintained
   once and consumed by any caller

### Success Metrics

| Metric | Target | Measurement Method |
|--------|--------|--------------------|
| Subprocess elimination | DDX Agent handles ≥1 harness in-process | Integration test |
| Local model completion rate | ≥70% of routine tasks succeed on local 7B+ | HELIX build pass logs |
| Cost per bead (blended) | <$0.05 average | `ddx-agent usage` report |
| Agent loop overhead | <10ms beyond model inference time | Benchmark suite |

### Non-Goals

- **TUI or interactive mode** — DDX Agent is headless-only. Interactive use goes
  through pi, claude, or other standalone agents.
- **MCP server** — DDX Agent provides tools directly, not via MCP protocol.
- **Prompt engineering** — DDX Agent executes prompts; the caller owns
  prompt design and persona injection.
- **Harness orchestration policy** — DDX Agent owns reusable model catalog data and
  policy, but callers choose harnesses/providers for a task and HELIX owns only
  stage intent.
- **Model hosting** — DDX Agent connects to LM Studio/Ollama/cloud APIs. It does
  not run inference itself.
- **IDE integration** — DDX Agent is a library, not an editor plugin.

## Users and Scope

### Primary Persona: Orchestrator / Caller

**Role**: An orchestration system or embedding application (software, not a human)
**Goals**: Dispatch agent work with minimal overhead, control cost, get structured results
**Pain Points**: Subprocess spawning is slow, cloud-only is expensive, output parsing is fragile

### Secondary Persona: Build System Integrator

**Role**: Developer embedding DDX Agent in custom CI pipelines or build tools
**Goals**: Run LLM-powered code tasks (fix lint, update deps, generate tests) in Go programs
**Pain Points**: Existing agent CLIs require process management, don't expose a library API

### Tertiary Persona: CLI User

**Role**: Developer using `ddx-agent` or a wrapper CLI from the command line
**Goals**: Faster, cheaper agent invocations with local model support
**Pain Points**: Current harness is slower than necessary, no local model path

## Requirements

### Must Have (P0)

1. **Agent loop** — tool-calling LLM loop: send prompt → model responds with
   tool calls or text → execute tools → repeat until done or max iterations
2. **Tool set** — shipped built-ins include read, write, edit, bash, find,
   grep, ls, patch, and task
3. **OpenAI-compatible provider** — generic provider for any OpenAI-compatible
   endpoint. Covers LM Studio (localhost:1234), Ollama (localhost:11434),
   OpenAI, Azure, Groq, Together, OpenRouter. Single implementation, configure
   by base URL.
4. **Anthropic provider** — Claude API support for cloud use
5. **Structured I/O** — accept prompt as string or structured envelope, return
   structured result (status, output, tool calls made, tokens, duration, error)
6. **Go library API** — `agent.Run(ctx, request) (Result, error)` as the
   primary interface. Library takes a Config struct; no global state.
7. **Token tracking** — count and report input/output tokens per invocation
8. **Iteration limit** — configurable max tool-call iterations to prevent
   runaway loops
9. **Working directory** — file operations scoped to a configurable root.
   Paths outside working dir are allowed (sandbox assumption) but logged.
10. **Session logging** — every LLM request/response and tool call recorded to
    JSONL log. Full prompt and response bodies stored. Logs must support replay —
    reading a session log reproduces the complete conversation including tool
    calls and results.
11. **Cost tracking** — preserve provider- or gateway-reported cost when
    available. If no reported cost exists, use only runtime-specific configured
    pricing for the exact provider system and resolved model; otherwise record
    cost as unknown. `agent.Result` includes `CostUSD` (`-1` when unknown).
12. **Standalone CLI** — `ddx-agent` binary wrapping the library. Proves the library
    works, serves as an embeddable harness backend (see CONTRACT-003). Reads its
    own config file. Accepts prompt via `-p` flag or stdin.

### Should Have (P1)

1. **System prompt composition** — base system prompt + caller-provided
   additions (persona, project context, conventions) — **Implemented**
2. **Session continuity** — option to carry conversation history across
   multiple `Run` calls within a session
3. **Streaming callbacks** — caller can receive tool call events and partial
   responses in real time — **Implemented**
4. **Timeout management** — per-invocation and per-tool-call timeouts
5. **Harness adapter** — implement the harness interface (CONTRACT-003) so DDX Agent
   appears as a native harness alongside claude/codex/pi
6. **Usage reporting** — `ddx-agent usage` command:
   per-provider/model token and cost summaries with time-window filtering
7. **Session replay** — `ddx-agent replay <session-id>` reads a session log and
   prints the conversation in human-readable form (every turn, tool call,
   result, tokens, timing)
8. **OpenTelemetry observability** — emit OTel GenAI-aligned spans and metrics
   for agent, LLM, and tool activity while retaining JSONL session logs for
   replay. Use standard OTel token/timing fields where available and
   project-namespaced attributes for cost/runtime details not yet covered by
   the standard.
9. **Conversation compaction** — auto-summarize long conversation histories
   to fit within model context windows — **Implemented**
10. **Shared model catalog** — an agent-owned catalog and publishable
    updateable manifest for model aliases, tiers/profiles (`code-high`,
    `code-medium`, `code-economy`, with compatibility aliases such as
    `smart`, `fast`, `cheap`), canonical policy targets, and deprecation
    metadata, kept separate from prompt presets

### Nice to Have (P2)

1. **Caching** — cache file reads within a session to reduce redundant I/O
2. **Model-first provider routing** — request a model or model ref and let the
   embedded runtime choose among equivalent configured providers with recorded
   attribution and bounded failover.
3. **Multi-model consensus** — run same prompt on N models, return majority
   answer (multi-harness quorum is a caller concern)
4. **Model selection optimization** — choose model based on task
   characteristics (context length, complexity heuristics)

## Functional Requirements

### Agent Loop

- The loop MUST send the prompt + conversation history to the LLM provider
- When the LLM responds with tool calls, DDX Agent MUST execute each tool and
  append the results to the conversation
- When the LLM responds with text only (no tool calls), the loop MUST
  terminate and return the text as the result
- The loop MUST terminate after `max_iterations` tool-call rounds
- The loop MUST terminate if the context provides a cancellation signal
- Each tool call MUST be logged with inputs, outputs, duration, and any error

### Tools

- **read**: Accept absolute or relative path (resolved against working dir).
  Return file contents as string. Error if file doesn't exist or is binary.
  Support line range (offset + limit).
- **write**: Accept path and content. Create parent directories if needed.
  Overwrite existing file. Return bytes written.
- **edit**: Accept path, old_string, new_string. Fail if old_string not found
  or not unique. Return success/failure.
- **bash**: Accept command string and optional timeout. Execute in working dir.
  Capture stdout, stderr, exit code. Kill on timeout. Default timeout 120s.

### Providers

- Each provider MUST implement a common interface: `Chat(ctx, messages, tools,
  opts) (Response, error)`
- Provider selection MUST be configurable per-request
- Provider configuration MUST remain separate from the agent's canonical model
  catalog. Providers own transport/auth details; the catalog owns model policy.
- Providers MUST report token usage when the upstream API provides it
- Providers MUST support tool/function calling in the format the model expects
- The LM Studio provider MUST connect to `http://localhost:1234/v1` by default
  with configurable host/port
- The Ollama provider MUST connect to `http://localhost:11434` by default

### Model Catalog

- DDX Agent MUST define a reusable shared model catalog for aliases, model
  families, tiers/profiles, canonical policy targets, and deprecation/stale
  metadata
- The shared catalog MUST be distinct from system prompt presets and use its
  own naming/config surface
- DDX Agent MUST ship an embedded release snapshot of the catalog and support an
  updateable external manifest for faster policy/data refresh where practical
- DDX Agent MUST support publishing catalog manifests outside normal binary
  releases and an explicit local refresh/install flow that does not introduce
  network access into ordinary request execution
- Callers MUST be able to resolve model references through the agent-owned
  catalog without duplicating model policy in their own repos
- HELIX stage intent MUST remain above this layer; HELIX selects intent, callers
  resolve harness/provider/model details using agent-owned catalog data

### Structured I/O

- Input: plain string prompt, or structured envelope (JSON with kind, id,
  prompt, inputs, response_schema fields)
- Output: `Result` struct with: status (success/failure/timeout/cancelled),
  output (final text), tool_calls (log of all tool calls), tokens
  (input/output/total), duration_ms, error (if any), model (which model was
  used)

## Acceptance Test Sketches

Detailed, authoritative acceptance criteria live with the feature specs in
`docs/helix/01-frame/features/FEAT-00X-*.md`. The sketches below remain the
product-level smoke matrix; feature-level AC should be used when creating
tests, review findings, or execution beads.

| Requirement | Scenario | Input | Expected Output |
|-------------|----------|-------|-----------------|
| Agent loop basics | Simple file read task | "Read main.go and tell me the package name" | Result with status=success, output contains package name |
| Tool: edit | Find-replace in file | Prompt to rename a function | File on disk is modified, result shows edit tool call |
| Tool: bash | Run tests | "Run `go test ./...` and report results" | Result includes test output, exit code |
| LM Studio provider | Connect to local model | Prompt with LM Studio running Qwen 3.5 | Successful completion with token count |
| Iteration limit | Prevent runaway | Max 3 iterations, task needs 10 | Result with status=failure after 3 rounds |
| Structured I/O | Structured envelope | JSON envelope with prompt and response_schema | Result matches schema |
| Token tracking | Count tokens | Any successful completion | Result.tokens has non-zero input and output counts |
| Session logging | Run any task | Any successful completion | JSONL log entry with full prompt, response, tool calls, tokens, timing |
| Session replay | Read logged session | `ddx-agent replay <id>` on a completed session | Human-readable dump of every turn, tool call, and result |
| Cost tracking | Run cloud task with billed cost returned | Claude or gateway completion with reported billing | Result.CostUSD > 0 and matches reported cost |
| Cost tracking | Run task without pricing data | Unconfigured runtime or provider with no reported billing | Result.CostUSD == -1 (unknown, not guessed) |
| Standalone CLI | End-to-end | `ddx-agent -p "Read main.go"` with config file | Successful completion, session logged |
| Shared model catalog | Resolve model reference | `ddx-agent -p "hi" --model-ref code-smart` | Concrete model resolved from the agent catalog for the selected surface |
| Harness path | Structured harness invocation | Prompt envelope or harness execution path (CONTRACT-003) | Machine-readable JSON output includes tokens, session ID, and cost semantics |
| Self-update check | Scripted version check | `ddx-agent update --check-only` | Exit code reflects update availability and output shows current/latest versions |

## Technical Context

- **Language/Runtime**: Go 1.23+
- **Key Libraries**:
  - `github.com/openai/openai-go` — OpenAI-compatible API client (LM Studio,
    Ollama, OpenAI, Azure)
  - `github.com/anthropics/anthropic-sdk-go` — Anthropic Claude API client
  - Standard library for HTTP, JSON, process execution
- **Build**: `go build`, Makefile
- **Platform Targets**: Linux (primary — CI and build servers), macOS
  (development). Windows is not a priority.
- **Integration Point**: Callers embed the Go module and interact via CONTRACT-003

## Constraints, Assumptions, Dependencies

### Constraints

- **No CGo** — pure Go for easy cross-compilation and embedding
- **No TUI dependencies** — headless only, no terminal UI libraries
- **Minimal dependency footprint** — avoid large frameworks; prefer standard
  library + provider SDKs
- **Must be embeddable** — no global state, no init() side effects, all
  configuration via explicit parameters

### Assumptions

- LM Studio or Ollama is running locally when local models are requested
- Local models with tool-calling support (Qwen 2.5+, Llama 3.1+) are
  available and loaded
- The harness interface (CONTRACT-003) is stable enough to target

### Dependencies

- LM Studio daemon (`lms daemon up`) or Ollama service for local models
- Cloud API keys (Anthropic, OpenAI) for cloud provider support
- Go toolchain 1.23+

## Risks

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Local model tool calling unreliable | Medium | High | Test against specific model versions (Qwen 3.5, Llama 3.2); implement retry with cloud fallback |
| LM Studio API breaks compatibility | Low | Medium | Pin to known-good LM Studio version; test in CI against local instance |
| openai-go SDK doesn't handle LM Studio edge cases | Medium | Medium | Thin adapter layer that can work around SDK limitations |
| Harness interface changes during development | Low | Low | DDX Agent defines its own API first; the adapter is a thin shim |
| Local model context window too small for large tasks | Medium | Medium | Model routing considers context length; auto-escalate large prompts to cloud |

## Resolved Decisions

- **Model loading**: Assume models are pre-loaded. DDX Agent does not manage
  `lms load` / `ollama pull`. Model selection optimization is P2.
- **Routing**: Callers own cross-harness selection. Within the embedded harness,
  DDX Agent owns model-first provider routing keyed by requested model or model
  ref; legacy backend pools are compatibility-only during migration.
- **Config**: Library takes a `Config` struct that any embedder provides. The
  standalone CLI has its own config reader. Library has no config file opinions.
- **File paths**: Allow paths outside working directory. Expectation is the
  agent runs in a sandbox. Log all file operations regardless.
- **Architecture**: Ghostty model — great library, proven by usable app.
- **Observability**: JSONL remains the replay artifact, while OTel is the
  canonical analytics surface. Report provider/gateway cost when available,
  otherwise use runtime-specific configured pricing or record cost as unknown.
  Do not guess cost from generic stale price tables.

## Open Questions

None at this time. JSONL is the local replay artifact, and OTel is the
canonical analytics surface per ADR-001.

## Success Criteria

- DDX Agent library compiles with `go build` and has no CGo dependencies
- `agent.Run()` can complete a file-read-and-edit task using LM Studio locally
- `agent.Run()` can complete the same task using Claude API
- A caller can use DDX Agent as an in-process harness via CONTRACT-003
- A HELIX build pass can execute a bead using DDX Agent with a local model
- Token usage and timing are accurately reported for both local and cloud
