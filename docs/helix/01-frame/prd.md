---
ddx:
  id: helix.prd
---
# Product Requirements Document — Forge

## Summary

Forge is a Go library that implements a coding agent runtime — a tool-calling
LLM loop with file read/write, shell execution, and structured I/O — designed
to be embedded in DDx and other build orchestrators. It prioritizes local
model inference via LM Studio and Ollama, with transparent escalation to cloud
providers when local models are insufficient. Forge replaces the
subprocess-based agent dispatch in DDx with an in-process alternative that
eliminates process overhead, enables direct state sharing, and provides native
cost control. Following the ghostty model — great library, proven by a usable
app — Forge ships as a Go package plus a thin standalone CLI that showcases the
library and serves as the DDx harness backend. Every LLM interaction and tool
call is logged and replayable, with per-model cost tracking built in. Success
means DDx can run a HELIX build pass where 70%+ of routine tasks use local
models at near-zero cost, and the operator can replay any session to understand
exactly what happened.

## Problem and Goals

### Problem

DDx dispatches work to AI agents by shelling out to standalone CLIs (claude,
codex, pi, opencode). Each invocation spawns a process, re-reads the codebase,
re-establishes context, and returns unstructured text that DDx must parse.
This is slow (~2-5s overhead per invocation), expensive (full cloud pricing on
every call including context re-establishment), and lossy (no shared state
between invocations). Local models are theoretically supported but require
running a separate agent CLI that may not support LM Studio or may not handle
tool calling reliably.

In a typical HELIX build pass, 70% of agent tasks are mechanical — reading
files, applying templated edits, running tests, scaffolding boilerplate. These
don't need a $15/MTok cloud model. But the current architecture treats every
task the same: spawn a process, send to cloud, parse the result.

### Goals

1. **Embed the agent loop in Go** — provide a `forge.Run(ctx, prompt, opts)`
   API that DDx calls in-process, eliminating subprocess overhead
2. **Local-model-first** — native LM Studio and Ollama support with tool
   calling, making local models the default for routine tasks
3. **Structured I/O** — accept DDx prompt envelopes, return structured results
   with status, output, token usage, and timing
4. **Full observability** — every LLM turn and tool call logged, replayable,
   cost-tracked. Pattern off DDx's session logging (JSONL + per-session detail)
5. **Prove it with an app** — standalone `forge` CLI that showcases the library
   and serves as a DDx harness, following the ghostty pattern

### Success Metrics

| Metric | Target | Measurement Method |
|--------|--------|--------------------|
| Subprocess elimination | Forge handles ≥1 DDx harness in-process | DDx integration test |
| Local model completion rate | ≥70% of routine tasks succeed on local 7B+ | HELIX build pass logs |
| Cost per bead (blended) | <$0.05 average | DDx agent usage report |
| Agent loop overhead | <10ms beyond model inference time | Benchmark suite |

### Non-Goals

- **TUI or interactive mode** — Forge is headless-only. Interactive use goes
  through pi, claude, or other standalone agents.
- **MCP server** — Forge provides tools directly, not via MCP protocol.
- **Prompt engineering** — Forge executes prompts; the caller (HELIX/DDx) owns
  prompt design and persona injection.
- **Model hosting** — Forge connects to LM Studio/Ollama/cloud APIs. It does
  not run inference itself.
- **IDE integration** — Forge is a library, not an editor plugin.

## Users and Scope

### Primary Persona: DDx/HELIX Orchestrator

**Role**: The DDx agent service and HELIX execution loop (software, not a human)
**Goals**: Dispatch agent work with minimal overhead, control cost, get structured results
**Pain Points**: Subprocess spawning is slow, cloud-only is expensive, output parsing is fragile

### Secondary Persona: Build System Integrator

**Role**: Developer embedding Forge in custom CI pipelines or build tools
**Goals**: Run LLM-powered code tasks (fix lint, update deps, generate tests) in Go programs
**Pain Points**: Existing agent CLIs require process management, don't expose a library API

### Tertiary Persona: DDx CLI User

**Role**: Developer using `ddx agent run` from the command line
**Goals**: Faster, cheaper agent invocations with local model support
**Pain Points**: Current harness is slower than necessary, no local model path

## Requirements

### Must Have (P0)

1. **Agent loop** — tool-calling LLM loop: send prompt → model responds with
   tool calls or text → execute tools → repeat until done or max iterations
