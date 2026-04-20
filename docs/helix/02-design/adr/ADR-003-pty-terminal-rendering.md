---
ddx:
  id: ADR-003
  depends_on:
    - ADR-002
    - CONTRACT-003
---
# ADR-003: PTY Terminal Rendering and Screen Model

| Date | Status | Deciders | Related | Confidence |
|------|--------|----------|---------|------------|
| 2026-04-20 | Accepted | DDX Agent maintainers | `ADR-002`, `SPIKE-001`, `CONTRACT-003` | Medium |

## Context

ADR-002 selects direct PTY ownership and versioned PTY cassettes. That decision
still leaves a hard implementation question: how does DDX Agent turn raw ANSI
PTY output from real TUIs into stable screen frames for assertions, replay, and
inspection?

`top` was spiked through a direct PTY in
[SPIKE-001](/Users/erik/Projects/agent/docs/helix/02-design/spikes/SPIKE-001-direct-pty-top-rendering.md).
The spike successfully started `top`, sent input, resized the PTY, captured raw
bytes, and rendered useful frames with a VT emulator. It also showed that raw
output contains dense ANSI mode changes, cursor motion, screen clears, SGR
styling, and volatile terminal content. Regex stripping is not a viable screen
model.

## Decision

DDX Agent will implement `internal/pty/terminal` as a wrapper around a real
VT/ANSI terminal emulator library. The project will not hand-roll ANSI parsing
or rely on regex stripping for TUI assertions.

`internal/pty/session` owns the PTY process and raw byte stream.
`internal/pty/terminal` consumes raw bytes and produces normalized screen
snapshots, frame diffs, cursor state, terminal size, and semantic extraction
helpers. `internal/pty/cassette` stores both the raw evidence stream and the
derived frame stream.

The emulator backend is intentionally hidden behind an internal interface so it
can be replaced if conformance tests expose gaps.

**Key Points**: real terminal emulator | raw bytes preserved | frames derived |
backend replaceable

## Terminal Model Contract

The terminal layer must expose:

- byte ingestion that preserves order from the PTY reader;
- current screen snapshot as cells or lines;
- frame snapshots or diffs with monotonic `t_ms`;
- cursor position and visibility;
- terminal size and resize handling;
- style metadata policy: either preserve color/style in cells or explicitly
  document what is dropped;
- semantic text extraction for harness probes;
- normalization hooks for volatile screen facts such as clocks, PIDs, elapsed
  durations, animation counters, and process ordering.

The terminal layer must not:

- spawn processes;
- write cassettes directly;
- know Claude, Codex, quota, model, reasoning, or token-usage semantics;
- import `internal/harnesses`.

## Library Selection

The first implementation bead must evaluate terminal emulator candidates before
locking one in. The spike proves `github.com/hinshun/vt10x` can render `top`
well enough for a first pass, but candidate evaluation should also consider
maintainability, Unicode/wide-character support, alternate screen behavior,
resize behavior, OSC/title handling, color/style support, API fit, and test
coverage.

Candidate families include:

- `github.com/hinshun/vt10x`
- terminal model pieces used by `go-expect`
- Charmbracelet/x ANSI tooling
- other small maintained VT parser/emulator libraries with a compatible API

## Conformance Tests

The PTY terminal model is not complete until tests prove behavior against real
terminal programs:

| Target | Required Evidence |
|--------|-------------------|
| `top` | Capture multiple rendered frames from one run, including initial paint, refresh, input-driven state change, and resize-driven layout change. Assertions check semantic screen facts, not full raw byte equality. |
| Pager | A `less`-style flow proves scroll, quit, and alternate-screen or raw-mode behavior where available. |
| Full-screen TUI | An editor/curses-style flow such as `vim`, `nano`, or `dialog` proves cursor movement, screen redraw, and key handling. |
| Synthetic fixtures | Deterministic ANSI fixtures cover Unicode/wide characters, style policy, cursor movement, clear-screen, scroll regions, resize races, and malformed/partial escape sequences. |

Linux and macOS host smoke tests are required before promoting primary PTY
support. Docker Linux conformance is useful but cannot prove host-specific PTY
semantics. Windows remains out of scope until a Windows PTY adapter and fixtures
are designed.

## Consequences

| Type | Impact |
|------|--------|
| Positive | Harness probes can assert against rendered screens instead of brittle raw ANSI output. |
| Positive | Cassettes preserve raw evidence while also carrying human-reviewable frames. |
| Positive | The emulator backend can be swapped without rewriting harness adapters. |
| Negative | DDX Agent inherits terminal-emulator edge cases and must maintain a conformance suite. |
| Negative | Terminal rendering is more work than PTY process control alone. |

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| Emulator library mishandles a real harness TUI | M | H | Keep backend behind `internal/pty/terminal`; require real TUI conformance fixtures before support claims |
| Tests become flaky due to volatile TUI content | H | M | Separate semantic normalization from secret scrubbing and assert stable screen facts |
| Unicode or style handling loses meaningful UI state | M | M | Add synthetic wide-character/style fixtures and document style preservation policy |
| Raw and rendered evidence diverge | M | M | Store `output.raw` as authoritative evidence and derive frames through deterministic replay tests |

## Validation

| Success Metric | Review Trigger |
|----------------|----------------|
| `top` spike behavior is reproduced in automated conformance tests | `top` can only be inspected manually |
| Terminal model handles raw byte streams, resize, input-driven redraw, and volatile normalization | Harness probes parse regex-stripped ANSI text |
| `output.raw` and `frames.jsonl` are both generated from the same PTY stream | Cassette contains frames without raw evidence |
| Terminal backend can be replaced behind one interface | Harness adapters import a concrete emulator package |

## References

- [ADR-002 PTY Cassette Transport](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-002-pty-cassette-transport.md)
- [SPIKE-001 Direct PTY Rendering With Unix Top](/Users/erik/Projects/agent/docs/helix/02-design/spikes/SPIKE-001-direct-pty-top-rendering.md)
- [CONTRACT-003 DdxAgent Service Interface](/Users/erik/Projects/agent/docs/helix/02-design/contracts/CONTRACT-003-ddx-agent-service.md)

## Review Checklist

- [x] Context names a specific problem
- [x] Decision statement is actionable
- [x] Alternatives are represented by library-selection criteria
- [x] Consequences include positive and negative impacts
- [x] Risks have mitigations
- [x] Validation section defines review triggers
