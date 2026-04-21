---
ddx:
  id: ADR-004
  depends_on:
    - ADR-002
    - ADR-003
    - CONTRACT-003
---
# ADR-004: Terminal Harness Build-vs-Buy Boundary

| Date | Status | Deciders | Related | Confidence |
|------|--------|----------|---------|------------|
| 2026-04-20 | Accepted | DDX Agent maintainers | `ADR-002`, `ADR-003`, `CONTRACT-003` | Medium |

## Context

ADR-002 selected direct PTY transport for harness execution, quota probes,
model-list probes, and cassette record/replay. ADR-003 requires a real
terminal emulator for screen frames. Together, those decisions are close to the
boundary of a terminal product: PTY lifecycle, ANSI/VT rendering, input
impersonation, timed recording, playback, inspection, and golden-master
assertions.

That is build-vs-buy territory. DDX Agent must not accidentally become a
general terminal emulator, terminal multiplexer, or IDE terminal. The only
reason to own new library code is the narrow DDX requirement that primary
harnesses produce authenticated, replayable, scrubbed evidence with raw PTY
bytes, rendered frames, timed input, opaque service events, final metadata,
quota data, and deterministic replay.

## Current Buy-Side Evidence

| Candidate | Buyable Capability | Missing for DDX Agent |
|-----------|--------------------|------------------------|
| `creack/pty` | Go PTY primitive for starting commands, attaching stdin/stdout/stderr to a pseudoterminal, sizing, resize forwarding, and Unix-style lifecycle control. | Higher-level process policy, screen model, cassettes, service events, authenticated harness preflight, scrub/normalization policy. |
| `Netflix/go-expect` | Expect-style input/output automation over a pseudoterminal. | Does not spawn or manage process lifecycle; does not define rendered frame evidence, cassette artifacts, or DDX service events. |
| `script` | Ubiquitous terminal session recording with timing support on util-linux systems. | Recorder only; wrapper text/noise; no reliable harness control, DDX service events, scrub reports, model/quota parsing, or cross-platform library boundary. |
| `asciinema` / asciicast v3 | Mature terminal recording and playback concepts, newline-delimited JSON, timed output/input/resize/exit events, local playback, speed controls, and raw output preservation. | Not a harness runner, not a Go library boundary, no DDX service-event stream, no quota/model/reasoning preflight, no accepted-vs-diagnostic cassette policy. |
| `tmux` | Mature terminal multiplexer with sessions, attachability, pane capture, key injection, pipe-pane streaming, and a useful human inspection story. | Global server/socket/session state, split-brain/stale-session cleanup, pane-index instability, command hangs, paste/send quirks, and no DDX cassette/service-event contract. |
| `ntm` / Gas Town | Current tmux-based multi-agent control planes with real operational patterns: socket isolation, session registries, cleanup, timeouts, circuit breakers, paste-buffer paths, capture helpers, and quota/status probing. | They validate the complexity of tmux ownership; adopting their shape would make tmux semantics part of DDX Agent's core capability story. |
| Charmbracelet `vhs` | Declarative terminal scripting, typed input, waits, generated GIF/video/PNG frames, tape recording, and dependency checks for documentation and demos. | Outputs visual media and tape scripts, not DDX evidence cassettes; depends on external rendering stack; not a live authenticated harness recorder with service-event replay. |
| `xterm.js` / serialize addon | Widely used terminal emulator and buffer serialization for browser/UI use. | JavaScript/browser dependency, experimental serialization addon, not a Go PTY harness layer, no DDX cassette/service-event contract. |
| JetBrains `JediTerm` / IntelliJ terminal | Mature Java terminal emulator used by IDE terminals with local PTY support and xterm/VT100 behavior. | Java UI stack and IDE product scope; no Go cassette/service-event library; illustrates the scale DDX Agent must not rebuild. |

The buyable pieces are real and should be used. The gap is not "terminal
emulation exists"; the gap is a small, testable Go orchestration/cassette layer
that combines PTY lifecycle, an adopted terminal renderer, and DDX-specific
evidence streams.

## Gate Outcome

This ADR closes the build-vs-buy gate for the first implementation pass:

