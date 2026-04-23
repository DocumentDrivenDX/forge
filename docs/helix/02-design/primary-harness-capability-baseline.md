# Primary Harness Capability Baseline

## Problem

The existing harness capability matrix is too broad and too permissive. It mixes
subprocess harnesses, the native `agent` harness, HTTP provider backends, and
test-only replay harnesses in one table. It also marks core primary-harness
behavior as `optional`, which hides critical gaps in `agent`, `codex`, and
`claude`.

This spec defines the confident baseline for the primary harnesses only:

- `agent`
- `codex`
- `claude`
- `gemini`

Secondary harness parity (`opencode`, `pi`) and provider-backend taxonomy
cleanup are separate follow-up work. They must not block or obscure the primary
baseline.

## Scope

This spec covers capability reporting and evidence requirements for primary
harness health. ADR-002 selects direct PTY as the service/cassette transport
for TUI-derived Claude and Codex evidence. If a capability is naturally exposed
through a harness TUI, the capability remains required; tmux evidence is out of
scope for the baseline and does not count toward a pass.

## Status Model

The primary baseline reports current implementation state with these statuses:

- `pass`: implemented and backed by required evidence.
- `gap`: required, but implementation or evidence is missing.
- `blocked`: required, but blocked by an external prerequisite such as auth,
  installed binary, quota, or unavailable local dependency.
- `n/a`: not applicable for this harness.

The primary baseline does not use `optional` for core behavior. Optional support
can exist in a broader matrix, but this primary baseline is deliberately strict.

## Required Baseline

| Harness | Run | FinalText | ProgressEvents | Cancel | WorkdirContext | PermissionModes | ListModels | SetModel | ListReasoning | SetReasoning | TokenUsage | QuotaStatus | ErrorStatus | RequestMetadata |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| agent | pass | pass | pass | pass | pass | `safe`, `unrestricted` | pass | pass | pass | pass | pass | n/a | pass | pass |
| codex | pass | pass | pass | pass | pass | `safe`, `supervised`, `unrestricted` | pass | pass | pass | pass | pass | pass | pass | pass |
| claude | pass | pass | pass | pass | pass | `safe`, `supervised`, `unrestricted` | pass | pass | pass | pass | pass | pass | pass | pass |
| gemini | pass | pass | pass | pass | pass | `safe`, `supervised`, `unrestricted` | pass | pass | n/a | n/a | pass | pass, auth-gated | pass | pass |

## Native Agent Evidence

The native `agent` harness is eligible for automatic routing because its core
capabilities are covered through `Service.Execute` with provider test doubles.
Current evidence:

| Capability | Evidence |
|---|---|
| Run, FinalText, TokenUsage, RequestMetadata | `service_execute_test.go:TestExecute_NativePathWithFakeProvider` asserts success, normalized final text, input/output/total usage, and `routing_actual` harness/provider/model metadata. |
| ProgressEvents, WorkdirContext, RequestMetadata | `service_execute_test.go:TestExecute_NativeReadToolEmitsToolEvents` asserts routing plus tool-call/tool-result progress events, metadata propagation, and reading a sentinel file from `WorkDir`. |
| Cancel and timeout status | `service_execute_test.go:TestExecute_OSCancelDuringStreaming` and `service_execute_test.go:TestExecute_TimeoutWallClock` assert cancelled/timed-out/failed terminal statuses instead of success. |
| PermissionModes | `service_execute_test.go:TestExecute_NativeSafePermissionExposesReadOnlyTools`, `TestExecute_NativeUnrestrictedToolsForwarded`, and `TestExecute_NativeSupervisedPermissionRejected` assert the documented `safe`/`unrestricted` modes and explicit `supervised` rejection. |
| SetReasoning | `service_execute_test.go:TestExecute_NativeReasoningForwarded` asserts requested reasoning is forwarded to the native provider path. |
| ListModels and SetModel | `service_models_test.go` covers native provider model listing and harness-filtered model selection; `service_route_attempts_test.go` and `service_routestatus_test.go` cover resolved provider/model decisions through `ResolveRoute`. |
| QuotaStatus | n/a for the native harness; quota belongs to the selected provider backend. |

## Automatic Routing Eligibility

