# Design Plan: `claude-tui` PTY Harness Fork

**Date**: 2026-05-14 (rehydrated 2026-05-17)
**Status**: ACTIVE — prerequisite refactor merged; plan reopened against CONTRACT-004.
**Governs**: [ADR-013](./adr/ADR-013-claude-tui-pty-harness-fork.md) (pending re-acceptance)

> **Rehydration note (2026-05-17):** The prerequisite refactor (ADR-014 /
> CONTRACT-004) is now done — see AR-2026-05-17-repo amendment for the
> verification trail. Specifically:
>
> - `internal/routehealth/refresh_scheduler.go` exists and is wired at
>   `service.go:1080-1081`; the per-harness `Refresh*QuotaAsync` helpers
>   are gone.
> - Zero per-harness imports remain in production service files
>   (`service.go`, `service_providers.go`, `service_models.go`,
>   `service_subscription_quota.go`, `internal/runtimesignals/collect.go`).
> - The CONTRACT-004 sub-interfaces (`QuotaHarness`, `AccountHarness`,
>   `ModelDiscoveryHarness`) are declared in `internal/harnesses/types.go`
>   and implemented by claude/codex/gemini/opencode/pi.
> - The lint rule that forbids per-harness imports outside the dispatcher
>   exists at `internal/lint/harnessimports/analyzer.go`.
>
> Therefore the `claude-tui` harness lands as a sibling
> `internal/harnesses/claude-tui/` package implementing
> `Harness + QuotaHarness + AccountHarness + ModelDiscoveryHarness`
> against the merged contract. No service-side changes are required;
> the dispatcher already constructs runners through the registered-name
> seam (`internal/serviceimpl/execute_dispatch.go`).
>
> The implementation steps below remain the right shape. They are
> re-anchored against the merged contract in §"CONTRACT-004 Alignment"
> immediately after Requirements.

## CONTRACT-004 Alignment

The interface footprint `claude-tui.Runner` implements:

| Interface | Methods | Source of truth |
|-----------|---------|-----------------|
| `harnesses.Harness` | `Info`, `HealthCheck`, `Execute` | `internal/harnesses/types.go` |
| `harnesses.QuotaHarness` | `QuotaStatus`, `RefreshQuota`, `QuotaFreshness`, `SupportedLimitIDs` | `internal/harnesses/types.go` |
| `harnesses.AccountHarness` | `AccountStatus`, `RefreshAccount`, `AccountFreshness` | `internal/harnesses/types.go` |
| `harnesses.ModelDiscoveryHarness` | `DefaultModelSnapshot`, `ResolveModelAlias`, `SupportedAliases` | `internal/harnesses/types.go` |

Sharing seam with the existing `claude` harness:

- `internal/harnesses/anthropic/` neutral package is the only allowed
  import edge between `claude` and `claude-tui` (Step 1).
- The durable `claudeQuotaSnapshot` cache (now unexported per the
  refactor) is owned by `claude` and **mirrored** into `anthropic/` so
  `claude-tui` can read it without importing `claude`. The single-
  account-per-installation assumption from ADR-013 holds.
- `ModelDiscoveryHarness.DefaultModelSnapshot()` MUST drive live PTY
  per the no-static-fallback principle just adopted (parent bead
  `fizeau-89eca738`). The Go-literal fallback that the existing
  `claude` harness still carries at `defaultClaudeModelDiscovery()`
  is being removed in the parallel cassette/PTY chain; `claude-tui`
  is implemented from day one without that anti-pattern.

Async refresh: `claude-tui` does NOT call any `Refresh*Async` helper
directly. The service-level `routehealth.RefreshScheduler` (already
running) consumes `QuotaHarness.QuotaFreshness()` /
`AccountHarness.AccountFreshness()` and drives refreshes. The runner's
job is to implement the four interfaces correctly; the scheduler does
the rest.

