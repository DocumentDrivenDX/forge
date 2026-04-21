# Harness Golden-Master Integration

## Replay Mode

Replay mode is the default integration path and does not require live
credentials. It runs checked-in cassette-shaped fixtures through
`Service.Execute` and replays versioned service-event cassettes from
`testdata/harness-cassettes/`. The suite verifies event order, progress text,
normalized final text, usage when exposed by the harness stream, routing
metadata, request metadata, tool events, failure status, and quota/status cache
projection.

Run:

```sh
go test -tags=integration ./...
```

The replay suite distinguishes itself from live harness evidence: POSIX fixture
scripts emulate recorded harness stdout and are not counted as proof that a live
Claude Code, Codex, Pi, opencode, or Gemini account is usable. Versioned
service-event cassettes are deterministic parser/typed-drain evidence; live
record mode must regenerate the same artifact shape before a capability is
claimed as live-authenticated.

## Record Mode

Record mode is opt-in and must fail fast before writing evidence when a required
binary, auth state, subscription/quota path, or transport dependency is absent.

The finalized first live path is a short-lived background probe, not a
long-running terminal manager. The probe starts one harness, drives the minimum
TUI flow needed to read quota/status/model/reasoning facts, emits a scrubbed
snapshot/cache and optional cassette, and exits. It is intended to run
periodically or on demand so routing can make decisions from current
subscription state.

Normal prompt execution should keep using harness-native batch modes when those
modes work, such as Claude print mode or the existing Codex execution path. The
PTY wrapper is the fallback for prompt execution only if a harness batch mode no
longer provides the behavior DDX needs.

Run the preflight:

```sh
AGENT_HARNESS_RECORD=1 go test -tags=integration -run TestHarnessGoldenRecordModePreflight .
```

Run live record mode and write sanitized version-1 cassette directories:

```sh
AGENT_HARNESS_RECORD=1 \
AGENT_HARNESS_CASSETTE_DIR=./testdata/harness-cassettes/live \
go test -tags=integration -run TestHarnessGoldenRecordModeLive .
```

Run authenticated usage-specific record mode:

```sh
AGENT_HARNESS_RECORD=1 \
AGENT_HARNESS_CASSETTE_DIR=./testdata/harness-cassettes/live \
go test -tags=integration -run usage .
```

Usage record mode serializes per harness account with cassette record locks,
requires the Claude/Codex binaries to be present, and refuses to write accepted
artifacts unless the run succeeds and final metadata contains fresh
`native_stream` token usage. Auth, quota, transport, or account-lock failures
therefore fail before artifact acceptance.

Each cassette directory contains:

- `manifest.json` — version, harness, scrubbed command policy, timeout, and
  permission mode, plus terminal and timing metadata. Version `1` uses
  `manifest.timing.resolution_ms: 100` by default, but recorders may choose a
  finer resolution without changing the version.
- `input.jsonl` — timed prompt/input, paste, control-key, resize, and signal
  events with monotonic `t_ms` timestamps.
- `output.raw` — scrubbed raw PTY output bytes.
- `frames.jsonl` — timed screen snapshots or frame diffs with monotonic `t_ms`
  timestamps.
- `service-events.jsonl` — timed opaque service-event JSON consumed by
  deterministic replay tests above the cassette library.
- `final.json` — normalized final event payload.
- `quota.json` — quota/status evidence when applicable.
- `scrub-report.json` — redaction and safety report.

Required live harness binaries for this bead:

- `claude`
- `codex`

Quota and model-list preflight must move to the direct PTY transport selected in
ADR-002. Existing tmux capture helpers are legacy diagnostics only and must not
be used by new probe code. Replay mode remains independent of live credentials.
Claude and Codex quota/status probes now use direct PTY transport for supported
health refreshes, write accepted cassettes only after quota parsing succeeds,
and keep tmux helpers as diagnostic-only fallbacks.

Secondary harnesses such as `gemini`, `opencode`, and `pi` follow the same
classification rule, but only their TUI-only capabilities require PTY
record/playback tests. If a secondary harness exposes the needed capability via
a stable CLI/API path, its non-PTY capability test owns that evidence.

## Secondary Harness Promotion Status

`opencode` and `pi` are promoted for automatic routing because their required
non-PTY capability surfaces are covered by package and service tests:

