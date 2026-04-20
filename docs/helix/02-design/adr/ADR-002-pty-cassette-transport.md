---
ddx:
  id: ADR-002
  depends_on:
    - helix.prd
    - helix.arch
    - CONTRACT-003
---
# ADR-002: PTY Cassette Transport for Harness Golden Masters

| Date | Status | Deciders | Related | Confidence |
|------|--------|----------|---------|------------|
| 2026-04-20 | Accepted, amended | DDX Agent maintainers | `CONTRACT-003`, harness capability matrix | Medium |

## Context

| Aspect | Description |
|--------|-------------|
| Problem | Real subprocess harness support needs golden-master evidence that exercises the same PTY behavior users see, but the project has not chosen whether tmux, direct PTY supervision, or a separate terminal recorder owns that lifecycle. |
| Current State | Runtime subprocess execution uses `os/exec` plus harness-specific runners. Quota probes have used tmux-shaped experiments, but normal harness execution does not have one attachable PTY transport. Existing concerns require either standardizing on tmux for the whole lifecycle or owning PTY/session supervision directly. |
| Requirements | One transport must cover live execution, record mode, replay mode, cancellation, cleanup, inspection, quota/status probing, service-event capture, and deterministic cassette playback. Record mode must fail fast on missing binary/auth/subscription/quota instead of writing misleading fixtures. |

## Current-State Research

This decision was re-reviewed against local and current external terminal-agent
managers on 2026-04-20.

| Source | Transport Shape | Useful Patterns | Limits for DDX Agent |
|--------|-----------------|-----------------|----------------------|
| `gastown` local repo | tmux is the runtime/session boundary | Session creation runs the target command directly instead of racing shell readiness; pane capture and process-command checks are used for inspection and zombie cleanup; input helpers account for paste/Enter timing. | Strong operator UX, but the service would inherit tmux server state, send timing quirks, and display scraping as correctness inputs. |
| `ntm` current GitHub repo | tmux is an explicit required dependency and multi-agent control plane | Uses named sessions/panes, strict session-name validation, command timeouts, a circuit breaker, semantic capture budgets, `pipe-pane` streaming with polling fallback, and buffer paste for multiline/large prompts. | Its own analysis notes deep tmux lock-in. Good negative evidence for DDX Agent's library boundary. |
| `claude-squad` current GitHub repo | hybrid: tmux owns sessions, Go PTY owns attach/control | Uses `creack/pty` to attach to tmux, resize the attached session, forward stdin/stdout, and write direct bytes for key input while still using tmux `capture-pane`. | Confirms that user impersonation needs a real terminal channel, but still leaves tmux as the persistent lifecycle owner. |
| `dmux` current GitHub repo | tmux panes plus git worktrees, TypeScript/Ink UI | Treats tmux as an operator pane manager with persistent panes, hooks, and worktree isolation. | Valuable operator pattern; not a deterministic record/replay or service-event evidence layer. |
| `dun` local repo | non-interactive CLI harnesses | Uses stdin/stdout CLI modes such as `claude --print` and `codex exec -`; prior spike docs prefer stdin for large prompts. | Does not solve TUI-only model/quota/status surfaces. |
| `creack/pty` | direct Go PTY primitive | Starts commands with a controlling terminal, supports explicit terminal sizing and resize handling, and keeps lifecycle inside the process. | Requires DDX Agent to own screen parsing, process-group cleanup, timeout behavior, and inspection UI. |
| `Netflix/go-expect` | expect-style terminal automation | Useful expectation/input layer over a pseudoterminal. | It does not own process lifecycle, so it is a helper over the selected terminal transport, not the transport itself. |
| `asciinema` | terminal recording/playback | Proven lightweight terminal recording format with timing and replay concepts. | Record/playback only; it does not drive the harness or emit CONTRACT-003 service events. |

The current ecosystem trend for human multi-agent operation is tmux. That is not
the right baseline for DDX Agent. DDX Agent needs a reusable direct PTY library
because cassettes need raw bytes, structured service events, deterministic
replay, credential-free playback, and process cleanup without depending on
global tmux server state. tmux evidence is useful for understanding common TUI
failure modes, but it must not be part of the core harness capability story.

## Decision

DDX Agent will own direct PTY lifecycle in-process using Go `os/exec` plus a
small reusable PTY library. tmux is not part of the core harness execution,
model-list probing, quota probing, cassette recording, cassette replay,
cancellation, or inspection design.