Lint: the existing `internal/lint/harnessimports/analyzer.go` will
flag any cross-harness import that violates the boundary. `claude-tui`
must pass the rule from its first PR.

## Problem Statement

Routed Claude traffic must land on Anthropic subscription capacity, which is
billed against the interactive Claude CLI surface (TUI + `/usage`). The
existing `claude` harness drives `claude --print`, which lands on
API-metered request paths and is at risk of being reclassified or
deprecated relative to subscription billing. ADR-013 forks a new harness
identity, `claude-tui`, that implements the existing `harnesses.Harness`
interface and drives the Claude CLI exclusively through the PTY transport
already used for `/usage` quota probing.

This plan lays out the implementation work: package skeleton, shared-helper
extraction, interface implementation, capability evidence, and gating for
auto-routing promotion. It does not promote `claude-tui` to auto-routing —
that decision is a follow-up spec change once the evidence in this plan
lands.

## Requirements

### Functional

- `internal/harnesses/claude-tui/` exists as a sibling package implementing
  `harnesses.Harness` (`Info`, `HealthCheck`, `Execute`).
- `internal/harnesses/anthropic/` exists as a neutral shared-helper package;
  both `claude` and `claude-tui` consume it. Neither harness package imports
  the other.
- `claude-tui` is registered in `builtinHarnesses` with empty `BaseArgs`,
  empty `PermissionArgs`, empty `ModelFlag`/`ReasoningFlag`/`WorkDirFlag`,
  `IsSubscription=true`, `AutoRoutingEligible=false`, and
  `TUIQuotaCommand="/usage"`.
- `claude-tui.Runner.Execute` drives Claude via `internal/pty/session`. No
  `os/exec` invocation of `claude` lives in the runner hot path.
- Prompt delivery uses bracketed paste (`ESC[200~` … `ESC[201~` + `Enter`)
  for multi-line prompts; single keystrokes for slash commands/menu
  navigation use one logical-key event with a 25–75ms inter-key delay band.
- Model and reasoning selection drive TUI affordances when available
  (`/model`, future `/reasoning`); when no TUI affordance exists, the
  capability is reported as `gap` and a non-default request is a routing
  rejection rather than a silent flag fallback.
- Quota is read from the existing `ClaudeQuotaSnapshot` durable cache.
  `claude-tui` does not run a parallel quota probe.
- Cassette `manifest.harness.name = "claude-tui"`; binary version, command,
  terminal, timing, provenance, and env allowlist follow ADR-002 and
  ADR-013.

### Non-Functional

- Replay-only tests for `claude-tui` run in default CI without credentials
  and without provider contact.
- Record-mode tests are opt-in (same conditions as `claude` and `codex`
  record-mode tests).
- Benchmark suite (`scripts/benchmark/`) gains a `claude-tui` lane
  exercising at least one long-turn scenario; results are recorded before
  any auto-routing promotion proposal.
- No Fizeau-introduced `CLAUDE_*`/`ANTHROPIC_*`/`*_AGENT*` environment
  variables on the `claude-tui` execution path.

## Harness Interface

The interface is unchanged from `internal/harnesses/types.go`:

```go
type Harness interface {
    Info() HarnessInfo
    HealthCheck(ctx context.Context) error
    Execute(ctx context.Context, req ExecuteRequest) (<-chan Event, error)
}
```

`claude-tui.Runner` implements it as follows:

