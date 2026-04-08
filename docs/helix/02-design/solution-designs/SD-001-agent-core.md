---
ddx:
  id: SD-001
  depends_on:
    - FEAT-001
    - FEAT-002
    - FEAT-003
    - FEAT-004
    - FEAT-005
---
# Solution Design: SD-001 — DDX Agent Core Library

**Features**: FEAT-001 (Agent Loop), FEAT-002 (Tools), FEAT-003 (Providers),
FEAT-004 (Provider Config), FEAT-005 (Logging & Cost)

## Scope

Feature-level design for the DDX Agent Go library — everything except the
standalone CLI binary (SD-002). Covers the agent loop, tool set, provider
interface, session logging, and cost tracking.

## Requirements Mapping

### Functional Requirements

| Requirement | Technical Capability | Package | Priority |
|-------------|---------------------|---------|----------|
| Agent loop (PRD P0-1) | `agent.Run()` tool-calling loop | `ddx-agent` | P0 |
| Tool set (PRD P0-2) | read/write/edit/bash tools | `agent/tool` | P0 |
| OpenAI-compat provider (PRD P0-3) | Chat completion via openai-go | `agent/provider/openai` | P0 |
| Anthropic provider (PRD P0-4) | Chat completion via anthropic-sdk-go | `agent/provider/anthropic` | P0 |
| Structured I/O (PRD P0-5) | Request/Result types | `ddx-agent` | P0 |
| Go library API (PRD P0-6) | `agent.Run(ctx, Request) (Result, error)` | `ddx-agent` | P0 |
| Token tracking (PRD P0-7) | Accumulate per-iteration token counts | `ddx-agent` | P0 |
| Iteration limit (PRD P0-8) | Configurable max iterations | `ddx-agent` | P0 |
| Working directory (PRD P0-9) | File ops scoped to root | `agent/tool` | P0 |
| Session logging (PRD P0-10) | JSONL event log | `agent/session` | P0 |
| Cost tracking (PRD P0-11) | Per-model pricing table | `agent/session` | P0 |

### NFR Impact on Architecture

| NFR | Requirement | Architectural Impact | Design Decision |
|-----|-------------|---------------------|-----------------|
| Performance | <1ms loop overhead per iteration | No reflection, no allocs in hot path | Direct struct passing, no interface boxing in loop |
| Concurrency | Multiple concurrent `Run` calls | No global state | All state in Request/session structs |
| Embeddability | No global state, no init() | Config via explicit parameters | Config struct, no package-level vars |
| Testability | Mockable providers | Provider as interface | `Provider` interface in consuming `ddx-agent` package |
| No CGo | Pure Go cross-compilation | No C dependencies | stdlib + provider SDKs only |

## Solution Approaches

### Approach 1: Monolithic Package

All code in a single `ddx-agent` package. Simple, but grows unwieldy as
providers and tools accumulate.

**Pros**: Simple imports, no internal dependency management.
**Cons**: Large package, unclear boundaries, hard to test providers in isolation.
**Evaluation**: Rejected — violates Go idiom of small, focused packages.

### Approach 2: Layered Packages with Internal

Standard Go layout: public API in `ddx-agent`, implementation in `internal/`
sub-packages, provider SDKs isolated in their own packages.

**Pros**: Clean API surface, testable in isolation, providers are swappable.
**Cons**: Slightly more boilerplate for package boundaries.
**Evaluation**: **Selected** — idiomatic Go, clean dependency graph, each
package is independently testable.

**Selected Approach**: Layered packages. The `ddx-agent` package defines the public
API (`Run`, `Request`, `Result`, `Provider`, `Tool`). Internal packages
implement specific concerns. Provider packages are siblings under `provider/`.

## Domain Model

### Core Types

