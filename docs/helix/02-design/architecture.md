---
ddx:
  id: helix.arch
  depends_on:
    - SD-001
    - SD-002
    - CONTRACT-003
---
# Architecture — DDX Agent

## System Context

DDX Agent is a library-first execution service. Callers submit intent
(`prompt`, `model`, `model_ref`, `profile`, `provider`, `harness`,
permissions, reasoning) through the public `DdxAgent` contract. The service
owns routing, provider construction, execution, event normalization, and
session-log persistence.

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│ Orchestrator │     │ CI / Worker  │     │ agent CLI    │
│ (in-process) │     │ (in-process) │     │ (binary)     │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       └────────────┬───────┴────────────────────┘
                    │
            ┌───────▼────────┐
            │ DdxAgent       │
            │ service API    │
            │ Execute/List/* │
            └───────┬────────┘
                    │
        ┌───────────┴────────────┐
        │                        │
┌───────▼────────┐      ┌────────▼────────┐
│ Native path     │      │ Subprocess path │
│ route/provider  │      │ harness runners │
│ + core loop     │      │ (claude/codex/  │
│ + tools         │      │ gemini/pi/...)  │
└───────┬─────────┘      └────────┬────────┘
        │                         │
 ┌──────▼──────────┐      ┌───────▼─────────┐
 │ provider        │      │ PTY / subprocess │
 │ adapters        │      │ integration      │
 │ openai/omlx/... │      └──────────────────┘
 └─────────────────┘
```

## Module Boundaries

### 1. CLI module: `cmd/agent`

Responsibilities:

- parse flags, stdin, env, and project working directory
- build public service requests
- call `agent.New`, `Execute`, `TailSessionLog`, `List*`, `ResolveProfile`,
  `ResolveRoute`, `RouteStatus`
- decode events with `DecodeServiceEvent` or `DrainExecute`
- render stdout/stderr/JSON and map status to process exit codes

Must not own:

- native provider construction
- route candidate ordering or failover
- direct `internal/core` loop invocation
- session lifecycle persistence by replaying service events into internal
  session-log types

### 2. Service module: root `agent` package and `service*.go`

Responsibilities:

- public contract for execution, route resolution, model/provider listing, and
  health/status
- request validation and route resolution
- native provider selection and construction from configured providers/endpoints
- subprocess harness dispatch
- event emission and typed event decoding
- session-log persistence and routing attribution
- failover policy for native-route execution

This is the only public execution boundary. `internal/core` is an implementation
detail used by the service for the native harness.

### 3. Provider adapter and routing modules: `internal/provider/*`,
`internal/routing`, model catalog, and service-owned native wrappers

Responsibilities:

- translate service/provider config into concrete provider implementations
- map public reasoning/model controls to provider-specific wire formats
- discover models and endpoint health
- rank and filter candidates
- execute native failover and report `routing_actual`

These modules are not consumer APIs. They exist to keep provider-specific and
routing-specific behavior behind the service boundary.

## Package View

```
agent/
├── *.go                        # public types and DdxAgent service methods
├── loop.go                     # native core loop implementation
├── stream_consume.go           # streaming helper for native providers
├── compaction/                 # conversation compaction
├── telemetry/                  # runtime telemetry scaffolding
├── tool/                       # built-in native tools
├── session/                    # log/replay/usage support
├── internal/
│   ├── provider/               # backend adapters
│   ├── routing/                # candidate ranking and routing policy
│   ├── harnesses/              # subprocess harness registry and runners
│   ├── modelcatalog/           # profile/model catalog
│   └── ...                     # config, safefs, prompt helpers, etc.
└── cmd/
    └── agent/                  # first-party CLI consumer of the service
```

## Execution Flow

### Native (`agent`) harness

```
CLI / caller
  -> DdxAgent.Execute(req)
  -> service route resolution
  -> service-native provider construction
  -> core loop
  -> tools / compaction / telemetry
  -> service final event + session log
```

### Subprocess harnesses

```
CLI / caller
  -> DdxAgent.Execute(req)
  -> service route resolution
  -> harness runner selection
  -> PTY / subprocess execution
  -> normalized service events
  -> service final event + session log
```

## Architectural Rules

1. The CLI is the first consumer of the service, not a parallel execution path.
2. `internal/core` is never called from `cmd/agent`.
3. Provider construction happens inside the service/provider-adapter layer.
4. Config-backed route planning and failover are service concerns.
5. Session logs are written by service-owned execution, not synthesized in the
   CLI from decoded events.
6. Any new CLI-visible execution/status behavior must be added to
   `CONTRACT-003` before the CLI reaches into internals to fetch it.

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Public boundary | `DdxAgent` service contract | One execution API for CLI and embedders |
| Native execution | service-owned wrapper around `internal/core` | Preserve one core loop while hiding internals |
| Provider ownership | service/provider adapters | Keep backend-specific behavior out of consumers |
| Routing ownership | service + `internal/routing` | One place for candidate ranking and failover |
| Session logging | service-owned persistence | Avoid dual schemas and lifecycle drift |
| CLI framework | `flag` stdlib | Minimal binary surface |
