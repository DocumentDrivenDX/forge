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
| Requirements | One direct PTY transport must cover metadata probe mode, record mode, replay mode, cancellation, cleanup, quota/status/model/reasoning probing, service-event capture, and deterministic cassette playback. Normal prompt execution may continue to use harness-native batch modes when they satisfy DDX requirements. Record mode must fail fast on missing binary/auth/subscription/quota instead of writing misleading fixtures. |

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
small reusable PTY library. That library includes cassette recording and
playback as a first-class layer; recording/replay is not a separate harness
helper bolted on beside the PTY code. tmux is not part of the core harness
execution, model-list probing, quota probing, cassette recording, cassette
replay, cancellation, or inspection design.

Existing tmux quota helpers are legacy experiments. They can remain only as
temporary diagnostics while direct PTY replacements are being built, and their
results do not promote a capability to final `supported` status. Any capability
that can only be proven through tmux is `gap` until direct PTY evidence exists.

If a future operator project wants tmux attach/switch UX, it must live outside
the core service/cassette path and consume DDX Agent outputs like any other
client. The DDX Agent baseline is direct PTY only.

SPIKE-002 clarified the only acceptable success criteria for this area:
DDX Agent must control Claude/Codex well enough to extract TUI-only quota,
available-model, reasoning-level, and related status facts; and it must replay
cassettes well enough that client-side parsers and terminal assertions run
without live Claude/Codex binaries, credentials, or network. Per-run token
usage remains a core capability, but it should stay on native stream or batch
JSON evidence unless a future harness path makes it TUI-derived. tmux's human
attachability is useful operator UX, but it is not in the current baseline and
is not the accepted replay or capability-evidence path.

The baseline live process is a small background probe, not a general terminal
control plane. It may periodically start Claude, Codex, or another harness,
drive the minimum TUI flow required to read quota/status/model/reasoning facts,
write a scrubbed snapshot/cache, and exit. If a harness's batch prompt mode
later stops working for normal execution, the same PTY wrapper may be promoted
to drive an interactive session, but that is a future fallback decision rather
than the first implementation target.

The cassette recorder and player remain part of `internal/pty` for the baseline
implementation, subject to the build-vs-buy gate in
[ADR-004](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-004-terminal-harness-build-vs-buy.md).
The project will adopt PTY, terminal-emulator, and recording concepts where
existing libraries fit. If reuse appears later, extract the mature PTY library
as a whole rather than splitting cassette playback from session and terminal
modeling prematurely.

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
| Cassette record/replay | `internal/pty/cassette` | Versioned manifest, input/output/frame streams, event timestamps, timing normalization, scrub reports, replay scheduler, deterministic and real-time playback drivers, read-only inspection inputs | Live credentials, provider calls, harness-specific capability decisions |
| Cassette assertion tests | `internal/ptytest` or equivalent test-only package | Scenario specs, fixture discovery, cassette playback assertions, time-coded predicates, replay clocks, fixture isolation, parallel-safe temp homes, and test reporting | Production harness behavior, credential storage, terminal emulation internals |
| Harness probes | `internal/harnesses/<name>` | Claude/Codex prompt flows, quota/status/model-list extraction, reasoning-level discovery, normalized errors, capability matrix updates | PTY lifecycle primitives or cassette file-format internals |
| Debug snapshots | CLI/debug helpers over `internal/pty` | Dump current rendered VT state, cursor/screen metadata, recent raw byte offsets, and recent timed input/output events for failed probes | Interactive terminal UI, long-lived session management, tmux-style attachability |

No package below `internal/pty` may import `internal/harnesses`. The PTY
library must be testable with synthetic programs and ordinary Unix TUIs before
Claude or Codex are involved. Claude and Codex quota/model probes are acceptance
tests for the harness adapters, not proof that the PTY library is complete by
themselves. The terminal rendering decision is detailed in
[ADR-003](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-003-pty-terminal-rendering.md).
The build-vs-buy boundary and extraction triggers are detailed in
[ADR-004](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-004-terminal-harness-build-vs-buy.md).
The terminal rendering decision is supported by the `top` spike in
[SPIKE-001](/Users/erik/Projects/agent/docs/helix/02-design/spikes/SPIKE-001-direct-pty-top-rendering.md).
The recorder/driver build-vs-buy pressure test is captured in
[SPIKE-002](/Users/erik/Projects/agent/docs/helix/02-design/spikes/SPIKE-002-terminal-driver-recorder-alternatives.md).