2. **Tool set** — read (file contents), write (create/overwrite file), edit
   (find-replace in file), bash (execute shell command with timeout)
3. **OpenAI-compatible provider** — generic provider for any OpenAI-compatible
   endpoint. Covers LM Studio (localhost:1234), Ollama (localhost:11434),
   OpenAI, Azure, Groq, Together, OpenRouter. Single implementation, configure
   by base URL.
4. **Anthropic provider** — Claude API support for cloud use
5. **Structured I/O** — accept prompt as string or DDx envelope, return
   structured result (status, output, tool calls made, tokens, duration, error)
6. **Go library API** — `forge.Run(ctx, request) (Result, error)` as the
   primary interface. Library takes a Config struct; no global state.
7. **Token tracking** — count and report input/output tokens per invocation
8. **Iteration limit** — configurable max tool-call iterations to prevent
   runaway loops
9. **Working directory** — file operations scoped to a configurable root.
   Paths outside working dir are allowed (sandbox assumption) but logged.
10. **Session logging** — every LLM request/response and tool call recorded to
    JSONL log (patterned on DDx `SessionEntry`). Full prompt and response bodies
    stored. Logs must support replay — reading a session log reproduces the
    complete conversation including tool calls and results.
11. **Cost tracking** — per-model pricing table (patterned on DDx
    `agent.Pricing`), cost estimated per session and accumulated across sessions.
    `forge.Result` includes `CostUSD`.
12. **Standalone CLI** — `forge` binary wrapping the library. Proves the library
    works, serves as the DDx harness backend. Reads its own config file
    (patterned on `.ddx/config.yaml`). Accepts prompt via `-p` flag or stdin.

### Should Have (P1)

1. **System prompt composition** — base system prompt + caller-provided
   additions (persona, project context, conventions) — **Implemented**
2. **Session continuity** — option to carry conversation history across
   multiple `Run` calls within a session
3. **Streaming callbacks** — caller can receive tool call events and partial
   responses in real time — **Implemented**
4. **Timeout management** — per-invocation and per-tool-call timeouts
5. **DDx harness adapter** — implement the DDx harness interface so Forge
   appears as a native `ddx agent` harness alongside claude/codex/pi
6. **Usage reporting** — `forge usage` command (pattern off `ddx agent usage`):
   per-provider/model token and cost summaries with time-window filtering
7. **Session replay** — `forge replay <session-id>` reads a session log and
   prints the conversation in human-readable form (every turn, tool call,
   result, tokens, timing)
8. **OpenTelemetry spans** — optionally emit OTel spans for each agent loop
   iteration, LLM call, and tool execution. If OTel overhead or complexity is
   unreasonable, fall back to the JSONL log as the primary observability
   surface.
9. **Conversation compaction** — auto-summarize long conversation histories
   to fit within model context windows — **Implemented**

### Nice to Have (P2)

1. **Grep tool** — search file contents (read-only, useful for codebase nav)
2. **Find/glob tool** — find files by pattern
3. **Caching** — cache file reads within a session to reduce redundant I/O
4. **Multi-provider round robin** — configure multiple providers, distribute
   requests across them. Phase 2 routing strategy.
5. **Multi-model consensus** — run same prompt on N models, return majority
   answer (mirrors DDx quorum)
6. **Model selection optimization** — choose model based on task
   characteristics (context length, complexity heuristics)

## Functional Requirements

### Agent Loop

- The loop MUST send the prompt + conversation history to the LLM provider
- When the LLM responds with tool calls, Forge MUST execute each tool and
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
- Providers MUST report token usage when the upstream API provides it
- Providers MUST support tool/function calling in the format the model expects
- The LM Studio provider MUST connect to `http://localhost:1234/v1` by default
  with configurable host/port
- The Ollama provider MUST connect to `http://localhost:11434` by default

### Structured I/O

- Input: plain string prompt, or DDx prompt envelope (JSON with kind, id,
  prompt, inputs, response_schema fields)
- Output: `Result` struct with: status (success/failure/timeout/cancelled),
  output (final text), tool_calls (log of all tool calls), tokens
  (input/output/total), duration_ms, error (if any), model (which model was
  used)

## Acceptance Test Sketches

