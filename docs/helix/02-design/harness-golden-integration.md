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

Each cassette directory contains:

- `manifest.json` — version, harness, scrubbed command policy, timeout, and
  permission mode.
- `input.json` — prompt, reasoning, permission, and request metadata.
- `frames.jsonl` — replayable output/service-event frames with normalized
  timing deltas.
- `service-events.jsonl` — CONTRACT-003 `ServiceEvent` stream consumed by
  deterministic replay tests.
- `final.json` — normalized final event payload.
- `quota.json` — quota/status evidence when applicable.
- `scrub-report.json` — redaction and safety report.

Required live harness binaries for this bead:

- `claude`
- `codex`
- `pi`
- `opencode`

Until the direct PTY quota recorder from ADR-002 is implemented, quota preflight
also requires `tmux` for the existing Claude/Codex quota capture helpers. Replay
mode remains independent of `tmux` and credentials.

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