## Data Flow

The cassette layer observes the PTY library; it does not replace or wrap
harness-specific parsing.

```text
internal/pty/session raw bytes and input events
  -> internal/pty/terminal frame derivation and screen normalization
  -> internal/harnesses/<name> adapter parsing and service-event emission
  -> internal/pty/cassette CassetteTee writes raw output, timed input, frames,
     opaque service-event JSON, final metadata, and scrub reports
```

`internal/pty/cassette` may store and replay opaque service-event JSON, but it
must not import harness adapters or CONTRACT-003 typed-event decoders. Service
assertions stay above the cassette library. Harness adapters hand timed opaque
events to the cassette layer through a narrow `CassetteTee`-style interface so
the dependency direction stays `internal/harnesses/<name>` -> `internal/pty`,
never the reverse.

## Cassette Data Contract

Every cassette is a single versioned directory or archive with a manifest and
append-only event streams. Version `1` contains:

| Field | Required | Description |
|-------|----------|-------------|
| `manifest.version` | Yes | Cassette schema version. Starts at `1`; incompatible changes increment the major version. |
| `manifest.id` | Yes | Stable UUID generated when the cassette is recorded. Assertion specs must reference this ID so they cannot silently attach to a different recording. |
| `manifest.content_digest` | Yes | Digest metadata for recorded evidence, including at least `sha256` over `output.raw`. Assertion specs must reference the digest they were authored against. |
| `manifest.harness` | Yes | Harness name, binary path fingerprint, binary version string when available, and capability row snapshot. |
| `manifest.command` | Yes | Scrubbed argv, working directory policy, environment allowlist names, timeout settings, and permission mode. |
| `manifest.terminal` | Yes | Initial rows/cols, resize events, locale, TERM value, PTY mode flags, and terminal emulator identity `{name, version}` used to derive frames. |
| `manifest.timing` | Yes | Clock policy, timestamp resolution, replay default, and any scaling/collapse policy used by tests. Version `1` defaults to 100ms timestamp resolution, but recorders may choose a finer `resolution_ms` without a schema bump. |
| `manifest.provenance` | Yes | Agent git SHA, contract version, OS/arch, recorded-at timestamp, and recorder version. |
| `input.jsonl` | Yes | User/input events: bytes sent to stdin, paste boundaries, control keys, resize events, and signal events. Every record includes monotonic `seq` and `t_ms`. |
| `output.raw` | Yes | Raw output bytes from the PTY, exactly as observed after environment scrubbing. This is the byte-for-byte evidence stream. |
| `output.jsonl` | Yes | Timed raw output chunks from the PTY. Every record includes monotonic `seq` and `t_ms`, byte offset into `output.raw`, chunk length, and optional chunk digest. Inline chunk bytes are forbidden in version `1`; replay reads bytes from `output.raw` by offset to avoid JSON byte-encoding ambiguity. |
| `frames.jsonl` | Yes | Screen snapshots or frame diffs at monotonic `seq` and `t_ms` timestamps for human review and deterministic replay assertions. Frames are derived artifacts, not the byte-level evidence source. |
| `service-events.jsonl` | Yes | Opaque service-event JSON emitted during the run, including routing, tool, final, and typed-drain-compatible payloads. Every record includes monotonic `seq` and `t_ms`. |
| `final.json` | Yes | Exit status, signal, duration, final metadata, usage, cost, routing actual, session log path, and normalized final text. |
| `quota.json` | When applicable | Scrubbed quota/status probe output and parsed quota windows used to accept or reject the record run. |
| `scrub-report.json` | Yes | Redaction rules applied, environment values removed, secret-pattern hit counts, and fields intentionally preserved. |
| `assertions.json` or `assertions.yaml` | Test fixtures only | Time-coded semantic assertions for automated tests. This is a sidecar test spec, not observed evidence, and may be regenerated or tightened without changing the recorded PTY facts. |

Because the child process runs under a PTY, stdout and stderr are normally
merged by the terminal slave and recorded together in `output.raw`. If a future
harness exposes a separate non-PTY stderr stream, that stream must either be
normalized into service events or added as an explicit optional artifact; it
must not be silently dropped.