| Harness | Auto-routing status | Evidence |
|---|---|---|
| `opencode` | eligible | `internal/harnesses/opencode/runner_test.go` covers prompt delivery, `--dir`, `-m`, `--variant`, permission no-op semantics, final text, token totals, stderr/exit mapping, and request timeout cleanup. `internal/harnesses/opencode/model_discovery_test.go` covers `opencode models` parsing and compatibility-table fallback metadata. `service_models_test.go`, `service_test.go`, `service_route_attempts_test.go`, and `internal/routing/engine_test.go` cover model listing, capability metadata, and automatic-routing eligibility. |
| `pi` | eligible | `internal/harnesses/pi/runner_test.go` covers prompt delivery, subprocess workdir, `--model`, `--thinking`, permission unsupported semantics, final text, token totals, stderr/exit mapping, and request timeout cleanup. `internal/harnesses/pi/model_discovery_test.go` covers `pi --help`, `pi --list-models`, and compatibility-table fallback metadata. `service_models_test.go`, `service_test.go`, `service_route_attempts_test.go`, and `internal/routing/engine_test.go` cover model listing, capability metadata, and automatic-routing eligibility. |
| `gemini` | explicit-only | Package tests now cover `-m`, workdir, stdin delivery, unsupported reasoning/permission metadata, token totals from stats blocks, and text-based model extraction from supplied output. Promotion remains blocked on verified headless invocation, live or recorded CLI usage output, default model discovery, permission/approval-mode mapping, quota/account/auth status behavior, and profile/catalog routing evidence. |

Before Claude/Codex authenticated cassettes can promote capability rows, the
underlying PTY library must pass its own conformance suite. That suite starts
with Unix `top` in a pinned Docker environment and must capture several useful
screens from one run: initial paint, later refresh frames, and at least one
interaction or resize that visibly changes the frame stream. The PTY suite also
needs ordinary Unix process tests and at least two additional TUI shapes, such
as a pager and editor/curses-style program. Harness probes then layer on top of
that library to test quota/status extraction, model listings, reasoning levels,
and failure normalization for Claude and Codex. Token usage remains a core
capability, but it is only part of the PTY checklist when the source becomes
TUI-derived.

## PTY Docker Conformance

The direct PTY library has an opt-in Docker conformance target for real Linux
TUIs. It builds a local image from the pinned base
`debian:bookworm-slim@sha256:4724b8cc51e33e398f0e2e15e18d5ec2851ff0c2280647e1310bc1642182655d`
and installs `procps`, `less`, and `vim-tiny` inside that image. The test then
starts `docker run -it` through `internal/pty/session`, records version-1
cassettes with `internal/pty/cassette`, derives frames through
`internal/pty/terminal`, and replays the result with the
`internal/ptytest` scenario assertion framework.

Run the Docker gate only in environments where Docker and package-network
access are expected:

```sh
AGENT_PTY_INTEGRATION=1 go test -tags=integration ./internal/pty/... ./internal/ptytest/...
```

Default CI and local development remain replay-only and Docker-free:

```sh
go test ./...
```

The Docker suite currently covers `top`, `less`, and `vim.tiny` with reusable
fixtures under `internal/ptytest/testdata/docker-conformance/`. `top`
assertions are scenario predicates over rendered frames and cassette service
metadata: initial paint, later refresh, help-screen input, resize, raw output
ordering, input bytes, final metadata, and artifact presence. The pager and
editor flows prove scrolling, quit/input handling, full-screen redraw, inserted
text, cursor-bearing frame streams, and deterministic cassette replay.

Host-only subsets are explicitly insufficient for closing Docker conformance:
they exercise the local PTY implementation, but they do not prove the pinned
Linux TUI target or the Docker promotion gate. Conversely, Docker conformance
does not replace the required Linux/macOS host smoke tests because Docker only
proves the Linux container path.

Debuggability is required but deliberately small: the PTY stack should expose
commands or equivalent test helpers that dump the current rendered VT screen,
cursor metadata, terminal size, recent raw byte offsets, and recent timed
input/output events. These snapshots are for diagnosing parser failures; they
are not an interactive terminal UI and do not imply tmux attachability.

## TUI-Only Capability Checklist

Every TUI-only capability below must become a recordable test scenario. Feature
complete means every row marked `record/playback required` passes in both:

- record mode against a live authenticated harness, producing a scrubbed
  accepted cassette or failing before artifact acceptance with a normalized
  blocker;