| Method | Behavior |
|--------|----------|
| `Info()` | Returns `HarnessInfo{Name: "claude-tui", Type: "subprocess", IsSubscription: true, AutoRoutingEligible: false, SupportedPermissions: ["safe", "supervised"], SupportedReasoning: []string{}, CostClass: "subscription", DefaultModel: <resolved from shared catalog>}`. `Path`/`Available` are populated from a PATH lookup of `claude` (same binary as `claude` harness). |
| `HealthCheck(ctx)` | Verifies the `claude` binary resolves and is executable (cheap, no invocation). Returns `nil` when ready. Does not probe quota — that is the service quota path's job. |
| `Execute(ctx, req)` | Starts a PTY session, drives the TUI to set model/reasoning/permission via slash commands/menus when applicable, pastes the prompt under bracketed paste, streams parsed events on the returned channel, emits a single `EventTypeFinal` and closes the channel. Setup failures (binary missing, PTY allocation failure) return as the second value; per-run failures (auth missing, quota blocked, parser desync, timeout, cancellation) are reported via a final event with `Status != "success"`. |

The shape of every event emitted is unchanged from the existing
`claude.Runner`: `routing_decision`, `tool_call`/`tool_result` where
applicable, `text_delta`, `final` with the same `FinalData` schema. The
agent loop and service-event drain treat `claude-tui` identically to
`claude`.

`ExecuteRequest` fields that have no `claude-tui` analog (e.g.
`Temperature`, `Seed` when no TUI affordance exists) are passed through
unchanged: the runner records them in request metadata and lets routing
decide whether to reject or accept the request. The runner does not
substitute a flag.

## Implementation Steps

### Step 1 — Extract shared helpers into `internal/harnesses/anthropic/`

**Goal**: Provide a neutral package both harnesses can depend on without
creating a `claude` ↔ `claude-tui` import edge.

**Work**:
- New package `internal/harnesses/anthropic/`.
- Move from `internal/harnesses/claude/`:
  - `IsClaudeQuotaExhaustedMessage`, `MarkClaudeQuotaExhaustedFromMessage`,
    and the quota-message corpus into `anthropic/quota_messages.go`.
  - `AccountInfo` plan-classification helpers if any are claude-specific
    (likely none; `harnesses.AccountInfo` is already neutral).
  - Model-name normalization helpers shared with the catalog into
    `anthropic/models.go`.
- Update `internal/harnesses/claude/` imports to consume from
  `internal/harnesses/anthropic/`. Existing `claude` tests must continue to
  pass with no behavior change.

**Tests**: Helper-level unit tests move with the helpers. `claude` package
tests stay green.

**Acceptance**: `go test ./internal/harnesses/...` passes. No `claude-tui`
package exists yet.

### Step 2 — Scaffold `internal/harnesses/claude-tui/`

**Goal**: Package compiles, registers, and implements the `Harness`
interface with a `gap`-only `Info()` and a stub `Execute` that returns
"not yet implemented" as a final event.

**Work**:
- New package `internal/harnesses/claude-tui/` (Go package name
  `claudetui`).
- `runner.go`: `Runner` struct, `Info()`, `HealthCheck()`, stub
  `Execute()`.
- `internal/harnesses/registry.go`: add `claude-tui` `HarnessConfig` entry
  per ADR-013 (empty `BaseArgs`/`PermissionArgs`/flags;
  `TUIQuotaCommand="/usage"`).
- Service dispatch wiring: extend `service_execute.go:380` (the existing
  `case "claude":` switch) and any other `case "claude":` dispatch sites
  identified by `grep -n '"claude"' service*.go` to also recognize
  `"claude-tui"`. The dispatch dispatches to the new runner; it does not
  branch by transport inside `claude`'s code.
- Wire `ListHarnesses` so `fiz` surfaces show `claude-tui`.

**Tests**:
- `runner_test.go` covers `Info()` shape, `HealthCheck()` PATH resolution,
  and the stub `Execute()` returning a `failed` final event with a clear
  reason.
- A service-level test asserts the dispatcher routes a request with
  `Harness: "claude-tui"` to the new runner (a parallel test of the
  existing `claude` dispatch test).

**Acceptance**: `go test ./...` passes. `fiz` surfaces show `claude-tui`
with all baseline rows as `gap`. No live Claude invocation yet.

### Step 3 — PTY session lifecycle + bracketed-paste prompt delivery