Timing is event-driven. The recorder writes a monotonic `seq` and monotonic
`t_ms` timestamp on every observed input event, raw output chunk, resize,
signal, derived frame, service event, and final event. Fixed-interval frame
sampling is optional and derived; it is not the authoritative recording model.
This keeps recordings compact while preserving the exact timeline needed to
test terminal emulation, buffering, delays, and interactions.

Timestamps are stored as monotonic milliseconds from cassette start, quantized
to `manifest.timing.resolution_ms`. Version `1` uses `resolution_ms: 100` by
default so replay preserves the shape of a real TUI session without pretending
to be nanosecond-accurate. Recorders may use a finer capture resolution in
version `1` when a TUI or test needs it; replay mode remains orthogonal to
capture resolution. Replay supports three timing modes:

- `realtime`: sleep according to recorded `t_ms` values at the recorded
  resolution; this is the default for human inspection and visual playback.
- `scaled`: multiply recorded delays by a caller-provided factor while
  preserving event order and relative pacing.
- `collapsed`: ignore sleeps and replay in event order for fast deterministic
  CI assertions.

Replay must preserve event order, raw output chunk boundaries, resize ordering,
process exit, final service metadata, and the recorded timing relation within
one timestamp resolution. Terminal emulator tests replay `output.jsonl` at full
speed under a virtual clock, so assertions run at the same logical place in the
timeline without waiting on wall-clock sleeps.

Event order is authoritative by `seq`, not by `t_ms` alone. Multiple events may
share the same quantized timestamp. Replay and assertion evaluation must process
same-`t_ms` events in ascending `seq` order. `seq` is global across all cassette
streams, assigned at observation time, and must be contiguous after merge. If a
reader needs a deterministic merge for older diagnostic artifacts that lack
`seq`, the fallback ordering is resize/signal, input, output chunk, derived
frame, service event, final; accepted version-1 cassettes must not rely on that
fallback.

Nondeterministic terminal content normalization is separate from secret
scrubbing. Scrubbing removes sensitive values. Normalization handles volatile
screen facts such as clocks, PIDs, elapsed durations, and animation counters so
semantic frame assertions remain stable without weakening raw evidence storage.

Default scrubbing and normalization rules are part of the cassette contract.
Version `1` starts with: explicit environment allowlist, `HOME` and worktree
path rewriting, bearer/API token patterns, account identifiers where configured,
UUID/request/session identifiers, RFC3339 and local timestamp values, elapsed
durations, PIDs, transient socket/file names, and animation counters. Harness
adapters may register extension rules, but the scrub report must list every
rule applied and every intentionally preserved volatile field.

## Schema Evolution

Version `1` readers reject cassettes with a `manifest.version` higher than the
reader supports, missing required artifacts, missing required fields, or unknown
required feature flags. Readers may ignore unknown optional fields within the
same major version. Additive optional fields do not require a schema bump;
renaming, removing, or changing the meaning of required fields requires a new
major version. Writers stamp the supported `manifest.version` and refuse to
overwrite a cassette written with a newer major version.

Recorders must compute timing from a monotonic elapsed clock such as
`time.Since(recordingStart)` or an injected monotonic test clock. They must not
derive `t_ms` from wall-clock timestamps after serialization. Tests must cover a
wall-clock jump during recording without changing monotonic event order.

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

Replay mode is parallel-safe. Record mode is not assumed to be parallel-safe for
authenticated harnesses. Recorders must serialize per harness account and fail
fast on lock contention rather than running two Claude or Codex record jobs
against the same credential, quota window, or session store.

Accepted authenticated cassettes must carry freshness metadata sufficient for
the capability matrix: `captured_at`, harness binary version, auth/account
class, and any freshness window used for a `supported` claim. Stale cassettes
remain useful parser fixtures, but they cannot promote or retain live
capability support without a documented refresh policy.

## Replay Mode

Replay mode never uses credentials and never contacts a provider. It feeds the
recorded input/output/frame streams through the same parser, service-event
decoder, and typed drain assertions used by live mode. Replay can prove parser,
event-shape, timing behavior, cancellation, cleanup, and PTY transport behavior;
it cannot prove that a live external harness still works today.

`output.raw` plus `output.jsonl` is the authoritative replay input for terminal
emulator assertions. `frames.jsonl` is stored for human review, debugging, and
fast smoke checks, but correctness assertions that validate terminal rendering
must be able to re-derive frames from `output.raw`/`output.jsonl` through the
manifest-pinned emulator. When the emulator backend or version changes, the
reader must either reject stale frame-derived assertions with a clear emulator
mismatch or require cassette re-recording/regeneration.