- playback mode from that cassette, without the harness binary, credentials, or
  network, using collapsed-time assertions by default.

| Scenario | Harness | TUI-only aspect | Record flow | Playback assertions | Gate |
|---|---|---|---|---|---|
| n/a | `agent` | None known. Model, reasoning, token usage, and quota/provider health are provider/config surfaces. | n/a | n/a | Non-PTY capability tests must pass. |
| `claude.status` | `claude` | Subscription/account/quota/status. | Start `claude` through direct PTY, wait for prompt, run `/status` and/or `/usage`, capture frames and service events, exit cleanly. | Parsed account class, quota/status windows, current model when exposed, freshness timestamp, and normalized unavailable/auth/quota blockers. | record/playback required. |
| `claude.models` | `claude` | Available model list and effort/reasoning levels. | Start `claude`, run `/model`, capture model rows, selected/default model, and effort selector state. | Non-empty model rows, selected/default marker, effort levels or version-pinned effort metadata, stale mapping detection. | record/playback required. |
| `codex.status` | `codex` | Subscription/account/quota/status. | Start `codex --no-alt-screen` through direct PTY, run `/status`, wait for the rendered status panel, capture frames and service events, exit cleanly. | Parsed account class, current model/reasoning, quota windows including model-specific limits, freshness timestamp, and normalized blockers. | record/playback required. |
| `codex.models` | `codex` | Available model list and reasoning-level path. | Start `codex --no-alt-screen`, type `/model`, complete/confirm the slash command when required, capture the model picker, and step into reasoning selection when needed without changing persisted state. | Non-empty model rows, selected/current marker, reasoning-effort choices or chooser transition, unsupported-value behavior. | record/playback required. |
| n/a | `gemini` | No required TUI-only aspect is accepted yet. Headless mode, model setting, output format, approval mode, and session listing appear to be CLI surfaces, but the harness still lacks verified evidence for these surfaces. | n/a unless future discovery finds an interactive-only capability. | n/a | Explicit-only until the non-PTY blockers in the secondary promotion table pass. |
| n/a | `opencode` | No required TUI-only aspect identified. `opencode models`, `opencode stats`, `opencode providers`, `opencode run`, and JSON output are CLI surfaces. | n/a unless future discovery finds an interactive-only capability. | n/a | Non-PTY capability tests pass; automatic routing is allowed. |
| n/a | `pi` | No required TUI-only aspect identified. `--list-models`, `--thinking`, `--models`, `--mode json`, and `--print` are CLI surfaces. `pi config` is configuration UI, not a core harness capability. | n/a unless future discovery finds an interactive-only capability. | n/a | Non-PTY capability tests pass; automatic routing is allowed. |

Token usage remains a core capability, but it is not classified as TUI-only
when the harness native run stream or batch JSON output exposes authoritative
per-run token usage. Current Claude and Codex token usage should stay covered by
their native stream capability tests unless a future runner path must extract
tokens from an interactive TUI transcript. If that happens, add explicit
`claude.prompt-usage` or `codex.prompt-usage` rows here before marking the
capability complete. Claude `/usage` is quota/subscription evidence, not a
substitute for per-run token utilization.

The checklist is append-only for supported harness behavior: if a harness gains
a required TUI-only surface, add a row here and add matching record/playback
tests before marking the capability complete. A broad capability matrix cell is
not `pass` unless its TUI-only checklist row, when present, has both accepted
record evidence and deterministic playback evidence.

Primary PTY support also requires host smoke coverage on Linux and macOS.
Docker-backed conformance is not enough by itself because it only proves the
Linux path. Windows remains an explicit unsupported gap until a Windows PTY
adapter and fixtures are designed.

Replay mode supports `realtime`, `scaled`, and `collapsed` timing. Human
inspection should use `realtime` playback so screen changes preserve recorded
100ms timing. CI replay should use `collapsed` timing unless the test is
specifically proving replay scheduling.

## Evidence Rules

- Replay cassettes may prove parser, event-shape, metadata, timeout, status, and
  typed-drain behavior.
- Accepted replay cassettes currently exist for primary subprocess harnesses
  `claude` and `codex`; both include a simple tool-using run with normalized
  `tool_call` and `tool_result` events.
- Live record-mode evidence is required before claiming real authenticated
  harness support for a capability.
- Skipped live record mode is not passing evidence; it is only an explicit
  diagnostic that local prerequisites are missing.
