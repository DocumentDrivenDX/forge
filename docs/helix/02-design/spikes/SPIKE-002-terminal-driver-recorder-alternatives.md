# SPIKE-002: Terminal Driver and Recorder Alternatives

| Date | Status | Related |
|------|--------|---------|
| 2026-04-20 | Completed | `ADR-002`, `ADR-004`, `CONTRACT-003` |

## Question

Can DDX Agent satisfy the real primary-harness goals without overbuilding a
terminal product?

The pass/fail criteria for this spike are deliberately narrow:

1. DDX Agent must be able to control Claude and Codex as terminal users and
   extract quota/status, model lists, reasoning levels, and adjacent facts that
   the CLIs expose only through their TUIs.
2. DDX Agent must be able to replay captured terminal evidence so client-side
   parsers, terminal rendering, and capability assertions can run in unit tests
   without installed, authenticated, or functional Claude/Codex binaries.

## Setup

Throwaway evidence was written outside the repo under
`/tmp/agent-pty-buy-spike`. No raw authenticated captures are committed.

Tested local tools:

| Tool | Version / Shape | Used For |
|------|-----------------|----------|
| `script` | util-linux `script` with advanced timing mode | Recorder-only baseline |
| `asciinema` | `2.4.0` asciicast v2 recorder/player | Timed output recording and raw replay baseline |
| `tmux` | `3.6a` | User-impersonation, inspectable sessions, pipe/capture baseline |
| `claude` | local authenticated CLI | Bounded TUI status and model-list probe |
| `codex` | `0.121.0` local authenticated CLI | Bounded TUI status and model-list probe |
| `top` | system TUI | Non-secret full-screen TUI target |

Current external/current-state references inspected:

- `ntm` (`github.com/Dicklesworthstone/ntm`) source checkout under `/tmp`.
- `gastown` (`github.com/gastownhall/gastown`) source checkout under `/tmp`.
- asciinema docs on PTY-backed raw output plus timing.
- tmux command surface: `new-session`, `send-keys`, `paste-buffer`,
  `pipe-pane`, `capture-pane`, `kill-session`.
- Charmbracelet/x current package list, including `vt`, `xpty`, and `ansi`.

## Results

### Recorder-Only Tools

`script` successfully recorded `top -n 2 -d 0.2` with output and timing files:

```text
script-top.io    4341 bytes
script-top.time   406 bytes
```

Its advanced timing stream contains output records such as `O <delta> <bytes>`,
but the data file also includes `script` wrapper text. A second attempt to pipe
`q` into `script ... top -d 5` did not reliably control `top`; it left a live
`top`/`script` process until explicitly killed. This is negative evidence:
recorder stdin is not a harness controller.

`asciinema rec --stdin --cols 80 --rows 24 -c 'top -n 2 -d 0.2'` wrote a
7217-byte asciicast. `asciinema play --out-fmt raw` replayed it into a 4550-byte
raw stream containing the expected `top`, `Tasks:`, `%Cpu`, memory, and command
rows. This proves timed output replay is mature and useful, but not sufficient:
asciinema records terminal sessions; it does not own Claude/Codex command
sequencing, service events, scrub reports, or capability parsing.

### tmux Diagnostic Spike

A bounded `top` run through an isolated tmux socket succeeded when targeting the
returned pane id rather than assuming a window index:

```text
tmux-top.pipe      3117 bytes
tmux-top.capture   1891 bytes
```

`pipe-pane` captured raw terminal output, `capture-pane -e -p` captured the
rendered pane with escape/style information, and `send-keys q` exited `top`.

The spike also created two stale socket files during failed attempts. That is
important design evidence. tmux brings a real side benefit: a human can attach
to or inspect a live harness session when the harness uses an advertised socket
and session name. It also brings global-state costs:

- session names collide unless generated and validated;
- default vs custom socket confusion can create split-brain state;
- stale sockets and dead sessions need reaping;
- pane/window indexes are user-configurable, so code must target pane ids;
- tmux server hangs need command timeouts and a circuit breaker;
- every key injection must account for literal send vs paste-buffer semantics;
- record and replay semantics are still DDX-owned.

The final baseline excludes tmux entirely. The spike remains useful as negative
evidence and as a record of the human-inspection tradeoff, but no tmux adapter
is part of the current implementation plan. If a future operator UI needs tmux
attachability, that must be a new design decision outside the core probe and
cassette path.

### NTM and Gas Town Prior Art

NTM is useful because it shows the "grown-up tmux wrapper" requirements, not
because DDX Agent should copy its scope. The checkout includes:

- command timeouts and a tmux circuit breaker;
- strict session-name validation;
- pane discovery, capture budgets, and health-oriented capture helpers;
- `pipe-pane` streaming with polling fallback;
- paste-buffer based multiline/large prompt injection;
- quota probing by sending slash commands to panes.

Gas Town shows similar lessons from another mature tmux-centered system:

- per-town socket naming and socket split-brain checks;
- default-socket vs town-socket routing;
- session registry and stale/zombie cleanup;
- tests around socket isolation and session lifecycle.

