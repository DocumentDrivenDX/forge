---
ddx:
  id: helix.arch
  depends_on:
    - SD-001
    - SD-002
---
# Architecture — Forge

## System Context

Forge is an embeddable Go agent runtime. It sits between a caller (DDx/HELIX,
CI system, or standalone CLI) and one or more LLM backends (LM Studio, Ollama,
Anthropic, OpenAI).

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  DDx / HELIX │     │  CI Pipeline │     │  forge CLI   │
│  (in-process)│     │  (in-process)│     │  (binary)    │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       └────────────┬───────┘────────────────────┘
                    │
            ┌───────▼───────┐
            │  forge library │
            │  forge.Run()   │
            └───────┬───────┘
                    │
       ┌────────────┼────────────┐
       │            │            │
┌──────▼──────┐ ┌───▼────┐ ┌────▼─────┐
│  LM Studio  │ │ Ollama │ │Anthropic │
│ localhost:   │ │ :11434 │ │  API     │
│ 1234        │ │        │ │          │
└─────────────┘ └────────┘ └──────────┘
```

## Container Diagram

Forge is a Go module with the following package structure:

```
forge/                          # root module: github.com/your-org/forge
├── forge.go                    # Run(), Request, Result, Provider, Tool interfaces
├── loop.go                     # agent loop implementation
├── provider/
│   ├── openai/
│   │   └── openai.go           # OpenAI-compatible provider (LM Studio, Ollama, OpenAI, etc.)
│   └── anthropic/
│       └── anthropic.go        # Anthropic Claude provider
├── tool/
│   ├── read.go                 # file read tool
│   ├── write.go                # file write tool
│   ├── edit.go                 # find-replace edit tool
│   └── bash.go                 # shell command tool
├── session/
│   ├── logger.go               # JSONL session event logger
│   ├── event.go                # event type definitions
│   ├── replay.go               # session replay renderer
│   ├── pricing.go              # model pricing table and cost estimation
│   └── usage.go                # usage aggregation (P1)
└── cmd/
    └── forge/
        └── main.go             # standalone CLI binary
```

## Component Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                       forge (root package)                   │
│                                                             │
│  ┌────────────┐    ┌──────────────┐    ┌────────────────┐  │
│  │   Run()    │───▶│  Loop Engine │───▶│ EventCallback  │  │
│  │  Request   │    │              │    │  (optional)    │  │
│  │  Result    │    │  - iterate   │    └────────┬───────┘  │
│  └────────────┘    │  - dispatch  │             │          │
│                    │    tools     │    ┌────────▼───────┐  │
│  Interfaces:       │  - accumulate│    │ session.Logger │  │
│  - Provider        │    tokens   │    │  (JSONL writer) │  │
│  - Tool            └──────┬──────┘    └────────────────┘  │
│                           │                                │
└───────────────────────────┼────────────────────────────────┘
                            │
              ┌─────────────┼─────────────┐
              │             │             │
      ┌───────▼──────┐ ┌───▼────┐ ┌──────▼──────┐
      │  Provider     │ │  Tool  │ │  Session    │
      │  Impls        │ │  Impls │ │  Services   │
      │              │ │        │ │             │
      │ openai/      │ │ read   │ │ logger      │
      │ anthropic/   │ │ write  │ │ replay      │
      │              │ │ edit   │ │ pricing     │
      │              │ │ bash   │ │ usage       │
      └──────────────┘ └────────┘ └─────────────┘
```

## Data Flow

### Agent Loop Sequence

```
Caller                  Loop Engine          Provider         Tools          Logger
  │                         │                   │               │              │
  │──Run(ctx, Request)─────▶│                   │               │              │
  │                         │──session.start────▶               │              │
  │                         │                   │               │           ◀──│
  │                         │──Chat(messages)──▶│               │              │
  │                         │◀─Response─────────│               │              │
  │                         │──llm.response─────▶               │              │
  │                         │                   │               │           ◀──│
  │                         │   [if tool calls]                 │              │
  │                         │──Execute(params)──────────────────▶              │
  │                         │◀─result───────────────────────────│              │
  │                         │──tool.call────────▶               │              │
  │                         │                   │               │           ◀──│
  │                         │   [loop until text-only or limit]              │
  │                         │──session.end──────▶               │              │
  │◀─Result────────────────│                   │               │           ◀──│
```

## Deployment

Forge has two deployment modes:

1. **Library** (primary): Imported as a Go module. No deployment — compiled
   into the host binary.
2. **CLI** (showcase): Single static binary built with `go build ./cmd/forge`.
   Distributed as a download or installed via `go install`.

No containers, no services, no infrastructure. Forge is a library.

## Key Design Decisions

See SD-001 for full decision log. Summary:

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Package layout | Layered with internal | Idiomatic Go, testable |
| Session logging | JSONL | Simple, appendable, jq-compatible |
| Observability | JSONL-first, OTel P1 | Avoid premature dependency |
| Provider interface | In consuming package | Go idiom |
| Tool interface | JSON Schema based | Model-agnostic |
| CLI framework | `flag` stdlib | Minimal, no dependency |
| Config format | YAML | DDx convention |