Only harnesses whose current baseline evidence is complete may set
`AutoRoutingEligible=true`. For subscription-backed primary harnesses, complete
evidence includes a fresh durable quota/account decision at routing time; Gemini
uses fresh auth/account evidence until the CLI exposes a stable numeric quota
counter. Missing or stale quota cache, blocked quota windows, auth failures, or
failed probes must make that harness ineligible and explain why. For
non-subscription harnesses, full runner coverage still does not imply automatic
routing unless cost and quota policy are concrete enough to compete with
subsidized primary capacity.

Codex, Claude, and Gemini are the primary subscription smart-routing drivers.
When Codex or Claude is blocked by quota extraction, the required response is to
fix the PTY probe/cache and keep routing honest. Gemini is eligible only through
fresh auth/account evidence until it exposes a stable quota counter; it must not
mask missing Codex/Claude quota evidence as a silent fallback.

Quota refresh ownership belongs to the service, not the operator. Foreground
routing remains cache-only and must not synchronously run live PTY probes on
every request. Instead, service startup checks primary quota cache state and, if
the cache is missing, stale, or incomplete, starts a refresh and waits only for a
short bounded startup window before continuing with the best cache information
available. Normal status/routing activity starts a debounced asynchronous refresh
when the cache is older than the operating margin, currently fifteen minutes.
Long-running DDx server processes should set the service quota refresh interval
so the same debounced refresh path runs from a timer.

Current status:

| Harness | Auto-routing status | Reason |
|---|---|---|
| agent | eligible | Native service evidence covers the baseline rows above. |
| codex | eligible, conditional on fresh subsidized account/quota evidence | Codex runner tests cover request controls, final text, progress, usage, and cancellation/error behavior; PTY cassette tests cover model/reasoning discovery and quota evidence; `internal/harnesses/codex/account_test.go` and quota-cache tests prove account metadata is extracted from Codex auth state and API-key-only or missing account evidence is not enough for auto-routing; `service_route_attempts_test.go:TestResolveRoute_CodexUsesDurableQuotaCache` proves automatic routing consumes fresh durable quota state. |
| claude | eligible, conditional on fresh complete account/quota evidence | Claude runner tests cover request controls, final text, progress, usage, and cancellation/error behavior; PTY cassette tests cover model/reasoning discovery and quota evidence; quota-cache and PTY tests now reject incomplete account, source, session, or weekly-window evidence; foreground routing consumes the durable Claude quota decision before automatic selection. |
| gemini | eligible, conditional on fresh auth/account evidence | Gemini runner tests cover request controls, final text, progress, usage, cancellation/error behavior, and model discovery fixtures. Gemini CLI does not expose a stable non-interactive numeric quota counter, so automatic routing is gated on fresh non-secret auth/account evidence and service status reports `ok`, `stale`, or `unauthenticated` for that gate while leaving quota windows empty. |

## Capability Contracts

### Run

The harness accepts a prompt and returns a terminal final event.

Evidence:

- unit or integration test for `Service.Execute`
- authenticated live evidence for `codex`, `claude`, and `gemini`

### FinalText

Successful execution returns non-empty normalized final text after trimming
harness scaffolding, status banners, and transport markers. A successful process
exit with empty final text or an error payload is not a pass.

Evidence:

- parser/unit tests for final text extraction
- service-level test asserting `final_text`
- live cassette final event for `codex`, `claude`, and `gemini`

### ProgressEvents

The harness emits normalized progress before the final event. At minimum, the
service stream includes `routing_decision` and at least one non-final execution
progress event for non-trivial runs.

Very short prompt smoke tests may complete with only routing and final events,
but they do not satisfy this capability by themselves. Progress evidence must
use a fixture or prompt that produces observable intermediate output.

Evidence:

- service-event replay or live cassette with a non-final progress event

### Cancel

Execution honors context cancellation. A cancelled or timed-out run terminates
the harness process tree and emits a normalized final status within a bounded
interval.

Evidence:

- test that cancels an in-flight primary harness execution and observes
  `cancelled` or `timed_out`
- subprocess harness tests must verify child cleanup behavior at the runner or
  service boundary

### WorkdirContext

Execution happens in the requested working directory or repository context.

Acceptable evidence:

- visible adapter flag such as Codex `-C <dir>`
- subprocess `cmd.Dir` plus behavioral proof
- behavioral proof such as reading a sentinel file from the requested directory