```
Request {
    Prompt       string
    SystemPrompt string
    Provider     Provider        // configured provider instance
    Tools        []Tool          // available tools
    MaxIter      int             // iteration limit
    WorkDir      string          // working directory for file ops
    Callback     EventCallback   // optional streaming callback
    Metadata     map[string]string // correlation metadata (bead_id, etc.)
}

Result {
    Status     Status          // success | iteration_limit | cancelled | error
    Output     string          // final text response
    ToolCalls  []ToolCallLog   // log of all tool calls
    Tokens     TokenUsage      // accumulated input/output/total
    Duration   time.Duration
    CostUSD    float64         // estimated cost (-1 if unknown)
    Model      string          // model that was used
    Error      error           // non-nil on status=error
    SessionID  string          // for log correlation
}

Status = success | iteration_limit | cancelled | error

Provider interface {
    Chat(ctx, []Message, []ToolDef, Options) (Response, error)
}

Response {
    Content    string
    ToolCalls  []ToolCall
    Usage      TokenUsage
    Model      string
    FinishReason string
}

Message {
    Role    Role              // system | user | assistant | tool
    Content string
    ToolCalls []ToolCall       // for assistant messages
    ToolCallID string          // for tool result messages
}

Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage  // JSON Schema for parameters
    Execute(ctx, params json.RawMessage) (string, error)
}

TokenUsage {
    Input  int
    Output int
    Total  int
}

EventCallback func(Event)     // optional real-time event sink
```

### Business Rules

1. **Loop termination**: The loop ends when the model returns text with no
   tool calls, max iterations is reached, or the context is cancelled.
2. **Tool execution is sequential**: Tool calls in a single response are
   executed one at a time in order. No parallel tool execution in P0.
3. **Provider immutability**: A Provider instance is configured once and
   reused across calls. No per-call reconfiguration.
4. **Cost unknown vs free**: CostUSD = -1 means "model not in pricing table"
   (unknown). CostUSD = 0 means "free" (local model with $0 pricing entry).

## System Decomposition

### Package: `ddx-agent` (root)

- **Purpose**: Public API surface — `Run()`, `Request`, `Result`, interfaces
- **Responsibilities**: Orchestrate the agent loop, accumulate tokens/cost,
  manage iteration counting, handle context cancellation
- **Requirements**: FEAT-001 (all), FEAT-004 P0 (single provider config)
- **Interfaces**: Consumes `Provider` and `Tool` interfaces; produces `Result`

### Package: `agent/provider/openai`

- **Purpose**: OpenAI-compatible provider implementation
- **Responsibilities**: Translate `agent.Message`/`agent.ToolDef` to OpenAI
  wire format, parse responses, report token usage
- **Requirements**: FEAT-003 (OpenAI-compat section)
- **Interfaces**: Implements `agent.Provider`; uses `github.com/openai/openai-go`

### Package: `agent/provider/anthropic`

- **Purpose**: Anthropic Claude provider implementation
- **Responsibilities**: Translate to Anthropic Messages API format, handle
  content blocks, parse tool use blocks
- **Requirements**: FEAT-003 (Anthropic section)
- **Interfaces**: Implements `agent.Provider`; uses `github.com/anthropics/anthropic-sdk-go`

### Package: `agent/tool`

- **Purpose**: Built-in tool implementations
- **Responsibilities**: File read/write/edit, bash execution, working directory
  scoping, output truncation
- **Requirements**: FEAT-002 (all)
- **Interfaces**: Each tool implements `agent.Tool`

### Package: `agent/session`

- **Purpose**: Session logging, replay, and cost tracking
- **Responsibilities**: Write JSONL event logs, pricing table lookup,
  cost estimation, replay rendering
- **Requirements**: FEAT-005 (all)
- **Interfaces**: Implements `agent.EventCallback` for the agent loop to emit
  events; provides `Logger` for session lifecycle

### Component Interactions

```
┌─────────────────────────────────────────────┐
│                  Caller                      │
│         agent.Run(ctx, Request)              │
└──────────────────┬──────────────────────────┘
                   │
┌──────────────────▼──────────────────────────┐
│              agent (root)                    │
│  ┌─────────┐  ┌──────────┐  ┌───────────┐  │
│  │  Loop   │──│ Provider │  │  Session   │  │
│  │ Engine  │  │(interface)│  │  Logger    │  │
│  └────┬────┘  └──────────┘  └───────────┘  │
│       │                                     │
│  ┌────▼────┐                                │
│  │  Tool   │                                │
│  │(interface)                               │
│  └─────────┘                                │
└─────────────────────────────────────────────┘
         │                    │
    ┌────▼────┐          ┌───▼────┐
    │ tool/*  │          │provider│
    │read,write│         │openai/ │
    │edit,bash │         │anthropic/
    └─────────┘          └────────┘
```

## Technology Rationale

