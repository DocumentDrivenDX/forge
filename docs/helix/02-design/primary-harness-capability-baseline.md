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

Secondary harness parity (`gemini`, `opencode`, `pi`) and provider-backend
taxonomy cleanup are separate follow-up work. They must not block or obscure the
primary baseline.

## Scope

This spec covers capability reporting and evidence requirements for primary
harness health. ADR-002 selects direct PTY as the service/cassette transport
for TUI-derived Claude and Codex evidence. If a capability is naturally exposed
through a harness TUI, the capability remains required; tmux-only evidence is a
gap until replaced by direct PTY and cassette evidence.

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
| agent | pass/gap | pass/gap | pass/gap | pass/gap | pass/gap | `safe`, `unrestricted` | pass/gap | pass/gap | pass/gap | pass/gap | pass/gap | n/a | pass/gap | pass/gap |
| codex | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | `safe`, `supervised`, `unrestricted` | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked |
| claude | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | `safe`, `supervised`, `unrestricted` | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked | pass/gap/blocked |

## Capability Contracts

### Run

The harness accepts a prompt and returns a terminal final event.

Evidence:

- unit or integration test for `Service.Execute`
- authenticated live evidence for `codex` and `claude`

### FinalText

Successful execution returns non-empty normalized final text after trimming
harness scaffolding, status banners, and transport markers. A successful process
exit with empty final text or an error payload is not a pass.

Evidence:

- parser/unit tests for final text extraction
- service-level test asserting `final_text`
- live cassette final event for `codex` and `claude`

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
That makes model listing required. If the implementation does not yet provide a
headless way to collect and parse those choices, `ListModels` is `gap` or
`blocked`, not optional.

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

For Claude and Codex, token-usage evidence must come from the harness native
stream, transcript, status output, or another documented source of truth and be
captured through the direct PTY/cassette path when the source is TUI-derived.
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

Codex and Claude quota can currently be probed through legacy tmux-backed TUI
helpers, but ADR-002 requires direct PTY evidence for final support. The
capability requirement does not change: tmux-only quota evidence is a visible
gap until replaced by direct PTY capture.

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

- include only `agent`, `codex`, and `claude`
- exclude HTTP provider backends such as `openrouter`, `lmstudio`, and `omlx`
- avoid `optional` for baseline capabilities
- show `gap` or `blocked` for missing primary requirements
- include evidence pointers or blocker summaries for each non-pass cell

The broader compatibility matrix may continue to exist during migration, but it
must not be used as the authoritative primary-harness health signal.

## Acceptance Criteria

1. A primary-harness baseline report exists and renders as a compact table for
   `agent`, `codex`, and `claude`.
2. The report includes all baseline capabilities in this spec.
3. `ListModels` is required for all primary harnesses and cannot be reported as
   optional.
4. Codex and Claude model listing gaps are visible until a TUI/catalog/registry
   implementation provides evidence-backed model choices.
5. Reasoning listing and setting are both reported independently.
6. Token usage is required and missing token data is reported as a gap.
7. Quota status is required for Codex and Claude and includes source,
   freshness, and state.
8. HTTP provider backends are not displayed as primary harnesses.
9. Tests fail if a primary baseline capability is silently downgraded to
   optional or omitted.

## Non-Goals

- Implementing the final PTY library in this baseline report; ADR-002 owns that
  transport design and its conformance requirements.
- Bringing secondary harnesses to parity.
- Adding deterministic record/replay to production harnesses.
- Making `CostUSD` a baseline requirement.
- Proving tool-call/tool-result event parity; that is important phase-two work
  after the primary baseline is honest.