| Requirement | Scenario | Input | Expected Output |
|-------------|----------|-------|-----------------|
| Agent loop basics | Simple file read task | "Read main.go and tell me the package name" | Result with status=success, output contains package name |
| Tool: edit | Find-replace in file | Prompt to rename a function | File on disk is modified, result shows edit tool call |
| Tool: bash | Run tests | "Run `go test ./...` and report results" | Result includes test output, exit code |
| LM Studio provider | Connect to local model | Prompt with LM Studio running Qwen 3.5 | Successful completion with token count |
| Iteration limit | Prevent runaway | Max 3 iterations, task needs 10 | Result with status=failure after 3 rounds |
| Structured I/O | DDx envelope | JSON envelope with prompt and response_schema | Result matches schema |
| Token tracking | Count tokens | Any successful completion | Result.tokens has non-zero input and output counts |
| Session logging | Run any task | Any successful completion | JSONL log entry with full prompt, response, tool calls, tokens, timing |
| Session replay | Read logged session | `forge replay <id>` on a completed session | Human-readable dump of every turn, tool call, and result |
| Cost tracking | Run cloud task | Claude API completion | Result.CostUSD > 0, matches pricing table estimate |
| Cost tracking | Run local task | LM Studio completion | Result.CostUSD == 0 (local model, no cost) |
| Standalone CLI | End-to-end | `forge -p "Read main.go"` with config file | Successful completion, session logged |

## Technical Context

- **Language/Runtime**: Go 1.23+
- **Key Libraries**:
  - `github.com/openai/openai-go` — OpenAI-compatible API client (LM Studio,
    Ollama, OpenAI, Azure)
  - `github.com/anthropics/anthropic-sdk-go` — Anthropic Claude API client
  - Standard library for HTTP, JSON, process execution
- **Build**: `go build`, Makefile consistent with DDx CLI patterns
- **Platform Targets**: Linux (primary — CI and build servers), macOS
  (development). Windows is not a priority.
- **Integration Point**: DDx CLI (`cli/internal/agent/`) — Forge implements
  the `Harness` interface or provides an adapter

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
- The DDx agent service (FEAT-006) harness interface is stable enough to
  target

### Dependencies

- LM Studio daemon (`lms daemon up`) or Ollama service for local models
- Cloud API keys (Anthropic, OpenAI) for cloud provider support
- DDx CLI codebase for harness integration
- Go toolchain 1.23+

## Risks

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Local model tool calling unreliable | Medium | High | Test against specific model versions (Qwen 3.5, Llama 3.2); implement retry with cloud fallback |
| LM Studio API breaks compatibility | Low | Medium | Pin to known-good LM Studio version; test in CI against local instance |
| openai-go SDK doesn't handle LM Studio edge cases | Medium | Medium | Thin adapter layer that can work around SDK limitations |
| DDx harness interface changes during development | Low | Low | Forge defines its own API first; DDx adapter is a thin shim |
| Local model context window too small for large tasks | Medium | Medium | Model routing considers context length; auto-escalate large prompts to cloud |

## Resolved Decisions

- **Model loading**: Assume models are pre-loaded. Forge does not manage
  `lms load` / `ollama pull`. Model selection optimization is P2.
- **Routing**: Phase 1 is dumb — configure one server, it works or it doesn't.
  Phase 2 adds multiple providers with round robin (P2).
- **Config**: Library takes a `Config` struct that DDx (or any embedder)
  provides. The standalone CLI has its own config reader (patterned on DDx's
  `.ddx/config.yaml`). Library has no config file opinions.
- **File paths**: Allow paths outside working directory. Expectation is the
  agent runs in a sandbox. Log all file operations regardless.
- **Architecture**: Ghostty model — great library, proven by usable app.

## Open Questions

- [ ] OTel vs custom logging: Is OTel span overhead acceptable for per-tool-call
  granularity, or should we rely on JSONL as primary and OTel as optional? —
  blocks logging architecture, resolve during design
- [ ] Session log format: JSONL (one line per event, DDx-compatible) vs
  per-session directories with separate files for prompt/response bodies (DDx
  FEAT-006 attachment model)? — blocks FEAT-005, resolve during design

## Success Criteria

- Forge library compiles with `go build` and has no CGo dependencies
- `forge.Run()` can complete a file-read-and-edit task using LM Studio locally
- `forge.Run()` can complete the same task using Claude API
- DDx can use Forge as an in-process harness via adapter
- A HELIX build pass can execute a bead using Forge with a local model
- Token usage and timing are accurately reported for both local and cloud