Existing tmux quota helpers are legacy experiments. They can remain only as
temporary diagnostics while direct PTY replacements are being built, and their
results do not promote a capability to final `supported` status. Any capability
that can only be proven through tmux is `gap` until direct PTY evidence exists.

If a future operator project wants tmux attach/switch UX, it must live outside
the core service/cassette path and consume DDX Agent outputs like any other
client. The DDX Agent baseline is direct PTY only.

The cassette recorder and player remain part of this repository until another
consumer needs the same API. If reuse appears, split the PTY cassette player
into a separate project only after the cassette format has one versioned
contract and at least two real consumers.

**Key Points**: Direct PTY only | tmux helpers are legacy diagnostics |
cassettes are versioned evidence artifacts

## Module Boundaries

The direct PTY work must land as a reusable terminal library with narrow
package boundaries. Harness-specific code consumes the library; it does not
live inside it.

| Layer | Proposed Boundary | Owns | Must Not Own |
|-------|-------------------|------|--------------|
| Raw PTY session | `internal/pty/session` | `Start`, command argv/env/workdir, terminal size, PTY file descriptors, process groups, stdin bytes, resize, raw output stream, `Wait`, timeout, cancellation, `Close`/`Kill` cleanup | Claude/Codex parsing, model names, quota semantics, cassette schema, service events |
| Terminal model | `internal/pty/terminal` | Byte-to-frame derivation, normalized screen snapshots, key encoding, expect/wait predicates, frame diffing, capture metadata | Process spawning, harness-specific slash commands, quota/model interpretation |
| Cassette record/replay | `internal/pty/cassette` | Versioned manifest, input/output/frame streams, timing normalization, scrub reports, deterministic playback driver, read-only inspection inputs | Live credentials, provider calls, harness-specific capability decisions |
| Harness probes | `internal/harnesses/<name>` | Claude/Codex prompt flows, quota/status/model-list extraction, reasoning-level discovery, normalized errors, capability matrix updates | PTY lifecycle primitives or cassette file-format internals |

No package below `internal/pty` may import `internal/harnesses`. The PTY
library must be testable with synthetic programs and ordinary Unix TUIs before
Claude or Codex are involved. Claude and Codex quota/model probes are acceptance
tests for the harness adapters, not proof that the PTY library is complete by
themselves.

## Cassette Data Contract

Every cassette is a single versioned directory or archive with a manifest and
append-only event streams. Version `1` contains:

| Field | Required | Description |
|-------|----------|-------------|
| `manifest.version` | Yes | Cassette schema version. Starts at `1`; incompatible changes increment it. |
| `manifest.harness` | Yes | Harness name, binary path fingerprint, binary version string when available, and capability row snapshot. |
| `manifest.command` | Yes | Scrubbed argv, working directory policy, environment allowlist names, timeout settings, and permission mode. |
| `manifest.terminal` | Yes | Initial rows/cols, resize events, locale, TERM value, and PTY mode flags needed for replay. |
| `manifest.provenance` | Yes | Agent git SHA, contract version, OS/arch, recorded-at timestamp, and recorder version. |
| `input.jsonl` | Yes | User/input events: bytes sent to stdin, paste boundaries, control keys, resize events, signal events, and timing deltas. |
| `output.raw` | Yes | Raw output bytes from the PTY, exactly as observed after environment scrubbing. |
| `frames.jsonl` | Yes | Screen snapshots or frame diffs at normalized timestamps for human review and deterministic replay assertions. |
| `service-events.jsonl` | Yes | CONTRACT-003 service events emitted during the run, including routing, tool, final, and typed-drain-compatible payloads. |
| `final.json` | Yes | Exit status, signal, duration, final metadata, usage, cost, routing actual, session log path, and normalized final text. |
| `quota.json` | When applicable | Scrubbed quota/status probe output and parsed quota windows used to accept or reject the record run. |
| `scrub-report.json` | Yes | Redaction rules applied, environment values removed, secret-pattern hit counts, and fields intentionally preserved. |

Timing is stored as monotonic deltas from cassette start. Replay may scale or
collapse delays, but it must preserve event order, resize ordering, process
exit, and final service metadata.

## Record Mode

Record mode runs the real harness binary through the direct PTY transport. It
fails before writing a cassette when:

- the harness binary is missing or not executable;
- authentication is missing, expired, or for the wrong account;
- subscription or quota state cannot be confirmed for subscription harnesses;
- requested model, reasoning, permission, or workdir capability is unsupported
  by the harness capability matrix;
- the run exits before producing a final service event.

If a failure happens after cassette creation starts, the recorder writes an
explicit failed-run artifact only under a diagnostic path, never as accepted
golden-master evidence.

## Replay Mode

Replay mode never uses credentials and never contacts a provider. It feeds the
recorded input/output/frame streams through the same parser, service-event
decoder, and typed drain assertions used by live mode. Replay can prove parser,
event-shape, cancellation, cleanup, and PTY transport behavior; it cannot prove
that a live external harness still works today.

Replay is deterministic by default:

- timestamps are interpreted as ordered deltas, not wall-clock requirements;
- environment is reconstructed only from the cassette allowlist;
- terminal size and resize events come from `manifest.terminal`;
- service-event assertions compare typed payloads after documented scrub rules,
  not raw secrets or machine-specific paths.

## PTY Library Test Strategy

The PTY library is not complete until it proves useful behavior against real
terminal programs, not only fake sessions and happy-path harness probes.

| Test Class | Required Coverage |
|------------|-------------------|
| Unit and fake-session tests | Startup failure, normal exit, EOF, timeout, cancellation, process-group cleanup, large input, multiline paste boundaries, control keys, resize events, raw output capture, frame derivation, deterministic fake clock, and replay ordering. |
| Host PTY smoke tests | Portable Unix commands such as `sh`, `cat`, `stty size`, and `sleep` verify stdin/stdout, exit status, terminal sizing, cancellation, and no leaked child processes without credentials or network. |
| Docker TUI conformance tests | A pinned Linux container image supplies known TUI programs. The first required target is Unix `top`: capture several distinct screens from one run, including initial paint, later refresh frames, and at least one interaction or resize that changes the screen. Assertions check semantic screen facts and frame progression rather than brittle byte-for-byte full-screen output. |
| Additional TUI diversity | Add at least two more common terminal shapes before calling the library mature: a pager flow such as `less`, and an editor or curses-style full-screen flow such as `vim`, `nano`, or `dialog`, using Docker when host availability is inconsistent. |
| Cassette replay tests | Record a deterministic synthetic terminal run, replay it through the cassette reader/player, and assert manifest fields, input ordering, raw output, frame snapshots, scrub report, final status, and read-only replay behavior. |
| Authenticated harness tests | Opt-in recorder tests drive Claude and Codex through the same PTY library to extract quota/status, model listings, reasoning levels, and token usage. Missing binary/auth/quota/timeout cases must fail before writing accepted cassettes. |

## Inspection

Live inspection attaches to a read-only mirror of the direct PTY stream.
Inspectors may watch frames and output bytes but cannot write to stdin, resize
the authoritative PTY, or mutate cassette files. Recorded-run inspection reads
`frames.jsonl` and `output.raw` through a viewer that opens files read-only and
never normalizes or rewrites the evidence.

## Alternatives

| Option | Pros | Cons | Evaluation |
|--------|------|------|------------|
| **Direct PTY ownership in agent** | One dependency-light lifecycle for execution, record, replay, cancellation, and inspection; portable test seams; cassette format can be shaped around CONTRACT-003 events | Requires careful PTY implementation and platform testing; attach UX must be built | **Selected: best fit for a library-first service boundary without a global tmux dependency** |
| Terminal-session interface with direct PTY and tmux adapters | Would preserve an easy operator escape hatch | Keeps tmux alive as an attractive partial implementation and makes capability evidence ambiguous | Rejected for the core baseline; direct PTY library only |
| Standardize on tmux for all harness lifecycle | Mature attach/detach UX, pane capture, process supervision already exists; matches tools such as gastown, ntm, claude-squad, and dmux | Makes tmux a hard dependency for library consumers and CI; Windows portability is poor; machine-local tmux state complicates deterministic replay; tmux capture is a derived screen view rather than raw service evidence | Rejected |
| Keep tmux only for quota/status while direct exec handles normal runs | Minimal short-term change | Violates the single-transport concern; quota behavior and live execution would diverge; cassette replay could not prove the path that quota probes use | Rejected: partial helper is explicitly the failure mode this ADR resolves |
| Adopt ntm or another terminal manager as the core | Faster access to mature tmux orchestration patterns and robot APIs | Adds another lifecycle owner without CONTRACT-003 semantics; inherits tmux coupling; does not define DDX Agent cassette/service-event evidence | Rejected |
| Use asciinema/script-style recorder as the core | Existing terminal recording/playback concepts and viewer ecosystem | Records terminal output but does not drive input, manage auth/quota preflight, own process cleanup, or emit service events | Rejected: useful format reference, insufficient as harness transport |
| Split a generic PTY cassette project now | Clean abstraction if multiple projects need it | Premature API freeze; no second consumer yet; slows harness support beads | Rejected for now; revisit after one stable format and a second consumer |