| Area | Decision | Rationale |
|------|----------|-----------|
| PTY lifecycle | Buy/adopt `creack/pty` or an equivalent maintained Go PTY primitive. | SPIKE-001 proved direct process control, reads, writes, resize, and exit against `top` without tmux. Writing platform PTY code is out of scope. |
| Terminal rendering | Buy/adopt a maintained VT/ANSI emulator behind `internal/pty/terminal`; reaffirm SPIKE-001's `vt10x` proof as the starting stack unless the implementation bead records an equal or better top-spike pass for a substitute. | Raw ANSI is not assertable. The spike rendered useful `top` frames through an emulator. The backend remains replaceable and version-pinned in cassettes. |
| Input automation | Build small DDX helpers over the PTY session, borrowing expect-style ideas where useful. | `go-expect` is a useful pattern but not the lifecycle, cassette, or service-event owner. |
| Recording format | Build the DDX cassette schema, reusing asciicast event/timing ideas. | Existing recorders do not carry DDX service events, quota evidence, scrub reports, assertion binding, or accepted-vs-diagnostic policy. |
| Replay and assertions | Build a test-only cassette assertion layer under `internal/ptytest` or equivalent. | DDX needs collapsed virtual-clock replay, service-event assertions, parallel-safe fixture isolation, and harness capability evidence. |
| tmux operator UX | Exclude from the baseline entirely. No tmux adapter, tmux shell-outs, tmux sockets, or tmux-backed capability promotion in the current plan. | SPIKE-002 showed tmux's human attachability is useful, but also reproduced stale socket cleanup and pane-targeting costs. NTM and Gas Town confirm those costs become substantial. If this tradeoff changes later, it requires a new ADR. |
| Project boundary | Keep the first pass internal under `internal/pty` and `internal/ptytest`. | There is no second consumer yet. Extraction triggers are documented below and should be revisited after one working Claude/Codex cassette path. |
| Non-TUI modes | Do not let `claude --print`, `codex exec`, or similar non-TUI paths promote TUI capability support. | They may later share cassette ideas for stream evidence, but primary harness model/quota/status parity is governed by the direct PTY/TUI evidence path. |

The selected first-pass stack is therefore: direct Go PTY process control,
wrapped VT/ANSI emulator rendering, DDX-owned event-driven cassettes, and
DDX-owned replay/assertion glue. SPIKE-001 is the accepted end-to-end proof for
the initial `creack/pty` plus `vt10x` stack against Unix `top`; changing either
core dependency before implementation must reproduce the same top scenario and
record that evidence before `agent-949a5ba4` starts.

SPIKE-002 pressure-tested the real primary-harness goal against `script`,
asciinema, tmux, NTM, Gas Town, Claude, and Codex. It proved that Claude and
Codex expose the required quota/status/model-list/reasoning surfaces through
their TUIs and that tmux can drive them, but it also confirmed that tmux's
session/socket state is an operational concern rather than a free abstraction.
Per-run token usage remains required, but it should stay on native stream or
batch JSON evidence unless it becomes TUI-derived. Therefore tmux is explicitly
out of the baseline. Direct PTY replay is the only accepted evidence path for
primary harness TUI-only capability support.

## Decision

DDX Agent will not implement a terminal emulator from scratch, a terminal UI,
an IDE terminal, or a tmux-like multiplexer.

The PTY library work is limited to orchestration and evidence:

- adopt a PTY primitive instead of writing platform PTY code when a maintained
  dependency fits;
- adopt or wrap a maintained VT/ANSI terminal emulator instead of hand-rolling
  ANSI parsing;
- reuse asciinema/asciicast timing and event ideas where they fit, but keep a
  DDX cassette schema when DDX service events, quota data, scrub reports, and
  deterministic replay require fields that asciicast does not own;
- build only narrow glue for session supervision, typed input helpers, rendered
  frame derivation, cassette record/playback, scrub/normalization, and
  CONTRACT-003 service-event evidence.

Before `agent-949a5ba4` starts implementation, the project must complete a
build-vs-buy evaluation bead that compares concrete dependencies for PTY
lifecycle, terminal rendering, event-driven recording/playback, replay timing,
automated time-coded assertion support, licensing, maintenance health, and API
fit. The evaluation must explicitly decide whether the first implementation
remains under `internal/pty` or is split out immediately.

The selected dependency stack must run the SPIKE-001 `top` scenario end to end
or explicitly reaffirm the SPIKE-001 stack without substitution. A paper matrix
alone is not sufficient to unblock implementation.

The default is to keep the first implementation under `internal/pty` with
separable package boundaries. Split it into a standalone project only when one
of these triggers occurs:

- a second repo needs the same cassette/PTY API;
- the generic PTY/cassette code exceeds the harness-specific code in size or
  release cadence;
- the package needs its own compatibility matrix, fixtures, or versioned public
  API independent of DDX Agent;
- adoption of a third-party recorder/emulator requires adapter work better
  maintained outside DDX Agent;
- build or release constraints make terminal dependencies a burden for ordinary
  agent library consumers.

The first production shape is intentionally smaller than a terminal
application: a background metadata probe that starts a harness briefly, extracts
quota/status/model/reasoning facts, records a scrubbed snapshot or cassette when
requested, and exits cleanly. Normal prompt execution continues to use
harness-native batch modes where those modes satisfy DDX requirements. The PTY
wrapper becomes the fallback execution path only if a harness's batch mode stops
working for DDX.

