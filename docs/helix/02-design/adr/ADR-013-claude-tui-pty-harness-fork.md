---
ddx:
  id: ADR-013
  status: re-proposed
  depends_on:
    - ADR-002
    - ADR-004
    - ADR-011
    - ADR-014
    - CONTRACT-004
  child_of: fizeau-67f2d585
---
# ADR-013: `claude-tui` PTY Harness as a Fork of `claude`

| Date | Status | Deciders | Related | Confidence |
|------|--------|----------|---------|------------|
| 2026-05-14 | Withdrawn pending CONTRACT-004 | Fizeau maintainers | ADR-002, ADR-004, ADR-011, ADR-014, CONTRACT-004 | Medium |
| 2026-05-17 | **Re-proposed** — CONTRACT-004 merged; awaiting empirical billing-observation evidence for acceptance | Fizeau maintainers | same | Medium |

> **Re-proposal note (2026-05-17):** CONTRACT-004 and the harness
> interface refactor (ADR-014) are merged — see AR-2026-05-17-repo
> amendment for the verification trail (scheduler at
> `internal/routehealth/refresh_scheduler.go`, zero per-harness imports
> in production service code, lint rule at
> `internal/lint/harnessimports/analyzer.go`).
>
> This ADR is re-proposed against the merged contract. The design
> constraints in the withdrawal note below remain authoritative —
> they are constraints, not interface contract, and CONTRACT-004 did
> not alter them. The implementation chain is rehydrated at
> [`plan-2026-05-14-claude-tui-fork.md`](../plan-2026-05-14-claude-tui-fork.md).
>
> **Promotion gate**: this ADR moves from `re-proposed` to `accepted`
> when the empirical subscription-billing observation (constraint #8
> below) is captured against an authenticated Anthropic account and
> recorded in `docs/helix/02-design/`. Filing this work as a bead is
> the first sub-task in the rehydrated plan.