Replay is deterministic by default:

- CI uses `collapsed` timing unless a test explicitly asks for `realtime` or
  `scaled` playback;
- environment is reconstructed only from the cassette allowlist;
- terminal size and resize events come from `manifest.terminal`;
- service-event assertions compare typed payloads after documented scrub rules,
  not raw secrets or machine-specific paths.

Realtime and scaled replay exist to validate the cassette replay scheduler:
given a recorded event sequence and timestamp resolution, the scheduler must
emit events in `seq` order, sleep according to recorded deltas within one
resolution tick for `realtime`, multiply deltas by the requested factor for
`scaled`, and avoid wall-clock sleeps in `collapsed` mode while preserving the
same logical `t_ms` positions for assertions.

## Automated Cassette Assertion Framework

All PTY/cassette acceptance tests must be automated. Manual inspection is useful
for debugging, but it is never a promotion gate for `supported` capability
status. The default `go test ./...` path must run replay-only tests that are
credential-free, provider-free, parallel-safe, and fast. Live record mode and
Docker conformance mode may be opt-in because they need binaries, credentials,
or containers, but when enabled they must still run without human keystrokes or
manual TUI observation.

The project will build a test-only cassette assertion framework on top of
`internal/pty/session`, `internal/pty/terminal`, and `internal/pty/cassette`.
That framework owns:

- scenario definitions that name the cassette, terminal size, replay mode,
  fixture driver, environment policy, expected artifacts, and expected
  `manifest.id`/`manifest.content_digest` values;
- time-coded assertions over frames, raw output chunks, input events, resize
  events, service events, final metadata, exit status, and timing gaps;
- a virtual clock for `collapsed` replay so time-coded tests run quickly while
  preserving recorded event order;
- `realtime` and `scaled` replay modes for tests that explicitly validate
  scheduler behavior;
- parallel fixture isolation: read-only cassette inputs, per-test temp dirs,
  per-test `HOME`/config roots for record mode, no global tmux/session state,
  unique artifact output paths, and deterministic cleanup;
- structured failure reports that include the failed assertion, nearest frame
  timestamps, relevant screen excerpts, and service-event context.

Assertion specs must bind to the cassette they were authored against by
`manifest.id` and content digest. CI must fail when an assertion file points to
a different cassette ID or digest, even if the current predicates happen to
pass.

Assertion specs must support at least these predicate families:

| Predicate Family | Examples |
|------------------|----------|
| Frame content | `at t_ms screen contains`, `within window eventually contains`, `never contains`, `stable_for`, normalized volatile text comparison |
| Terminal state | cursor position/visibility, rows/cols, alternate-screen state, style/color policy, scrollback or screen clear facts |
| Timing and buffering | output chunk ordering, maximum gap, minimum delay, delayed prompt arrival, split escape sequence handling, backpressure/large output completion |
| Input and resize | exact bytes sent, paste boundaries, control keys, signal events, resize order relative to output |
| Service events | typed-drain-compatible JSON shape, quota/model/reasoning/usage metadata, final status, warning presence or absence |
| Process lifecycle | exit code, signal, EOF, timeout, cancellation, no leaked child process evidence |

The initial scenario set is:

1. `top` through Docker conformance, with initial paint, refresh, input-driven
   change, and resize-driven layout change.
2. `claude` authenticated record mode plus replay cassettes for quota/status,
   model list, and reasoning levels.
3. `codex` authenticated record mode plus replay cassettes for quota/status,
   model list, and reasoning levels.

The framework must be extensible before those scenarios are marked complete.
Adding a new weird terminal case should require adding a scenario fixture and
assertion spec, not writing one-off sleeps or parser-specific test plumbing.
Required synthetic fixture families include partial ANSI/VT escape sequences,
one-byte chunking, alternate screen, cursor addressing, screen clears, SGR
style changes, OSC title, OSC 8 hyperlinks, OSC 52 clipboard writes, bell,
DECRQM/mode-query responses, line-drawing and alternate character sets,
bracketed paste, SGR mouse mode, focus-in/out, Unicode wide and combining
characters, resize during output, resize during an escape sequence, rapid
redraw/spinner frames, delayed output, no-newline prompts, PTY backpressure or
large buffered output, final output burst at process exit, EOF during redraw,
cancellation, and timeout. Sixel and image protocols are out of scope for the
first implementation unless a selected primary harness emits them; if observed,
they become explicit gap fixtures rather than silently ignored behavior.

