---
ddx:
  id: helix.arch
  depends_on:
    - SD-001
    - SD-002
---
# Architecture — DDX Agent

## System Context

DDX Agent is an embeddable Go agent runtime. It sits between a caller (DDx/HELIX,
CI system, or standalone CLI) and one or more LLM backends (LM Studio, Ollama,
Anthropic, OpenAI).

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  DDx / HELIX │     │  CI Pipeline │     │  agent CLI   │
│  (in-process)│     │  (in-process)│     │  (binary)    │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       └────────────┬───────┘────────────────────┘
                    │
            ┌───────▼───────┐
            │  agent library │
            │  agent.Run()   │
            └───────┬───────┘
                    │
       ┌────────────▼────────────┐
       │ agent model catalog     │
       │ + external manifest     │
       └────────────┬────────────┘
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

DDX Agent is a Go module with the following package structure:

```
agent/                          # root module: github.com/your-org/agent
├── agent.go                    # Run(), Request, Result, Provider, Tool interfaces
├── loop.go                     # agent loop implementation
├── modelcatalog/               # shared model catalog loader/resolver (planned)
│   ├── catalog.go              # catalog API and resolution helpers
│   └── manifest.go             # manifest loading/validation
├── provider/
│   ├── openai/
│   │   └── openai.go           # OpenAI-compatible provider (LM Studio, Ollama, OpenAI, etc.)
│   ├── anthropic/
│   │   └── anthropic.go        # Anthropic Claude provider
│   └── virtual/
│       └── virtual.go           # Virtual provider for deterministic replay
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
├── catalog/
│   └── models.yaml             # externally maintained model manifest snapshot (planned)
└── cmd/
    └── agent/
        └── main.go             # standalone CLI binary
```

## Component Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                       agent (root package)                   │
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
│  - Model Catalog          │                                │
│                           │                                │
└───────────────────────────┼────────────────────────────────┘
                            │
              ┌─────────────┼─────────────┬──────────────┐
              │             │             │              │
      ┌───────▼──────┐ ┌───▼────┐ ┌──────▼──────┐ ┌─────▼────────┐
      │  Provider     │ │  Tool  │ │  Session    │ │ Model Catalog │
      │  Impls        │ │  Impls │ │  Services   │ │  Services     │
      │              │ │        │ │             │ │               │
      │ openai/      │ │ read   │ │ logger      │ │ modelcatalog/ │
      │ anthropic/   │ │ write  │ │ replay      │ │ catalog/*.yaml│
      │ virtual/     │ │ edit   │ │ pricing     │ │               │
      │              │ │ bash   │ │ usage       │ │               │
      └──────────────┘ └────────┘ └─────────────┘ └───────────────┘
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

DDX Agent has two deployment modes:

1. **Library** (primary): Imported as a Go module. No deployment — compiled
   into the host binary.
2. **CLI** (showcase): Single static binary built with `go build ./cmd/ddx-agent`.
   Distributed as a download or installed via `go install`.

No containers, no services, no infrastructure. DDX Agent is a library.

The shared model catalog follows the same deployment shape: agent releases ship
an embedded manifest snapshot, while consumers may point at a separately
maintained external manifest file when they need newer model policy without a
full binary refresh.

## Key Design Decisions

See SD-001 for full decision log. Summary:

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Package layout | Layered with internal | Idiomatic Go, testable |
| Session logging | JSONL | Simple, appendable, jq-compatible |
| Observability | JSONL-first, OTel P1 | Avoid premature dependency |
| Provider interface | In consuming package | Go idiom |
| Model policy | Shared catalog + external manifest | Separate volatile policy/data from runtime code and preserve one owner |
| Tool interface | JSON Schema based | Model-agnostic |
| CLI framework | `flag` stdlib | Minimal, no dependency |
| Config format | YAML | DDx convention |