> **Historical status note (2026-05-14):** This ADR was withdrawn pending the
> universal harness interface refactor in
> [ADR-014](./ADR-014-universal-harness-interface.md) and
> [CONTRACT-004](../contracts/CONTRACT-004-harness-implementation.md).
> A 2026-05-14 inventory found 69 service-side call sites reaching into
> per-harness exports beyond the documented `Harness` interface;
> introducing `claude-tui` against that surface would either duplicate
> ~25 exports under a new prefix or wire service code through a fifth
> set of per-harness imports. Both outcomes are the leak pattern
> CONTRACT-004 exists to eliminate.
>
> The fork remains the right shape for accessing Anthropic subscription
> pricing through the TUI. Re-proposal happens after CONTRACT-004
> merges, at which point the implementation reduces to: a new
> `internal/harnesses/claude-tui/` package implementing
> `Harness` + `QuotaHarness` + `AccountHarness` +
> `ModelDiscoveryHarness`, sharing a snapshot type with `claude`
> through an `internal/harnesses/anthropic/` neutral subpackage. No
> service-side changes required.
>
> The companion implementation plan
> [`plan-2026-05-14-claude-tui-fork.md`](../plan-2026-05-14-claude-tui-fork.md)
> is superseded by
> [`plan-2026-05-14-harness-interface-refactor.md`](../plan-2026-05-14-harness-interface-refactor.md)
> for the prerequisite refactor work. A new claude-tui plan is written
> after re-proposal.
>
> The capability-baseline row added by this ADR
> (`claude-tui` in `primary-harness-capability-baseline.md`) and the
> recorder reference in `harness-golden-integration.md` are removed
> alongside this status flip.
>
> ---
>
> ### Prior art surveyed (2026-05-14)
>
> Two reference implementations exist for driving Claude Code outside
> the `claude --print` batch path. Future re-proposal does not have to
> redo this survey.
>
> | Project | Transport | Session lifetime | Notes |
> |---------|-----------|------------------|-------|
> | [smithersai/claude-p](https://github.com/smithersai/claude-p) | in-process PTY (zmux) + `--settings '<inline-json>'` to register `SessionStart`/`Stop` hooks | per-invocation, one-shot | A small ANSI scanner answers Ink's DA1/DA2/DSR/XTVERSION/window-size startup probes — no full terminal emulator. Final text + usage extracted by reading the JSONL transcript whose path is delivered in the `Stop` hook payload. README explicitly notes "client-side restrictions ... are fundamentally unenforceable"; no claim about subscription billing. |
> | [dexhorthy/shannon](https://github.com/dexhorthy/shannon) | tmux session + `tmux send-keys` | persistent across turns | Reads the same JSONL transcript by tailing `~/.claude/projects/`. Rejected as a transport choice for Fizeau per ADR-002 (tmux is not part of the core path), but informative as convergent evidence for the parsing seam. |
>
> The convergent insight from both projects: **the parsing seam is the
> on-disk JSONL transcript at `~/.claude/projects/<workdir>/<id>.jsonl`,
> not rendered TUI output, regardless of transport choice**.
> `internal/pty/terminal` frame derivation and screen pattern-matching
> are not required for normal prompt execution under either reference
> design; they remain required only for the `/usage` quota probe.
>
> ### Design direction for re-proposal
>
> The following decisions are recorded here so the future ADR-013
> re-proposer can adopt them without re-deriving the rationale. They
> are constraints, not interface contract — CONTRACT-004 and ADR-014
> remain unaffected and transport-agnostic.
>
> 1. **Transport**: in-process PTY via the existing `internal/pty/`
>    library, with hooks registered via `--settings '<inline-json>'`
>    (Anthropic-published extension point). The `--settings` flag is
>    explicitly distinguished from the previously-forbidden batch flags
>    (`--print`, `-p`, `--output-format`, `--stream-json`, `--effort`,
>    `--model`, `--permission-mode`, `--dangerously-skip-permissions`)
>    because it configures end-user-facing behavior the way a user's
>    `~/.claude/settings.json` would, not an automation/batch mode. The
>    batch-flag prohibition stands; the `--settings` carve-out is
>    additive.
>
> 2. **Output parsing**: read the JSONL transcript whose path is
>    delivered in the `Stop` hook payload. Do not parse rendered TUI
>    output. `internal/pty/terminal` (vt10x) is not required for
>    Execute; the PTY layer is reduced to "enough to keep Ink happy at
>    startup" — a small responder for DA1 / DA2 / DSR / XTVERSION /
>    window-size probes. A reusable startup-probe responder belongs in
>    `internal/pty/` so the quota probe path can consume it too.
>
> 3. **Streaming progress events**: use `PreToolUse` / `PostToolUse`
>    hooks (or whichever Claude Code hooks are documented for tool-call
>    boundaries at re-proposal time) to emit `tool_call` / `tool_result`
>    events during the turn. claude-p is batch (only `Stop`); Fizeau's
>    CONTRACT-004 requires intermediate `ProgressEvents` so this hook
>    set is load-bearing.
>
> 4. **Session lifetime**: **pooled long-lived sessions with `/clear`
>    between turns, lifetime bounded by the fiz process**. Rationale:
>    Ink + auth startup is the expensive part (~50–200 ms per claude-p's
>    measurements); amortizing it across the many Execute calls within
>    a single fiz invocation is worth the pool-management cost.
>    `/clear` resets conversation state without dropping the warm
>    session. The pool dies when fiz dies — no PID files, no daemon, no
>    cross-invocation persistence.
>
>    Pool key default: **per `(harness, workdir)`**. Claude sessions
>    are bound to a working directory at startup; switching workdirs in
>    an existing session is not supported by `claude`. Per-(harness)
>    only would force serialization across all workdirs;
>    per-(harness, workdir, model) is overkill — model selection is
>    cheap enough to run via `/model` post-`/clear`.
>
>    Pool depth default: **1 per key**. Adequate for serial agent
>    loops, which is the standalone CLI's usage. Service-mode
>    concurrency can raise this with no contract change.
>
>    Pool placement: the pool lives at package scope (a singleton in
>    `internal/harnesses/claude-tui/`) or at service scope (a
>    constructor-injected dependency on the Runner), **not as a field
>    on the Runner struct**. Two `&claudetui.Runner{}` instances must
>    share the pool, otherwise the dispatcher's "construct a fresh
>    Runner per Execute" pattern defeats the amortization. CONTRACT-004
>    invariant #6 forbids mutable quota/account state on the Runner
>    but does not forbid shared transport state behind a singleton;
>    this is the right escape valve.
>
> 5. **Empirical `/clear` semantics gate** (pre-implementation): verify
>    against the installed Claude Code that `/clear`:
>    - resets conversation history (the point of the command);
>    - does NOT reset model selection (otherwise per-turn `/model` is
>      required, lengthening the per-turn ritual);
>    - does NOT reset permission mode;
>    - does NOT close the auth/session token;
>    - starts a new transcript file at a path observable from the
>      next turn's `Stop` hook payload.
>
>    If any of those don't hold, the per-turn ritual lengthens but
>    the pool model is still worthwhile. If `/clear` doesn't exist or
>    is unstable in the installed version, fall back to per-Execute
>    sessions (claude-p model) and accept the cold-start cost.
>
> 6. **Orphan reaper**: fiz crashes leave pooled `claude` processes
>    orphaned. A startup reaper analogous to the existing
>    `service_stale_harness_reaper*.go` kills `claude` processes whose
>    parent fiz PID is gone, before the new fiz instance constructs
>    its pool. No persistent state across fiz invocations — the
>    reaper inspects live process state only.
>
> 7. **Hook conflict handling**: a user's existing
>    `~/.claude/settings.json` may declare its own `SessionStart` /
>    `Stop` / `PreToolUse` / `PostToolUse` hooks. The `--settings
>    '<inline-json>'` mechanism's merge semantics (replace vs. layer)
>    are unspecified in claude-p's README and need explicit
>    verification before claude-tui can ship. If hooks are
>    replace-not-merge, Fizeau must compose with whatever the operator
>    already has, not stomp it.
>
> 8. **Subscription billing observation**: still required as a
>    promotion gate per ADR-014. Neither claude-p nor shannon claims
>    subscription billing; both route through the same `claude`
>    binary and inherit whatever billing classification that binary's
>    request paths produce. The re-proposal must include an empirical
>    measurement showing PTY+hooks-driven Claude moves the `/usage`
>    window, otherwise the fork's economic premise is unverified.
>
> ---
>
> The content below remains for historical reference of the original
> proposal.

## Context

The current `claude` harness (`internal/harnesses/claude/runner.go`) drives the
Claude CLI through `claude --print -p --output-format stream-json` (with a
legacy `--output-format json` fallback) via `os/exec`. That subprocess path
covers normal prompt execution today, but it is a different transport from the
direct PTY path Fizeau already uses for `/usage` quota probing
(`internal/harnesses/claude/quota_pty.go` over `internal/harnesses/ptyquota`,
`internal/pty/session`, `internal/pty/terminal`).

**The driver for forking is Anthropic subscription pricing.** Claude Pro/Max
subscription capacity is billed against an account quota that is exposed
through the interactive Claude CLI surface (TUI + `/usage`). The Anthropic
API, including request paths a `claude --print` invocation may resolve
against, is metered separately at per-token API pricing. Routing prompt
execution through the interactive TUI is how Fizeau accesses subsidized
subscription capacity (ADR-011 cost-based routing already assumes this is
possible). The existing `claude --print` transport cannot be relied on to
keep landing on subscription capacity as Anthropic evolves the boundary; the
TUI surface is the durable, documented entry point for subscription quota.

This is not about anti-automation fingerprinting or impersonating a human as
a security goal. It is about using the subscription product the way it is
priced, which means: drive the TUI, do not pass batch-API-shaped flags, and
do not introduce Fizeau-side signals (env vars, argv markers) that change
billing classification or invite policy enforcement against automated batch
use.

ADR-002 already commits Fizeau to direct PTY ownership and bans tmux from the
core path. ADR-004 caps build vs. buy for the PTY library boundary. ADR-011
treats subsidized Claude/Codex quota as a routing-preferred capacity pool.
None of those ADRs specify *how* normal prompt execution should adopt PTY
without breaking the existing `claude` evidence base (cassettes, runner
tests, capability baseline rows, auto-routing eligibility).

## Decision

Fizeau will add a **new, separate primary harness identity `claude-tui`** that
implements the existing `harnesses.Harness` interface
(`internal/harnesses/types.go`, `Info`/`HealthCheck`/`Execute`) and runs the
Claude CLI exclusively through the direct PTY transport. `claude-tui` is a
fork — a sibling package alongside `internal/harnesses/claude/` — not a
mode, flag, or conditional branch in the existing runner.

The existing `claude` harness keeps its current implementation, capability
evidence, and auto-routing eligibility unchanged. Both harnesses coexist
indefinitely; capability and routing decisions are tracked per-identity.

`claude-tui` is the identity through which subscription-billed Claude
capacity is routed once it earns auto-routing eligibility. Until then, the
existing `claude` harness continues to serve routed Claude traffic.

**Key Points**: separate package + registry identity | PTY only, no batch
flags, no Fizeau-side identification env/argv | both `claude` and
`claude-tui` are primary harnesses with independent baseline rows | both
implement the same `harnesses.Harness` interface.

## Harness Interface (Normative)

The new identity implements the existing, unchanged interface defined in
`internal/harnesses/types.go`:

```go
type Harness interface {
    // Info returns identity + capability metadata for this harness.
    Info() HarnessInfo

    // HealthCheck triggers a fresh probe (binary present, auth ok, etc.)
    // and returns nil if the harness is ready to execute.
    HealthCheck(ctx context.Context) error

    // Execute runs one resolved request. Events stream on the returned
    // channel; a single final event closes the stream. The first error
    // return is reserved for setup failures (binary missing, etc.) — once
    // the channel is returned, all per-run failures are reported via a
    // final event with Status != "success".
    Execute(ctx context.Context, req ExecuteRequest) (<-chan Event, error)
}
```

`HarnessInfo`, `ExecuteRequest`, `Event`, `FinalData`, `FinalUsage`,
`RoutingActual`, `ReasoningActual`, and the `EventType` closed union are
defined in the same file and are not extended by this ADR. CONTRACT-003 is
the public-surface contract that those internal shapes reflect; this ADR
adds no new event types, no new request fields, and no new metadata keys.

`claude-tui` therefore differs from `claude` only in:

- transport mechanism (PTY vs. `os/exec` batch),
- registry config (`HarnessConfig`) entry name, binary invocation, and
  baseline flags,
- `HarnessInfo.Name` returned by `Info()`,
- internal package layout and tests,

not in interface, event shapes, or request semantics. A caller routing
through `harnesses.Harness` cannot tell the two apart except by the
`HarnessInfo.Name` it returns.

A separate harness identity (rather than a `Transport` mode on a single
`HarnessConfig`) is required because:

- `HarnessInfo.Name` is the key the capability matrix, routing layer, and
  evidence store use to attribute rows. Collapsing two transports into one
  name makes evidence ambiguous.
- `HarnessConfig` carries `BaseArgs`, `PermissionArgs`, `ModelFlag`,
  `ReasoningFlag`, and `TUIQuotaCommand` fields that have transport-specific
  meaning. A PTY harness uses none of `BaseArgs`/`ModelFlag`/`ReasoningFlag`
  and uses `TUIQuotaCommand`-shaped flows for routine model/reasoning
  selection, not just for quota. Two configs is cheaper than a polymorphic
  one.
- `AutoRoutingEligible` is a per-row decision in the primary baseline. It
  must be expressible independently for the PTY transport, which has no
  live cassette evidence at introduction.

## Scope

### In Scope

| Aspect | Detail |
|--------|--------|
| New package | `internal/harnesses/claude-tui/` (Go package `claudetui`) owns a `Runner` type implementing `harnesses.Harness`, the TUI stream parser, model/reasoning discovery, and Final/Progress event emission. |
| Registry entry | Adds a `claude-tui` entry to `builtinHarnesses` (`internal/harnesses/registry.go`). `BaseArgs` is empty (no batch flags). `PermissionArgs` is empty (permission selection happens via TUI affordances or not at all — see Permissions below). `ModelFlag`/`ReasoningFlag`/`WorkDirFlag` are empty (`""`). `IsSubscription=true`. `AutoRoutingEligible=false` at introduction. `TUIQuotaCommand="/usage"` (shared command, shared cache). |
| Transport | Exclusively `internal/pty/session` + `internal/pty/terminal`. No `os/exec` direct execution and no `claude --print`/`-p`/`--output-format` invocations from this package. Reuses the same direct-PTY library that `internal/harnesses/ptyquota` already drives. |
| Invocation profile | `claude` (no `--print`, no `-p`, no `--output-format`, no `--stream-json`, no other batch/API-shaped flag). Workdir via the `session.Start` workdir argument (equivalent of `cmd.Dir`). Permissions/reasoning/model selected through the same TUI affordances a human uses (slash commands and selection menus) when such an affordance exists. |
| Prompt delivery | The prompt is sent into the TUI input area using the terminal's bracketed-paste sequence (`ESC[200~` … `ESC[201~`) followed by `Enter`. Paste arrives as a single burst inside the bracketed-paste boundaries, matching the byte shape a real paste produces; Fizeau does not insert artificial inter-byte delays inside a paste. Single keystrokes for slash commands and menu navigation are sent one logical key per event with a small fixed inter-key delay (default 25–75ms) to avoid super-human keystroke bursts that could trip TUI input handling. |
| Capability surface | Same baseline rows as `claude`, evidenced independently: `Run`, `FinalText`, `ProgressEvents`, `Cancel`, `WorkdirContext`, `PermissionModes`, `ListModels`, `SetModel`, `ListReasoning`, `SetReasoning`, `TokenUsage`, `QuotaStatus`, `ErrorStatus`, `RequestMetadata`. |
| Cassettes | Record-mode and replay-mode follow ADR-002. Cassette `manifest.harness.name = "claude-tui"`; binary version, command, terminal, timing, and provenance fields are stamped from the PTY session as for `claude` quota cassettes. Live cassette evidence is required for promotion of any baseline row from `gap` to `pass`, identical to the `claude` rule. |
| Quota | `claude-tui` consumes the same durable `ClaudeQuotaSnapshot` cache as `claude`. The quota probe drives `/usage` once via PTY and the parsed snapshot is shared. **Assumption**: both identities authenticate as the same Anthropic account; quota is per-account, not per-transport. If a future operator binds different accounts to different identities, the durable cache key must be extended to include account identity (out of scope for this ADR). |
| Shared helpers | Helpers genuinely needed by both runners — quota-message classification (`IsClaudeQuotaExhaustedMessage`, `MarkClaudeQuotaExhaustedFromMessage`), account-info shapes, model/reasoning resolution helpers — move to a new `internal/harnesses/anthropic/` neutral package before being consumed from both runners. Not `internal/harnesses/claude/shared` (that would make `claude-tui` import from under `claude`). |
| Auto-routing | `claude-tui` is `AutoRoutingEligible=false` at introduction. Promotion is gated on fresh complete account/quota evidence (same conditions as `claude`) **plus** at least one accepted live record-mode cassette per supported capability row. The promotion decision is a separate spec change once evidence lands. |

### Out of Scope

| Aspect | Detail |
|--------|--------|
| Merging implementations | Sharing a runner with conditionals, build tags, or `if usePTY { ... } else { ... }` branching inside the `claude` package is explicitly rejected. The fork pays a one-time duplication cost so neither implementation contorts to fit the other. |
| Retiring `claude` | Not part of this ADR. The subprocess `claude` harness keeps its tests, cassettes, capability rows, and auto-routing eligibility. Any future retirement is a separate ADR with its own promotion/deprecation gates. |
| Multi-account support | Quota cache keying remains per-account-default. Operators with multiple Anthropic accounts continue to fall outside the supported routing model until a follow-up spec extends the cache key. |
| Tmux | Explicitly rejected by ADR-002. Reaffirmed here: `claude-tui` must not depend on tmux, screen, or any external terminal multiplexer. |
| Operator UX changes | No new `fiz` flags or new operator commands. `claude-tui` is selectable through the same routing/identity mechanisms as any other primary harness. |
| `codex`/`gemini` PTY forks | Out of scope. If those harnesses need the same treatment for the same subscription-pricing reason, follow-up ADRs will mirror this structure rather than generalize prematurely. |
| Anti-fingerprinting / human impersonation as a goal | Out of scope. The constraints below exist to keep Fizeau's invocation looking like normal TUI usage so subscription-billing classification is preserved, not to defeat adversarial detection. |

## Invocation Constraints

These constraints derive from the subscription-pricing goal: invocation must
not look like batch-API automation, and Fizeau must not introduce side
channels that could be used to reclassify the request.

`claude-tui` **must not**:

- Pass any flag that signals batch/automation intent or selects a non-TUI
  output format: `--print`, `-p`, `--output-format`, `--stream-json`,
  `--effort`, `--model`, `--permission-mode`,
  `--dangerously-skip-permissions`, or any future Anthropic flag in the same
  family. Where a TUI affordance for the same setting exists (e.g. a `/model`
  selector), use that affordance via PTY input bytes. Where no TUI affordance
  exists, the capability is `gap` for `claude-tui` until one does — do not
  silently fall back to a CLI flag.
- Introduce Fizeau-side environment variables that identify the caller as an
  agent: no `CLAUDE_*`, `ANTHROPIC_*`, `*_AGENT*`, `*_AUTOMATED*`, or similar
  Fizeau-introduced names on this path. Pre-existing variables already set
  in the operator's environment are passed through unchanged via the
  documented allowlist below.
- Send single-keystroke input bursts faster than the configured inter-key
  delay band (default 25–75ms per logical key). Pasted prompts go through
  bracketed paste and may arrive as one burst; that matches real paste
  behavior and is allowed.

`claude-tui` **may**:

- Use the bracketed-paste sequence to deliver a multi-line prompt in one
  event. Bracketed paste is the documented terminal mechanism for paste
  input and is what a real paste produces.
- Read raw PTY bytes, derive frames through `internal/pty/terminal`, and
  emit Fizeau service events from them. Service-event emission is internal
  to Fizeau and is not visible to the Claude binary.
- Cancel a turn with the key combination the TUI documents for cancellation
  (Esc by default; Ctrl-C as a fallback that exits the session, in which
  case `claude-tui` restarts a session for the next request).
- Reuse the existing `internal/pty/cassette` recorder and
  `internal/ptytest` assertion framework with no modifications beyond a new
  harness name and scenario fixtures.

### Environment Allowlist

The PTY session is started with the following environment, exactly:

- Pass-through from the operator environment: `HOME`, `PATH`, `USER`,
  `LOGNAME`, `SHELL`, `LANG`, `LC_ALL`, `TZ`, `XDG_*` (any present
  `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME`,
  `XDG_RUNTIME_DIR`), and any environment variables under the `CLAUDE_`
  prefix that the operator has already set in their shell (Fizeau itself
  must not set these — passing through what the operator already exported
  is acceptable because it represents the operator's normal shell state).
- Set by Fizeau if not already present: `TERM=xterm-256color` (default
  terminal type a modern shell would set), `LANG=C.UTF-8`, `LC_ALL=C.UTF-8`
  (locale defaults; existing operator values win).

Anything else is dropped. The cassette manifest's `env_allowlist` records
the exact set used per recording so future spec amendments can be reviewed
against captured evidence.

## Module Boundaries

Per ADR-002, no package below `internal/pty` may import
`internal/harnesses`. `claude-tui` honors that boundary:

| Layer | Path | Owns |
|-------|------|------|
| Runner | `internal/harnesses/claude-tui/runner.go` | Implements `harnesses.Harness` (`Info`, `HealthCheck`, `Execute`); owns event emission, prompt delivery, slash-command drivers for model/reasoning/permission selection, cancellation, timeout enforcement, idle timeout, request-metadata stamping. |
| TUI parser | `internal/harnesses/claude-tui/stream.go` | Frame-derived parsing of the Claude TUI: prompt echo recognition, assistant message extraction, tool-call/tool-result framing, usage extraction from `/cost` or equivalent TUI surfaces, final-event derivation. Must not import `internal/harnesses/claude`. |
| Model/reasoning discovery | `internal/harnesses/claude-tui/model_discovery.go` | TUI-driven model and reasoning enumeration. Persists results into `internal/harnesses.ModelDiscoveryCache` keyed by `claude-tui` so it does not collide with `claude` evidence. |
| Shared neutral helpers | `internal/harnesses/anthropic/` | `IsClaudeQuotaExhaustedMessage`, `MarkClaudeQuotaExhaustedFromMessage`, account-info shapes, model-name normalization. Consumed by both `claude` and `claude-tui`. Has no dependency on either harness package. |
| Quota reuse | (consumed, not re-implemented) | `claude-tui` reads `ClaudeQuotaSnapshot` from the existing durable cache. It does not own a parallel quota probe. |
| Cassettes/tests | `internal/harnesses/claude-tui/testdata/` and `internal/ptytest` scenarios | Live record cassettes per capability row; replay-only tests for default CI. |

The `claude-tui` runner must not import `internal/harnesses/claude`, and the
`claude` runner must not import `internal/harnesses/claude-tui`. The
`internal/harnesses/anthropic` neutral package is the only allowed sharing
seam.

## Capability Baseline Impact

`primary-harness-capability-baseline.md` adds a fifth row, `claude-tui`. At
introduction every row is `gap` until live PTY record-mode evidence exists,
mirroring how `claude` quota status was originally gated. The
`AutoRoutingEligible` flag stays `false` for `claude-tui` until promotion.

Known gaps at introduction with documented blockers (not optimistic
"pending evidence"):

| Row | Status | Known blocker |
|-----|--------|---------------|
| PermissionModes (`unrestricted`) | `gap`, no TUI affordance known | The `claude --print` path uses `--dangerously-skip-permissions`; no equivalent TUI affordance is documented today. Until a `/permissions bypass` or similar surface ships, `claude-tui` exposes only `safe` and `supervised`. |
| SetReasoning | `gap`, no TUI affordance known | The `claude --print` path uses `--effort`; no documented Claude TUI slash command sets per-turn reasoning. Until one ships, `claude-tui` does not set reasoning and treats requested non-default values as a routing rejection. |
| All other rows | `gap` until live cassette evidence | Standard ADR-002 promotion path. |

Once any `claude-tui` capability has accepted live cassette evidence, the
row becomes `pass` independently of `claude`. A `pass` on `claude` does not
imply `pass` on `claude-tui`, and vice versa.

`harness-golden-integration.md` extends the cassette scenario list to
include `claude-tui` authenticated record mode plus replay cassettes for
each capability row that has a TUI affordance.

## Performance and Cost Acknowledgement

The fork is not free:

- **Parsing cost**: Frame derivation through `internal/pty/terminal`
  (vt10x-backed) on every spinner/render update is meaningfully more
  expensive than parsing `stream-json` JSONL lines. For long turns with
  many redraws the CPU and wall-clock cost is noticeable. The benchmark
  suite (`scripts/benchmark/`) must include a `claude-tui` lane so this
  cost is measured before promotion to auto-routing.
- **Per-turn latency**: Inter-key delays for menu navigation add a fixed
  overhead (commonly a few hundred ms total) per turn that selects
  model/reasoning. Bracketed-paste prompts do not add latency.
- **Code duplication**: Two runners, two parsers, two discovery paths, two
  cassette suites. Estimated 1500–2500 LOC of additional code carried
  indefinitely (the existing `claude` package is ~3.5k LOC across
  runner/stream/quota; `claude-tui` will be smaller because it skips the
  stream-json fallback path and the `--print` argument builder). Review
  burden is the larger long-term cost.

These costs are accepted because subscription pricing is the load-bearing
economic constraint for primary-routing Claude capacity (ADR-011). If
`claude --print` ever lands on subscription capacity reliably and durably
without TUI-driven traffic, this ADR is open to revision.

## Alternatives

| Option | Pros | Cons | Evaluation |
|--------|------|------|------------|
| **Fork as `claude-tui` (selected)** | Two implementations evolve independently; capability evidence is unambiguous per identity; honors ADR-002 PTY-first design; allows `claude` to keep shipping while PTY work matures; lets the routing layer treat subscription-billed and API-billed Claude as distinct supply pools. | One-time duplication of runner scaffolding; risk of drift in shared parsing helpers (mitigated by extracting them to `internal/harnesses/anthropic`). | **Selected**: matches the subscription-pricing driver, matches ADR-002's library-first boundary, and avoids hidden conditional branches in the hot path. |
| Add a `Transport` mode to existing `claude.Runner` | No new package; smaller code footprint at first. | Conditionals in every hot path (Execute, stream parsing, model discovery, error classification); capability evidence becomes ambiguous because the harness identity collapses two transports into one row; auto-routing eligibility decisions cannot be expressed cleanly; `HarnessConfig` fields like `BaseArgs` and `ModelFlag` have no meaning for half the configurations of a single name. | Rejected: explicitly the failure mode the user called out. |
| Replace `claude` with PTY-only implementation now | Single canonical path. | Throws away existing `--print` cassettes, runner tests, and quota/auto-routing evidence before PTY parity is proven; high risk of regressing primary subscription capacity. | Rejected: parity is unproven; coexistence is cheaper and safer. |
| Drive PTY through tmux | Reuses well-trodden multiplexer patterns. | ADR-002 already rejected this for the core path; would inherit global tmux server state and weaken cassette determinism. | Rejected by ADR-002. |
| Build a generic "TUI harness" abstraction now, parameterized per CLI | One library covers Claude, Codex, Gemini. | Premature: only Claude needs this today; the right shape is unclear until at least two consumers exist; would slow `claude-tui` shipping. | Rejected for now; revisit if a second consumer emerges. |

## Consequences

| Type | Impact |
|------|--------|
| Positive | Subscription-billed Claude capacity is accessible through a stable, documented TUI surface. |
| Positive | Capability evidence stays unambiguous: each harness identity owns its own baseline row. |
| Positive | The existing `claude` harness, its tests, and its operator-visible behavior are unaffected during the rollout. |
| Positive | The routing layer can treat subscription-billed (`claude-tui`) and API-billed (`claude`) Claude as distinct supply pools with distinct cost classes. |
| Negative | The codebase carries two Claude runners until a future retirement ADR. Reviewers must keep them honestly separate. |
| Negative | Frame-derived parsing has a real CPU cost relative to stream-json parsing. Benchmark lane required before auto-routing promotion. |
| Negative | Live record-mode cassettes for `claude-tui` need authentication and quota windows; CI footprint grows on opt-in record runs. |
| Negative | Some capability rows (`unrestricted` permissions, per-turn reasoning) are blocked indefinitely until Anthropic ships TUI affordances. |
| Neutral | Quota evidence is shared across both Claude identities because Anthropic accounts limit per account, not per Fizeau-side transport — under the named single-account assumption. |

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| Shared parsing helpers drift between `claude` and `claude-tui` | M | M | Extract genuinely shared helpers into `internal/harnesses/anthropic` before consuming from both runners; cover with helper-level tests; lint rule blocks cross-imports. |
| TUI surface changes break the parser between Claude CLI releases | H | M | Cassette manifests pin binary version; replay tests pin manifest IDs and content digests (ADR-002 contract); a TUI surface change that breaks parsing surfaces as a capability `gap` in the matrix, not as silent execution failures. |
| Operators conflate `claude` and `claude-tui` in routing config | M | L | Routing surface treats the two identities as fully distinct (already true via primary baseline rows); error messages from the routing layer name the exact identity that was selected. |
| Single-account quota assumption fails for multi-account operators | L | M | Cache key gains an account dimension in a follow-up spec when the first multi-account user report lands; until then, document the assumption in the routing snapshot. |
| Anthropic changes the boundary between subscription and API billing such that PTY no longer guarantees subscription routing | L | H | The ADR is open to revision; the fork's coexistence design means `claude` remains available as the API-billed path with no code change required to fall back. |
| Performance regression on long turns from frame-derived parsing | M | M | Required benchmark lane in `scripts/benchmark/` covers a long-turn `claude-tui` scenario; promotion to auto-routing requires acceptable benchmark deltas vs. `claude`. |

## Validation

| Success Metric | Review Trigger |
|----------------|----------------|
| `internal/harnesses/claude-tui/` exists as a sibling package with no imports of `internal/harnesses/claude` (and vice versa) | A code change makes `claude-tui` import `claude` or vice versa |
| `internal/harnesses/anthropic/` exists and is the only shared seam | A shared helper appears in either harness package |
| `claude-tui` registry entry has empty `BaseArgs`, empty `PermissionArgs`, and empty `ModelFlag`/`ReasoningFlag`/`WorkDirFlag` | A registry change adds any of those for `claude-tui` |
| `claude-tui` runner uses `internal/pty/session` exclusively (no `os/exec` invocations of `claude`) | A code change introduces `os/exec` in the `claude-tui` runner hot path |
| Environment passed to the `claude-tui` PTY session is exactly the documented allowlist; cassette manifests record it | A recording uses an env outside the allowlist or sets a Fizeau-introduced `CLAUDE_*`/`ANTHROPIC_*` variable |
| `primary-harness-capability-baseline.md` shows a separate `claude-tui` row with independent per-capability status; known-blocker rows (`unrestricted`, `SetReasoning`) are annotated | A baseline change collapses both identities into one row or hides a known blocker |
| Live record-mode cassettes for `claude-tui` capability rows live under `internal/harnesses/claude-tui/testdata/` and replay under default CI | A `pass` capability cell cites only `claude` evidence or only synthetic fixtures |
| Both `claude` and `claude-tui` share the same `ClaudeQuotaSnapshot` durable cache under the documented single-account assumption | A change adds a second per-identity quota cache or runs a redundant probe |
| Benchmark suite includes a `claude-tui` lane covering at least one long-turn scenario | Auto-routing promotion is proposed without a benchmark delta vs. `claude` |
| `AutoRoutingEligible=true` for `claude-tui` is set only by an explicit follow-up spec change citing live evidence and benchmark deltas | `claude-tui` becomes auto-routing eligible without a documented promotion decision |

## Concern Impact

- **Resolves subscription-pricing access for Claude prompt execution**:
  Establishes a stable TUI-driven path so routed Claude traffic lands on
  subscribed capacity rather than per-token API pricing.
- **Supports ADR-002**: Extends direct PTY ownership from the quota probe
  into normal prompt execution without weakening the library boundary.
- **Supports ADR-011**: Lets cost-based routing model subscription-billed
  and API-billed Claude as distinct supply pools.
- **Supports primary-harness capability baseline**: Adds a clean second
  primary identity for Claude rather than blurring evidence across transport
  modes.

## References

- [ADR-002 PTY Cassette Transport for Harness Golden Masters](./ADR-002-pty-cassette-transport.md)
- [ADR-004 Terminal Harness Build-vs-Buy Boundary](./ADR-004-terminal-harness-build-vs-buy.md)
- [ADR-011 Cost-Based Routing With Quota Pools](./ADR-011-cost-based-routing-with-quota-pools.md)
- [Primary Harness Capability Baseline](../primary-harness-capability-baseline.md)
- [Harness Golden-Master Integration](../harness-golden-integration.md)
- [Implementation plan: claude-tui fork](../plan-2026-05-14-claude-tui-fork.md)
- `internal/harnesses/types.go` — `Harness` interface, `ExecuteRequest`, `HarnessInfo`, event types
- `internal/harnesses/harness.go` — `HarnessConfig` registry struct
- `internal/harnesses/registry.go` — `builtinHarnesses` map
- `internal/harnesses/claude/runner.go` — existing `--print` subprocess runner
- `internal/harnesses/claude/quota_pty.go` — existing direct-PTY `/usage` probe
- `internal/harnesses/ptyquota/probe.go` — shared PTY probe scaffold
- `internal/pty/session`, `internal/pty/terminal`, `internal/pty/cassette` — direct PTY library

## Review Checklist

- [x] Context names a specific problem
- [x] Decision statement is actionable
- [x] At least two alternatives were evaluated
- [x] Each alternative has concrete pros and cons
- [x] Selected option's rationale explains why it wins
- [x] Consequences include positive and negative impacts
- [x] Negative consequences have mitigations
- [x] Risks are specific with probability and impact assessments
- [x] Validation section defines review triggers
- [x] Concern impact is complete
- [x] ADR is consistent with governing feature spec and PRD requirements
