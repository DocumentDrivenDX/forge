---
ddx:
  id: SD-002
  depends_on:
    - FEAT-006
    - SD-001
---
# Solution Design: SD-002 — Standalone CLI

**Feature**: FEAT-006 (Standalone CLI)

## Scope

Feature-level design for the `ddx-agent` CLI binary — the thin porcelain that
proves the DDX Agent library works end-to-end. The CLI is not the product; the
library is. This design covers the binary, config loading, and session
subcommands.

## Requirements Mapping

### Functional Requirements

| Requirement | Technical Capability | Component | Priority |
|-------------|---------------------|-----------|----------|
| Non-interactive mode (FEAT-006 FR-1..3) | `ddx-agent -p "prompt"`, stdin | `cmd/ddx-agent` | P0 |
| Exit codes (FEAT-006 FR-4) | 0/1/2 mapping | `cmd/ddx-agent` | P0 |
| Output modes (FEAT-006 FR-5..6) | stdout text, --json, stderr progress | `cmd/ddx-agent` | P0 |
| Config file (FEAT-006 FR-7..10) | YAML config + env + flags | `cmd/ddx-agent` | P0 |
| Session commands (FEAT-006 FR-11..14) | log, replay, usage subcommands | `cmd/ddx-agent` | P1 |
| DDx harness (FEAT-006 FR-15..16) | stdin prompt, JSON output | `cmd/ddx-agent` | P0 |

### NFR Impact

| NFR | Requirement | Design Decision |
|-----|-------------|-----------------|
| Startup time | <50ms to first LLM request | No heavy init; parse config + construct provider only |
| Binary size | <20MB static binary | Minimal deps, no TUI libraries |
| Zero config | Works with LM Studio defaults | Sensible defaults for localhost:1234 |

## Solution Approach

The CLI is a single `cmd/ddx-agent/main.go` entry point using Go's `flag` stdlib
package (per project concern override — no Cobra). Subcommands are dispatched
by the first positional argument.

### Command Structure

```
ddx-agent -p "prompt"              # run agent, print result
ddx-agent -p @file.md              # prompt from file
echo "prompt" | ddx-agent          # prompt from stdin
ddx-agent --json -p "prompt"       # JSON output

ddx-agent log                      # list recent sessions
ddx-agent log <session-id>         # show session detail
ddx-agent replay <session-id>      # human-readable replay
ddx-agent usage                    # cost/token summary
ddx-agent usage --since=7d         # with time window
```

### Config Resolution Order

1. Built-in defaults (localhost:1234, openai-compat, 20 iterations)
2. Global config: `~/.config/agent/config.yaml`
3. Project config: `.agent/config.yaml`
4. Environment variables: `AGENT_PROVIDER`, `AGENT_BASE_URL`, `AGENT_API_KEY`,
   `AGENT_MODEL`
5. CLI flags: `--provider`, `--base-url`, `--api-key`, `--model`,
   `--max-iter`, `--work-dir`

Later sources override earlier ones.

### Config File Format

```yaml
provider: openai-compat
base_url: http://localhost:1234/v1
api_key: ""
model: qwen3.5-7b
max_iterations: 20
session_log_dir: .agent/sessions
```

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Agent completed successfully |
| 1 | Agent failed (error, iteration limit, provider error) |
| 2 | CLI usage error (bad flags, missing config) |

## System Decomposition

### `cmd/ddx-agent/main.go`

- Parse flags and subcommand
- Load config (file → env → flags)
- Construct provider and tools
- Call `agent.Run()` or session subcommand
- Print result, set exit code

### Config loader (internal to cmd)

- YAML parsing with `gopkg.in/yaml.v3`
- Env var overlay
- Flag overlay
- Validate and return `agent.Request`-compatible config

### Session subcommands (internal to cmd)

- `log`: List session files from log directory, show summary
- `replay`: Use `agent/session.Replay()` to render a session
- `usage`: Aggregate session logs with time filtering

## Technology Rationale

| Layer | Choice | Why |
|-------|--------|-----|
| CLI framework | `flag` stdlib | Minimal, no dependency, sufficient for this scope |
| Config format | YAML | Human-readable, DDx convention (`.ddx/config.yaml`) |
| YAML parser | `gopkg.in/yaml.v3` | De facto standard Go YAML library |

## Traceability

| Requirement | Component | Test Strategy |
|-------------|-----------|---------------|
| FEAT-006 FR-1..3 | main.go prompt handling | Functional: run binary with `-p` and stdin |
| FEAT-006 FR-4 | main.go exit codes | Functional: check exit codes for success/failure/usage |
| FEAT-006 FR-5..6 | main.go output | Functional: text vs `--json` output |
| FEAT-006 FR-7..10 | config loader | Unit: config merging from file/env/flags |
| FEAT-006 FR-11..14 | session subcommands | Functional: run against test session logs |
| FEAT-006 FR-15..16 | main.go DDx mode | Integration: DDx harness invocation |

## Concern Alignment

- **Concerns used**: go-std (areas: all)
- **Project override applied**: `flag` stdlib instead of Cobra
- **Constraints honored**: `gofmt`, `go vet`, version metadata via `-ldflags`

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| `flag` stdlib too limited for subcommands | L | L | Subcommand dispatch is trivial; upgrade to Cobra later if needed |
| Config file format drift from DDx | L | L | Follow same YAML conventions |

## Review Checklist

- [x] Requirements mapping covers all FEAT-006 functional requirements
- [x] Command structure is clear and documented
- [x] Config resolution order is explicit
- [x] Exit codes defined
- [x] Technology choices justified
- [x] Traceability complete
- [x] Concern alignment verified