**Goal**: `Execute` can start a Claude TUI session, paste a prompt, observe
the assistant reply, cancel cleanly, and emit a final event with the
captured text. No model/reasoning/permission selection yet.

**Work**:
- `runner.go` Execute path:
  - Start session via `pty.session.Start` with binary `claude`, no args,
    documented env allowlist, and `req.WorkDir`.
  - Wait for TUI ready marker (reuse `ptyquota` ready detection).
  - Send bracketed paste: `\x1b[200~` + prompt bytes + `\x1b[201~` + `\r`.
  - Stream PTY output through `terminal.VT10x` frame derivation.
  - Detect end-of-turn via TUI completion marker; emit final.
  - On `ctx.Done()` send the documented cancel key, drain, and emit a
    `cancelled` final.
- `stream.go`: minimal parser that recognizes prompt echo and assistant
  reply boundaries, emits `EventTypeTextDelta` for the reply, and derives
  `FinalText` from the rendered frame.
- Record cassettes under `internal/harnesses/claude-tui/testdata/`.

**Tests**:
- Replay-only `internal/ptytest` scenarios for: short prompt happy path,
  cancellation mid-turn, timeout, EOF without final.
- Live record-mode test (opt-in) for the same scenarios. Cassettes carry
  manifest `harness.name = "claude-tui"`.

**Acceptance**: Capability rows `Run`, `FinalText`, `Cancel`, `WorkdirContext`,
`ErrorStatus`, `RequestMetadata` are demonstrable via replay cassettes
(`gap` → `pass` for those rows once evidence lands).

### Step 4 — Progress events and tool-call/result framing

**Goal**: Emit `routing_decision`, `tool_call`, `tool_result`, and
intermediate progress events on the same schema as `claude.Runner`.

**Work**:
- `stream.go`: recognize Claude TUI's tool-call rendering (block header,
  arguments, result block) from derived frames and emit
  `EventTypeToolCall`/`EventTypeToolResult` with `ToolCallData`/`ToolResultData`
  matching the existing CONTRACT-003 shapes.
- Mirror existing `RunnerDefaultResolutionEvent`/`RunnerReasoningResolutionEvent`
  emission so routing metadata is consistent with `claude`.

**Tests**: Replay scenarios with a multi-tool turn. Live cassettes record
the corresponding flow.

**Acceptance**: `ProgressEvents` row becomes evidenced.

### Step 5 — Model and reasoning discovery + selection

**Goal**: Drive `/model` (or equivalent TUI affordance) to list models and
to select a requested model. Capture and persist the result in
`harnesses.ModelDiscoveryCache` keyed by `claude-tui`.

**Work**:
- `model_discovery.go`: TUI flow that enters the model selector, captures
  the listed models from rendered frames, snapshots the result into the
  shared discovery cache, and exits the selector without applying a
  change.
- Apply-model flow: enter selector, navigate to requested model, confirm.
  If the model is not in the listed set, return a `failed` final with a
  clear reason (no silent fallback to `--model`).
- For reasoning: confirm no TUI affordance exists; if `req.Reasoning` is
  set and non-default, return a `failed` final with reason "claude-tui
  does not support per-turn reasoning selection". Routing layer treats
  this as a capability rejection.

**Tests**:
- Discovery snapshot replay test asserts a non-empty model list with
  source metadata.
- Apply-model replay test asserts the request's `Model` is reflected in
  request metadata and that the rendered model name matches.
- Reasoning rejection test asserts the non-default request is rejected.

**Acceptance**: `ListModels`, `SetModel` evidenced. `ListReasoning`
documented as empty; `SetReasoning` row remains `gap, no TUI affordance
known`.

### Step 6 — Permission modes

**Goal**: Support `safe` and `supervised` permission modes. Document
`unrestricted` as unsupported until a TUI affordance ships.

**Work**:
- For `safe`: no per-tool elevation; tool calls that would require
  elevation are declined inside the TUI session by Claude's default
  behavior. Runner records the requested mode in metadata.
