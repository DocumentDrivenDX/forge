# Harness Golden-Master Integration

## Replay Mode

Replay mode is the default integration path and does not require live
credentials. It runs checked-in cassette-shaped fixtures through
`Service.Execute` and verifies event order, progress text, normalized final
text, usage when exposed by the harness stream, routing metadata, request
metadata, and quota/status cache projection.

Run:

```sh
go test -tags=integration ./...
```

The replay suite distinguishes itself from live harness evidence: POSIX fixture
scripts emulate recorded harness stdout and are not counted as proof that a live
Claude Code, Codex, Pi, opencode, or Gemini account is usable.

## Record Mode

Record mode is opt-in and must fail fast before writing evidence when a required
binary, auth state, subscription/quota path, or transport dependency is absent.

Run the preflight:

```sh
AGENT_HARNESS_RECORD=1 go test -tags=integration -run TestHarnessGoldenRecordModePreflight .
```

Run live record mode and write sanitized JSON cassette summaries:

```sh
AGENT_HARNESS_RECORD=1 \
AGENT_HARNESS_CASSETTE_DIR=./testdata/harness-golden/live \
go test -tags=integration -run TestHarnessGoldenRecordModeLive .
```

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
- Live record-mode evidence is required before claiming real authenticated
  harness support for a capability.
- Skipped live record mode is not passing evidence; it is only an explicit
  diagnostic that local prerequisites are missing.