Codex trusted-directory requirements are part of this capability. Test workdirs
for Codex must be prepared as git repositories or the run must be reported as a
gap/blocked state, not silently worked around without evidence.

### PermissionModes

The matrix reports the exact supported mode set per primary harness.

Required mode sets:

- `agent`: `safe`, `unrestricted`
- `codex`: `safe`, `supervised`, `unrestricted`
- `claude`: `safe`, `supervised`, `unrestricted`

Unsupported modes must be reported explicitly. The native `agent` harness does
not pass a three-mode check by hiding `supervised`; it passes only if the
reported mode set is exactly documented and enforced.

Evidence:

- adapter argument tests for subprocess harnesses
- native tool-set enforcement tests for `agent`

### ListModels

Model listing is required for every primary harness. A harness that can set a
model but cannot list usable model choices is not baseline-complete.

Acceptable model-list sources:

- `provider`: native provider model discovery or configured provider catalog
- `catalog`: service-owned model catalog with surface-specific entries
- `registry`: curated harness registry list with version/freshness metadata
- `tui`: authenticated harness TUI output parsed through an interim or final
  terminal transport

Codex and Claude expose model choices through their interactive TUI surfaces.
Gemini model choices are covered by the service-owned catalog and Gemini CLI
surface fixtures. That makes model listing required. If the implementation does
not yet provide an accepted source for a primary harness, `ListModels` is `gap`
or `blocked`, not optional.

Evidence:

- test that `ListModels(... Harness: "agent")` returns configured/provider
  models
- test that `ListModels(... Harness: "codex")` returns a non-empty model list
  with source metadata
- test that `ListModels(... Harness: "claude")` returns a non-empty model list
  with source metadata
- live TUI evidence or accepted catalog/registry evidence for each listed
  Codex/Claude model source

### SetModel

The harness accepts one listed model and passes it to the underlying
runner/provider.

Evidence must include:

- requested model
- observed actual model when the harness exposes it, or exact adapter
  flag/config passed when it does not

Examples:

- Codex: `-m <model>`
- Claude: `--model <model>`
- agent: provider request model or resolved provider/profile model metadata

### ListReasoning

Reasoning-level listing is required for every primary harness. A harness that
can accept an effort/reasoning setting but cannot say which values are usable is
not baseline-complete.

Acceptable reasoning-list sources:

- provider/model capability metadata for `agent`
- harness registry or catalog data when the CLI values are stable
- authenticated TUI or CLI evidence when the available levels are surfaced there

Evidence:

- non-empty list for each primary harness
- source metadata
- tests proving unsupported reasoning levels are rejected or reported

Claude reasoning/effort support must be owned by an explicit compatibility
decision. If the installed Claude CLI exposes stable `--effort` values, the
adapter may use that source. Otherwise the baseline requires a version-pinned
compatibility table with freshness metadata and tests that stale mappings leave
the capability as `gap` or `blocked`, not silently supported.

### SetReasoning

The harness accepts one listed reasoning level and applies the correct
runner/provider control.

Evidence must include:

- requested reasoning level
- observed actual reasoning when exposed, or exact adapter flag/config passed
  when it is not

Examples:

- Codex: `-c reasoning.effort=<level>`
- Claude: `--effort <level>` if supported by the installed CLI, or a
  version-pinned compatibility table accepted by the reasoning-listing
  evidence; otherwise this capability remains `gap`
- agent: resolved provider request metadata or internal reasoning-budget
  metadata

### TokenUsage

Every successful primary-harness execution emits normalized token utilization on
the final event:

- `input_tokens`
- `output_tokens`
- `total_tokens`

If the harness reports only input and output, `total_tokens` is derived as
`input_tokens + output_tokens`. Missing token fields are a failure for the
primary baseline, not a zero value.

For Claude, Codex, and Gemini, token-usage evidence must come from the harness
native stream, transcript, status output, or another documented source of truth
and be captured through the direct PTY/cassette path when the source is
TUI-derived.
Cache, reasoning, or provider-specific token subfields should be preserved when
exposed, but the required baseline remains input, output, and total tokens.

When more than one source is available, the default precedence is native stream,
then transcript/session artifact, then status output, then an explicitly
documented fallback. If sources disagree, the normalized final metadata must
record a warning with the sources and values instead of silently picking one.