These projects confirm that tmux can support human inspection and multi-agent
orchestration, but only with substantial registry, socket, lifecycle, and
reconciliation code. For DDX Agent, that is a warning against allowing tmux
semantics to leak into the core cassette library.

### Claude TUI Probe

The Claude probe used an isolated tmux socket only as the temporary spike driver.
It started `claude`, sent `/status`, then sent `/model`, and captured a scrubbed
screen.

Observed status facts:

```text
Status   Config   Usage   Stats
Login method: Claude Max account
Model: Default Opus 4.7 with 1M context
```

Observed model/reasoning picker facts:

```text
Select model
1. Default (recommended)  Opus 4.7 with 1M context
2. Sonnet                 Sonnet 4.6
3. Haiku                  Haiku 4.5
xHigh effort (default) <- -> to adjust
```

Result: the capability surface exists and is extractable from the rendered TUI.
The direct PTY implementation needs equivalent control, rendered frames, and
screen parsing without depending on tmux.

### Codex TUI Probe

The Codex probe used `codex --no-alt-screen`, an isolated tmux socket, `/status`,
and `/model`. The model picker required a slash-command completion sequence:
type `/model`, press `Tab`, then `Enter`.

Observed status/quota facts:

```text
Model: gpt-5.4 (reasoning high, summaries auto)
Account: [scrubbed] (Pro)
5h limit: 95% left
Weekly limit: 99% left
GPT-5.3-Codex-Spark limit: 100% left
```

Observed model list:

```text
Select Model and Effort
1. gpt-5.4 (current)
2. gpt-5.2-codex
3. gpt-5.1-codex-max
4. gpt-5.4-mini
5. gpt-5.3-codex
6. gpt-5.3-codex-spark
7. gpt-5.2
8. gpt-5.1-codex-mini
Press enter to select reasoning effort, or esc to dismiss.
```

Result: quota and model list extraction are tractable, but timing matters.
The harness adapter must wait on rendered screen predicates, not fixed sleeps.

## Decision Pressure

The spike changes the build-vs-buy recommendation in one way: tmux should not
be described only as "noise." It has one real product benefit: operator
inspectability while an authenticated TUI is live. However, the costs are also
real and recurring. NTM and Gas Town both had to build safety rails around
socket naming, session registries, stale cleanup, command timeouts, pane-target
identity, paste semantics, and split-brain detection. For the finalized plan,
that cost is not justified.

The core DDX success criteria still point at a direct PTY cassette library:

- direct PTY gives DDX the exact raw byte stream, input stream, resize stream,
  process lifecycle, and replay contract needed for deterministic tests;
- terminal rendering must be backed by a real emulator and asserted through
  time-coded screen predicates;
- record mode must fail fast on missing auth/quota/model surfaces;
- replay mode must run without Claude/Codex binaries, credentials, or network;
- service events and scrub reports are DDX-specific and do not fit cleanly in
  tmux, `script`, or asciinema alone.

## Finalized Follow-Up Requirements

1. tmux is off the baseline entirely. Do not build a tmux adapter, do not shell
   out to tmux from harness probes, and do not use tmux evidence to promote a
   capability.
2. `internal/pty/cassette` should reuse asciicast-style event timing ideas, but
   keep DDX-owned sidecar streams for input, frames, service events, quota, and
   scrub reports.
3. The first production use is a small background probe process. It starts a
   harness briefly, extracts current quota/status/model/reasoning facts, writes
   a scrubbed cache/snapshot, and exits cleanly.
4. Batch execution remains preferred for normal prompt runs where the harness
   supports it, such as `claude -p` or the existing Codex execution path. The
   PTY wrapper is the control path for TUI-only metadata and the fallback path
   if a harness's batch mode stops meeting DDX requirements.
5. Add minimal debug commands around the direct PTY stack so a developer can
   dump current VT state, raw byte offsets, recent input/output events, and the
   latest derived frame when a probe parser fails. These commands are diagnostic
   only; they are not a user-facing terminal UI.
6. Claude and Codex harness adapters must encode slash-command flows as
   predicate-driven interactions:
   - Claude: `/status`, `/model`, model-row extraction, effort extraction.
   - Codex: `/status`, `/model` + completion sequence, quota-window extraction,
     model-row extraction, reasoning-effort follow-up.
7. All supported harnesses must be classified in the harness-golden TUI-only
   checklist. Rows that have TUI-only requirements must have matching
   record/playback scenarios; rows with no TUI-only requirements must point to
   their CLI/API evidence instead of disappearing from the plan.
8. Authenticated raw captures must remain local/transient until the scrubber and
   cassette acceptance gate are implemented.

## Confidence

Medium-high for tractability. The two real success criteria are achievable:
the primary TUI surfaces exist, and terminal output replay is established
technology. The remaining risk is implementation discipline: without strict
module boundaries and cassette-first tests, a tmux-style control plane can
easily grow into the core architecture.