## Consequences

| Type | Impact |
|------|--------|
| Positive | Harness execution, quota probes, model-list probes, record/replay, cancellation, and inspection share one direct PTY library. |
| Positive | Library consumers do not need tmux installed to use or test DDX Agent harness support. |
| Positive | Golden-master cassettes can carry CONTRACT-003 service events and typed-drain payloads as first-class evidence. |
| Negative | The project must own PTY edge cases: resize races, process groups, signal handling, terminal modes, and OS portability. |
| Negative | Read-only inspection needs a purpose-built viewer instead of relying on `tmux attach`. |
| Neutral | Developers may still use tmux manually outside DDX Agent, but tmux is not a DDX Agent dependency or evidence source. |

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| Direct PTY implementation leaks subprocesses on cancellation | M | H | Add process-group cleanup tests, timeout tests, and failed-run diagnostics before marking live capabilities supported |
| Legacy tmux helpers linger and hide direct PTY gaps | M | H | Track replacement beads and mark tmux-only capabilities `gap` until direct PTY evidence exists |
| Cassette scrub rules remove data needed for replay | M | M | Store scrub reports and compare replay against typed events rather than raw secrets |
| Replay creates false confidence about live harness availability | H | M | Keep live-run policy: fresh record-mode evidence is required to promote or retain `supported` capability status |
| Cross-platform PTY behavior diverges | M | M | Define OS-specific transport adapters behind one cassette contract and require per-OS fixtures before claiming support |

## Validation

| Success Metric | Review Trigger |
|----------------|----------------|
| A future cassette runner can record and replay one codex or claude run through the same direct PTY transport | Record and replay use different process/session supervisors |
| PTY conformance tests capture useful multi-frame output from Unix `top` and at least two other terminal program shapes | The library is marked complete using only fake sessions or Claude/Codex probes |
| Codex and Claude model-list and quota probes run through the direct PTY library | A capability is marked supported from tmux-only evidence |
| Accepted cassettes contain manifest, input, output, frames, service events, final metadata, quota data when applicable, and scrub report | A cassette lacks any required version-1 artifact |
| Record mode refuses missing auth/quota/binary cases before writing accepted evidence | CI or local record mode creates a passing cassette for an unauthenticated harness |
| Inspection cannot alter the live PTY or recorded files | Viewer writes to stdin, resizes the authoritative PTY, or rewrites cassette artifacts |

## Concern Impact

- **Resolves inspectable harness execution concern**: Selects direct PTY
  ownership as the canonical service evidence path and rejects tmux in the core
  harness/cassette design.
- **Supports harness capability matrix**: Future `supported` harness
  capabilities can cite versioned cassette evidence produced by this transport.

## References

- [CONTRACT-003 DdxAgent Service Interface](/Users/erik/Projects/agent/docs/helix/02-design/contracts/CONTRACT-003-ddx-agent-service.md)
- [Concerns](/Users/erik/Projects/agent/docs/helix/01-frame/concerns.md)
- [Architecture](/Users/erik/Projects/agent/docs/helix/02-design/architecture.md)
- [gastown local tmux wrapper](/Users/erik/Projects/gastown/internal/tmux/tmux.go)
- [dun local harness spike](/Users/erik/Projects/dun/main/docs/helix/01-frame/spikes/SPIKE-001-nested-agent-harness.md)
- [Named Tmux Manager](https://github.com/Dicklesworthstone/ntm)
- [Claude Squad](https://github.com/smtg-ai/claude-squad)
- [dmux](https://github.com/standardagents/dmux)
- [creack/pty](https://github.com/creack/pty)
- [Netflix/go-expect](https://github.com/Netflix/go-expect)
- [asciinema](https://github.com/asciinema/asciinema)

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