- For `supervised`: pass through interactive tool prompts; runner records
  the requested mode and a metadata flag that prompts were honored
  interactively. (This is the TUI default and requires no setup work
  beyond not enabling bypass.)
- For `unrestricted`: return a `failed` final with reason "claude-tui does
  not support unrestricted permissions". Routing layer treats this as a
  capability rejection.

**Tests**: Replay scenarios for safe and supervised happy paths;
unrestricted rejection test.

**Acceptance**: `PermissionModes` row populated with `safe`, `supervised`;
`unrestricted` documented as known-blocker `gap`.

### Step 7 — Token usage extraction

**Goal**: Emit `FinalUsage` (input/output/total tokens) on every successful
final event.

**Work**:
- `stream.go`: locate the TUI's per-turn usage rendering (commonly `/cost`
  output or an inline usage footer) and parse `input_tokens`,
  `output_tokens`, and any cache/reasoning subfields the TUI exposes.
  Derive `total_tokens` when not directly rendered.
- Use the existing `ResolveFinalUsage` aggregator for source-precedence
  resolution.

**Tests**: Replay scenarios assert usage on the final event for short and
long turns.

**Acceptance**: `TokenUsage` row evidenced.

### Step 8 — Quota integration

**Goal**: Both `claude` and `claude-tui` read the same durable
`ClaudeQuotaSnapshot`. No parallel cache, no redundant probe.

**Work**:
- `claude-tui.Runner` consumes `ReadClaudeQuotaRoutingDecision` exactly
  like `claude` does.
- Service quota refresh continues to drive `/usage` via the existing
  `internal/harnesses/claude/quota_pty.go` path. The shared
  `ClaudeQuotaSnapshot` is keyed by the Claude account, not by the
  Fizeau-side harness identity.
- Document the single-account assumption in the routing snapshot's
  `claude_quota_decision` shape (no code change to the shape; doc only).

**Tests**: Routing test confirms `claude-tui` selection consults the same
quota decision as `claude`.

**Acceptance**: `QuotaStatus` row evidenced as "shared with `claude`
durable snapshot" with the assumption documented.

### Step 9 — Benchmark lane

**Goal**: Measure `claude-tui` cost relative to `claude` so promotion to
auto-routing has data behind it.

**Work**:
- Add a `claude-tui` profile under `scripts/benchmark/profiles/`.
- Run a long-turn scenario (multi-tool, 10+ tool calls, ≥30s wall clock)
  through both `claude` and `claude-tui`. Capture per-turn latency, CPU,
  memory, and output-byte-rate.
- Record results under `docs/helix/02-design/benchmark-baseline-claude-tui-<date>.md`.

**Acceptance**: A baseline benchmark document exists. Auto-routing
promotion may reference it.

### Step 10 — Auto-routing promotion (separate follow-up spec)

**Not part of this plan.** When all the above steps have landed and each
baseline row is evidenced with a live cassette, a follow-up spec amends
`primary-harness-capability-baseline.md` to flip `AutoRoutingEligible=true`
for `claude-tui` (or to keep it `false` and explain why). That spec must
cite the cassette IDs, the benchmark baseline, and the routing change set.

## Test Strategy