| Layer | Choice | Why | Alternatives Rejected |
|-------|--------|-----|----------------------|
| Language | Go 1.23+ | PRD mandate, embeddable, no CGo | N/A |
| OpenAI SDK | `openai/openai-go` | Official SDK, covers LM Studio/Ollama | Hand-rolled HTTP (more maintenance) |
| Anthropic SDK | `anthropics/anthropic-sdk-go` | Official SDK | Hand-rolled HTTP |
| Logging format | JSONL | Appendable, streamable, jq-compatible, DDx-compatible | Per-session directories (over-engineered for P0) |
| Structured logging | `log/slog` | Stdlib, no dependency | zerolog/zap (unnecessary dependency) |
| Config | Go struct | Library has no config file opinions | Viper (too heavy for a library) |
| Testing | `go test` + testify | Stdlib runner, readable assertions | Ginkgo (too heavy) |

## Key Design Decisions

### D1: JSONL for session logs (resolves PRD open question)

JSONL with one event per line. Full prompt/response bodies inline in the event
(not external files). Rationale: simpler to implement, simpler to replay,
`jq` friendly, DDx-compatible. Per-session directories with external
attachments can be added later if body sizes become a problem.

### D2: JSONL-first observability, OTel optional (resolves PRD open question)

JSONL session log is the primary observability surface. OTel span emission is
P1 — if OTel is added, it wraps the same events. Rationale: OTel adds
dependency weight and configuration complexity. The JSONL log provides full
replay capability without it. OTel is additive, not a replacement.

### D3: Provider interface defined in `ddx-agent` package

The `Provider` interface lives in the consuming `ddx-agent` package, not in a
`provider` package. This follows the Go idiom: "accept interfaces, return
structs." Provider implementations are in `agent/provider/openai` and
`agent/provider/anthropic`.

### D4: Tools as an interface with JSON Schema

Tools implement a `Tool` interface with `Name()`, `Description()`,
`Schema() json.RawMessage`, and `Execute()`. The schema is the OpenAI-format
JSON Schema that gets sent to the model. This is model-agnostic — both
OpenAI and Anthropic accept JSON Schema for tool definitions.

### D5: Event callback for real-time streaming

The `Request` accepts an optional `EventCallback func(Event)`. The agent loop
calls this for every event (LLM request, LLM response, tool call, tool result).
The session logger is one implementation of this callback. Callers can also
use it for progress reporting. This avoids coupling the loop to the logger.

## Session Log Format

Each line is a JSON object with a common envelope:

```json
{"session_id":"s-abc123","seq":0,"type":"session.start","ts":"2026-04-06T10:00:00Z","data":{...}}
{"session_id":"s-abc123","seq":1,"type":"llm.request","ts":"...","data":{"messages":[...],"tools":[...]}}
{"session_id":"s-abc123","seq":2,"type":"llm.response","ts":"...","data":{"content":"...","tool_calls":[...],"usage":{"input":150,"output":42},"cost_usd":0.003,"latency_ms":1200}}
{"session_id":"s-abc123","seq":3,"type":"tool.call","ts":"...","data":{"tool":"read","input":{"path":"main.go"},"output":"package main...","duration_ms":2}}
{"session_id":"s-abc123","seq":4,"type":"session.end","ts":"...","data":{"status":"success","output":"...","tokens":{"input":500,"output":200},"cost_usd":0.01,"duration_ms":5000}}
```

## Pricing Table

Built-in pricing for common models. Extensible via config.

```go
var DefaultPricing = map[string]ModelPricing{
    "claude-sonnet-4-20250514":  {InputPerMTok: 3.00, OutputPerMTok: 15.00},
    "claude-haiku-4-20250414":   {InputPerMTok: 0.80, OutputPerMTok: 4.00},
    "gpt-4o":                    {InputPerMTok: 2.50, OutputPerMTok: 10.00},
    "gpt-4o-mini":               {InputPerMTok: 0.15, OutputPerMTok: 0.60},
    // Local models — free
    "qwen3.5-7b":                {InputPerMTok: 0, OutputPerMTok: 0},
    "llama-3.2-8b":              {InputPerMTok: 0, OutputPerMTok: 0},
}
```

Unknown models: CostUSD = -1, logged as warning.

## Error Handling