Cost is useful when exposed, but `cost_usd` is not part of this baseline.

### QuotaStatus

Quota status is required for subscription harnesses:

- `codex`
- `claude`
- `gemini`

Quota is `n/a` for the native `agent` harness because quota belongs to the
selected provider backend, not to the harness itself.

Minimum quota states:

- `ok`
- `blocked`
- `unknown`
- `stale`

`low` may be added once thresholds are defined. Until then, remaining capacity
that is not blocked is `ok`; stale or unavailable probes are not `ok`.

Quota evidence must include:

- source (`cache`, `tui`, `cli`, or `api`)
- captured timestamp
- freshness TTL
- parsed state

Codex and Claude numeric quota windows must be proven through direct PTY
evidence for final support. Legacy tmux-backed helpers are not part of the
baseline and do not count as accepted evidence.
The supported implementation drives Claude `/usage` and Codex `/status` through
direct PTYs, normalizes missing binary/auth/timeout failures, and validates
sanitized quota cassettes through the replay layer.

Gemini quota status is currently an auth/account gate, not a numeric remaining
quota counter: fresh authenticated Gemini evidence is `ok`, stale evidence is
`stale`, and missing/unusable evidence is `unauthenticated`. Per-run rate-limit
or quota failures remain execution errors until Gemini CLI exposes a stable
non-interactive quota surface.

### ErrorStatus

The harness normalizes failure outcomes. Bad configuration, bad model,
authentication failure, process failure, timeout, and cancellation must not be
reported as success.

Minimum statuses:

- `success`
- `failed`
- `timed_out`
- `cancelled`

Evidence:

- service-level tests for failure and timeout paths
- parser tests that reject error envelopes as success

### RequestMetadata

Every primary-harness final/result event must carry enough normalized metadata
to prove that list/set and execution controls were applied.

Required metadata fields:

- requested model
- applied model or adapter flag/config
- requested reasoning level
- applied reasoning or adapter flag/config
- workdir policy or workdir evidence
- permission mode
- model-list source when `SetModel` uses a listed model
- reasoning-list source when `SetReasoning` uses a listed value

The exact JSON shape may evolve, but the baseline report must be able to point
to these fields as evidence.

## Reporting Rules

The service must expose a primary-harness baseline report that is separate from
the broad harness/provider capability matrix. The report must:

- include only `agent`, `codex`, `claude`, and `gemini`
- exclude HTTP provider backends such as `openrouter`, `lmstudio`, and `omlx`
- avoid `optional` for baseline capabilities
- show `gap` or `blocked` for missing primary requirements
- include evidence pointers or blocker summaries for each non-pass cell

The broader compatibility matrix may continue to exist during migration, but it
must not be used as the authoritative primary-harness health signal.

TUI-only capability rows are feature complete only when the corresponding
record/playback checklist in
[Harness Golden-Master Integration](/Users/erik/Projects/agent/docs/helix/02-design/harness-golden-integration.md)
passes in both live record mode and credential-free playback mode.

## Acceptance Criteria

1. A primary-harness baseline report exists and renders as a compact table for
   `agent`, `codex`, `claude`, and `gemini`.
2. The report includes all baseline capabilities in this spec.
3. `ListModels` is required for all primary harnesses and cannot be reported as
   optional.
4. Codex and Claude model listing gaps are visible until a TUI/catalog/registry
   implementation provides evidence-backed model choices.
5. Reasoning listing and setting are both reported independently.
6. Token usage is required and missing token data is reported as a gap.
7. Quota status is required for Codex, Claude, and Gemini and includes source,
   freshness, and state. Gemini may omit numeric windows while the CLI has no
   stable non-interactive quota counter.
8. HTTP provider backends are not displayed as primary harnesses.
9. Tests fail if a primary baseline capability is silently downgraded to
   optional or omitted.

## Non-Goals

- Implementing the final PTY library in this baseline report; ADR-002 owns that
  transport design and its conformance requirements.
- Bringing remaining secondary harnesses to parity.
- Adding deterministic record/replay to production harnesses.
- Making `CostUSD` a baseline requirement.
- Proving tool-call/tool-result event parity; that is important phase-two work
  after the primary baseline is honest.