## PTY Library Test Strategy

The PTY library is not complete until it proves useful behavior against real
terminal programs, not only fake sessions and happy-path harness probes. These
tests are layered on the automated cassette assertion framework above; they do
not rely on manual inspection, arbitrary sleeps, or terminal text scraping
outside the selected emulator.

| Test Class | Required Coverage |
|------------|-------------------|
| Unit and fake-session tests | Startup failure, normal exit, EOF, timeout, cancellation, process-group cleanup, large input, multiline paste boundaries, control keys, resize events, raw output capture, frame derivation, deterministic fake clock, replay ordering, and assertion-runner failure reporting. |
| Host PTY smoke tests | Portable Unix commands such as `sh`, `cat`, `stty size`, and `sleep` verify stdin/stdout, exit status, terminal sizing, cancellation, and no leaked child processes without credentials or network. Linux and macOS host smoke targets are required before primary PTY support is promoted. Windows support is an explicit gap until a Windows PTY adapter and fixtures are designed. |
| Docker TUI conformance tests | A pinned Linux container image supplies known TUI programs. The first required target is Unix `top`: capture several distinct screens from one run, including initial paint, later refresh frames, and at least one interaction or resize that changes the screen. Assertions are time-coded scenario predicates over rendered frames and service metadata, not brittle byte-for-byte full-screen output. |
| Terminal rendering tests | `internal/pty/terminal` must wrap a real VT/ANSI emulator. Tests must prove screen clears, cursor movement, SGR style policy, alternate-screen behavior where available, Unicode/wide characters, partial escape sequences, resize races, buffering/delay behavior, and volatile-content normalization. Regex ANSI stripping is not accepted as the screen model. |
| Additional TUI diversity | Add at least two more common terminal shapes before calling the library mature: a pager flow such as `less`, and an editor or curses-style full-screen flow such as `vim`, `nano`, or `dialog`, using Docker when host availability is inconsistent. Each new TUI shape must be a reusable scenario fixture. |
| Cassette replay tests | Record a deterministic synthetic terminal run, replay it through the cassette reader/player and assertion framework, and assert manifest fields, input ordering, raw output, frame snapshots, scrub report, final status, read-only replay behavior, and parallel replay safety. |
| Authenticated harness tests | Opt-in recorder tests drive Claude and Codex through the same PTY library to extract TUI-only quota/status, model listings, and reasoning levels. Per-run token usage is covered by native stream capability tests unless it becomes TUI-derived; then it must get its own checklist row and cassette scenario. Missing binary/auth/quota/timeout cases must fail before writing accepted cassettes. Replay cassettes for these flows must run in default CI without credentials. |

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
| Split a generic PTY cassette project now | Clean abstraction if multiple projects need it | Premature API freeze; no second consumer yet; slows harness support beads | Rejected for now by ADR-004; revisit at the documented extraction triggers |

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
| Cross-platform PTY behavior diverges | M | M | Require Linux and macOS host smoke tests before claiming primary PTY support; track Windows as an explicit unsupported gap until an OS-specific adapter and fixtures exist |

## Validation

| Success Metric | Review Trigger |
|----------------|----------------|
| A future cassette runner can record and replay one codex or claude run through the same direct PTY transport | Record and replay use different process/session supervisors |
| Time-coded cassette assertions run in collapsed mode quickly and in parallel for top, Claude, Codex, and synthetic edge fixtures | Tests rely on sleeps, manual inspection, or serial global state |
| PTY conformance tests capture useful multi-frame output from Unix `top` and at least two other terminal program shapes | The library is marked complete using only fake sessions or Claude/Codex probes |
| Linux and macOS host PTY smoke tests pass or report an explicit platform gap | Primary PTY support is claimed from Docker-only Linux evidence |
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
- [ADR-003 PTY Terminal Rendering and Screen Model](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-003-pty-terminal-rendering.md)
- [ADR-004 Terminal Harness Build-vs-Buy Boundary](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-004-terminal-harness-build-vs-buy.md)
- [SPIKE-001 Direct PTY Rendering With Unix Top](/Users/erik/Projects/agent/docs/helix/02-design/spikes/SPIKE-001-direct-pty-top-rendering.md)
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