| Error Category | Source | Handling |
|----------------|--------|----------|
| Provider connection failure | Network | Return error after 3 retries with backoff |
| Provider rate limit (429) | API | Retry with exponential backoff, max 3 |
| Provider server error (500/503) | API | Retry with backoff, max 3 |
| Tool execution failure | Tool | Include error in tool result message, let model decide |
| Unknown tool call | Model | Return error for that tool call, continue loop |
| Context cancelled | Caller | Kill active tool (bash process), return partial result |
| Max iterations reached | Loop | Return Result with status=iteration_limit |
| Empty model response | Model | Treat as success with empty output |

## Security

- **Sandbox assumption**: DDX Agent assumes it runs in a sandboxed environment.
  File paths outside working directory are allowed but logged.
- **No secrets in logs**: API keys are never logged. Session logs contain
  prompts and responses — callers must not put secrets in prompts.
- **Bash tool**: Runs commands via `sh -c` with stdin from `/dev/null`.
  Process is killed on timeout or context cancellation. No shell injection
  risk beyond what the model generates — the model is trusted within the
  sandbox.
- **Output truncation**: Bash output >1MB is truncated. File reads of binary
  content return an error.

## Test Strategy

- **Unit**: Agent loop (mock provider), tool implementations (temp directories),
  session logger (write + read back), pricing calculator
- **Integration**: OpenAI provider against LM Studio (build tag `integration`),
  Anthropic provider against Claude API (build tag `e2e`)
- **E2E**: Full `agent.Run()` with real LM Studio model completing a
  file-read-and-edit task (build tag `e2e`)

## Traceability

| Requirement ID | Package | Design Element | Test Strategy |
|----------------|---------|----------------|---------------|
| FEAT-001 FR-1..11 | `ddx-agent` | Run() loop engine | Unit: mock provider loop tests |
| FEAT-002 read | `agent/tool` | ReadTool | Unit: temp file reads |
| FEAT-002 write | `agent/tool` | WriteTool | Unit: temp file writes |
| FEAT-002 edit | `agent/tool` | EditTool | Unit: find-replace tests |
| FEAT-002 bash | `agent/tool` | BashTool | Unit: command execution, timeout |
| FEAT-003 openai | `agent/provider/openai` | OpenAIProvider | Integration: LM Studio |
| FEAT-003 anthropic | `agent/provider/anthropic` | AnthropicProvider | E2E: Claude API |
| FEAT-004 P0 | `ddx-agent` | Request.Provider field | Unit: provider selection |
| FEAT-005 logging | `agent/session` | Logger, Event types | Unit: write + replay |
| FEAT-005 cost | `agent/session` | PricingTable, CostEstimate | Unit: calculation tests |

### Gaps

- None for P0. All P0 requirements are mapped.

## Concern Alignment

- **Concerns used**: go-std (areas: all)
- **Constraints honored**: `gofmt`, `go vet`, `golangci-lint`, `context.Context`
  as first param, error wrapping with context, `log/slog` for structured logging,
  interfaces in consumer package
- **ADRs referenced**: None yet (first design pass)
- **Departures**: CLI framework override — using `flag` stdlib instead of Cobra
  (applies to SD-002, not this design)

## Constraints & Assumptions

- **Constraints**: Pure Go (no CGo), no global state, minimal dependencies
- **Assumptions**: LM Studio/Ollama running locally when local models requested;
  models with tool-calling support are loaded; provider SDKs are stable
- **Dependencies**: `openai/openai-go`, `anthropics/anthropic-sdk-go`,
  `stretchr/testify` (test only)

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| Local model tool calling unreliable | M | H | Test against pinned model versions; structured retry |
| openai-go SDK doesn't handle LM Studio edge cases | M | M | Thin adapter if needed; SDK issues are well-tracked |
| Session log bodies grow large | L | M | Truncation for tool outputs >1MB; body externalization is P2 |
| Provider SDK breaking changes | L | M | Pin SDK versions in go.mod |

## Review Checklist

- [x] Requirements mapping covers all P0 functional requirements
- [x] NFR impact section shows architectural satisfaction
- [x] Two solution approaches evaluated
- [x] Selected approach rationale explains rejection
- [x] Domain model captures core types and relationships
- [x] Business rules are implementable
- [x] System decomposition assigns every requirement to a package
- [x] Package interfaces defined
- [x] Technology rationale explains choices
- [x] Traceability maps every requirement to package and test
- [x] No gaps in P0 coverage
- [x] Concern alignment verified (go-std)
- [x] Consistent with PRD and feature specs