| Layer | Coverage |
|-------|----------|
| `internal/harnesses/anthropic/` | Helper unit tests (quota-message classifier, model normalization). |
| `internal/harnesses/claude-tui/` unit tests | `Info()`, `HealthCheck()`, request-metadata stamping, error classification, env-allowlist enforcement, bracketed-paste byte construction, key-event pacing. |
| `internal/harnesses/claude-tui/` replay tests | Each capability row (Run, FinalText, Progress, Cancel, WorkdirContext, ListModels, SetModel, TokenUsage, ErrorStatus, RequestMetadata) has at least one replay cassette assertion under `internal/ptytest`. Reasoning and unrestricted-permissions rejection have explicit negative tests. |
| Live record mode | Opt-in. Runs the real `claude` binary as `claude-tui` driver. Subject to the same lock/serialization and freshness rules as `claude` and `codex` record-mode tests. |
| Service-level dispatch tests | Confirm `Harness: "claude-tui"` requests reach the new runner; capability matrix and `ListHarnesses` surface the new identity. |
| Routing tests | Confirm `claude-tui` shares the same quota decision as `claude`; confirm `AutoRoutingEligible=false` excludes it from auto-routing until promotion. |
| Benchmark | One long-turn lane comparing `claude-tui` vs. `claude` cost. |

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| TUI surface changes break parser between Claude CLI releases | Cassette manifests pin binary version; replay tests pin `manifest.id`/`content_digest`; broken parsing surfaces as a `gap`, not a silent failure. |
| Shared helpers re-couple `claude` and `claude-tui` | A lint check (or a `depguard`-style rule) blocks `claude-tui` from importing `claude` and vice versa; `internal/harnesses/anthropic` is the only allowed seam. |
| Bracketed paste produces a visibly different byte sequence than a real terminal paste | The exact byte sequence is recorded in cassette `input.jsonl`; replay tests assert it; differences can be tuned without ADR amendment. |
| Frame parsing is slow on long turns | Step 9 benchmark surfaces the regression before promotion; the routing layer may keep `claude-tui` opt-in if the cost is unacceptable. |
| Single-account quota assumption fails | Documented in the routing snapshot and ADR-013; multi-account support is a follow-up spec, not a hidden corner case. |

## Acceptance Criteria

1. `internal/harnesses/anthropic/` exists; both `claude` and `claude-tui`
   consume it; neither imports the other. Verified by a build-time/lint
   check.
2. `internal/harnesses/claude-tui/` implements `harnesses.Harness` and is
   registered in `builtinHarnesses` with the ADR-013 invariants (empty
   `BaseArgs`/`PermissionArgs`/`*Flag`).
3. `claude-tui.Runner.Execute` is implemented exclusively over
   `internal/pty/session`. No `os/exec` invocation of `claude` exists in
   its hot path.
4. Replay cassettes evidence every baseline row that has a TUI affordance.
   `SetReasoning` and `unrestricted` permissions remain `gap` with the
   documented blocker.
5. `claude` and `claude-tui` share the same `ClaudeQuotaSnapshot` cache
   under the single-account assumption.
6. The benchmark suite includes a `claude-tui` lane with at least one
   long-turn comparison run vs. `claude`.
7. `claude-tui` remains `AutoRoutingEligible=false` at the end of this
   plan. Promotion is a separate spec change.
8. `go test ./...` passes. No new test reuses `claude` cassettes for
   `claude-tui` evidence.

## Open Questions

- **Does Claude TUI expose a stable `/model` slash command in the
  currently supported CLI versions?** Step 5 assumes yes. If the
  affordance is arrow-key navigation in a settings dialog, the parser
  becomes more fragile and the implementation cost rises. Resolution:
  smoke-test against the installed `claude` binary as Step 5's first
  task; if no affordance exists, `ListModels`/`SetModel` rows remain
  `gap` and the row description names the blocker.
- **What is the TUI's per-turn usage rendering?** Step 7 assumes
  `/cost` or an inline footer. If the TUI does not render per-turn
  usage at all in the current CLI version, `TokenUsage` becomes a
  `gap` and the routing layer treats `claude-tui` runs as
  zero-usage-known (a regression vs. `claude`). Resolution: include
  this measurement in the Step 5 smoke test.
- **Cancel key**: Esc vs. Ctrl-C semantics in the current Claude TUI
  build. Resolved by smoke test in Step 3.

These questions are deliberately left to implementation discovery; the
ADR-013 capability matrix already absorbs negative answers cleanly as
`gap` rows.