## Non-Goals

- Do not build a replacement for `top`, `htop`, tmux, terminal panes, or an IDE
  terminal.
- Do not build a general terminal UI or screen inspector beyond read-only
  evidence viewing needed for cassette review.
- Do not build a general terminal recording format unless the evaluation proves
  asciicast-style formats cannot carry the required DDX evidence as extensions
  or sidecar streams.
- Do not expose a public PTY API before the internal harness use cases prove the
  boundary.

## Consequences

| Type | Impact |
|------|--------|
| Positive | The implementation effort narrows to DDX-specific evidence instead of terminal-emulator ownership. |
| Positive | Candidate dependency evaluation becomes a blocking prerequisite, reducing accidental NIH work. |
| Positive | The selected dependencies must fit the automated cassette assertion strategy, not only live terminal display. |
| Positive | Extraction is designed in, but delayed until the API is proven by real harnesses. |
| Negative | The PTY/cassette work cannot start as a coding bead until the buy-vs-build review completes. |
| Negative | The project may still need to own a small cassette schema if existing recorders cannot carry service events and quota evidence cleanly. |

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| The team reimplements terminal behavior because no dependency is perfect | M | H | ADR-003 requires a wrapped emulator; ADR-004 blocks implementation on explicit dependency evaluation |
| A generic PTY project emerges inside `agent` by accumulation | M | H | Track extraction triggers and review package size/API pressure before each PTY milestone |
| Existing recorders appear close enough but cannot support service-event evidence | M | M | Evaluate sidecar streams and asciicast compatibility before inventing a new format |
| Build dependencies become too heavy for normal library users | M | M | Keep terminal code under `internal/pty`, isolate imports, and split when dependency burden becomes visible |

## Validation

| Success Metric | Review Trigger |
|----------------|----------------|
| `agent-949a5ba4` cites a completed build-vs-buy matrix before choosing dependencies | Implementation imports a terminal/PTY dependency without documented comparison |
| Selected dependencies run the SPIKE-001 `top` scenario end to end | Build-vs-buy approves a stack that has not handled a real full-screen TUI |
| Dependency choices support fast replay and time-coded assertion tests without live credentials | The test plan depends on manual TUI inspection or wall-clock sleeps |
| PTY code wraps adopted PTY/emulator dependencies behind package interfaces | Raw ANSI parsing or platform PTY code appears without a rejection rationale |
| Cassette design reuses existing recording concepts where possible and explains every DDX-specific extension | New recording schema duplicates asciicast without service-event justification |
| Project split is reconsidered after one working Claude/Codex cassette and before any public API promise | `internal/pty` grows into a reusable library with no extraction decision |

## References

- [ADR-002 PTY Cassette Transport](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-002-pty-cassette-transport.md)
- [ADR-003 PTY Terminal Rendering and Screen Model](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-003-pty-terminal-rendering.md)
- [SPIKE-001 Direct PTY Rendering With Unix Top](/Users/erik/Projects/agent/docs/helix/02-design/spikes/SPIKE-001-direct-pty-top-rendering.md)
- [SPIKE-002 Terminal Driver and Recorder Alternatives](/Users/erik/Projects/agent/docs/helix/02-design/spikes/SPIKE-002-terminal-driver-recorder-alternatives.md)
- [creack/pty](https://github.com/creack/pty)
- [Netflix/go-expect](https://github.com/Netflix/go-expect)
- [asciicast v3 specification](https://docs.asciinema.org/manual/asciicast/v3/)
- [asciinema CLI usage docs](https://docs.asciinema.org/manual/cli/usage/)
- [tmux](https://github.com/tmux/tmux)
- [NTM](https://pkg.go.dev/github.com/Dicklesworthstone/ntm)
- [Gas Town](https://github.com/gastownhall/gastown)
- [Charmbracelet VHS](https://github.com/charmbracelet/vhs)
- [Charmbracelet/x terminal packages](https://github.com/charmbracelet/x)
- [xterm.js serialize addon](https://github.com/xtermjs/xterm.js/tree/master/addons/addon-serialize)
- [JetBrains JediTerm](https://github.com/JetBrains/jediterm)
- [JetBrains Terminal architecture note](https://blog.jetbrains.com/idea/2025/04/jetbrains-terminal-a-new-architecture/)

## Review Checklist

- [x] Context names a specific problem
- [x] Decision statement is actionable
- [x] At least two alternatives were evaluated
- [x] Each alternative has concrete limits for DDX Agent
- [x] Selected boundary explains why it wins
- [x] Consequences include positive and negative impacts
- [x] Negative consequences have mitigations
- [x] Risks are specific with probability and impact assessments
- [x] Validation section defines review triggers
- [x] Concern impact is complete
