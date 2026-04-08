---
ddx:
  id: FEAT-006
  depends_on:
    - helix.prd
    - FEAT-001
    - FEAT-002
    - FEAT-003
    - FEAT-005
---
# Feature Specification: FEAT-006 — Standalone CLI

**Feature ID**: FEAT-006
**Status**: Draft
**Priority**: P0
**Owner**: DDX Agent Team

## Overview

The `ddx-agent` CLI is a thin binary wrapping the DDX Agent library. Following the
ghostty model, it proves the library works end-to-end and serves as the DDx
harness backend. It is not the product — the library is. But a usable CLI
validates the design and provides a concrete integration target.

Patterned on pi's CLI interface (`pi -p "prompt"`) and DDx's config conventions
(`.ddx/config.yaml` → `.agent/config.yaml`).

## Problem Statement

- **Current situation**: The DDX Agent library has no way to be exercised outside
  of Go test code or a DDx integration.
- **Pain points**: Can't validate the library without building an embedder
  first. Can't use DDX Agent standalone for testing or experimentation.
- **Desired outcome**: A single binary that reads config, accepts a prompt,
  runs the agent loop, logs the session, and prints the result.

## Requirements

### Functional Requirements

#### Core CLI

1. `ddx-agent -p "prompt"` — non-interactive mode: run prompt, print result, exit
2. `ddx-agent -p @file.md` — read prompt from file
3. Prompt from stdin when not a TTY: `echo "prompt" | ddx-agent`
4. Exit code: 0 on success, 1 on agent failure, 2 on config/usage error
5. Output: final agent text to stdout. Structured JSON with `--json` flag.
6. Stderr: progress/status messages (tool calls in progress, etc.)

#### Configuration

7. Config file: `.agent/config.yaml` in the working directory, or
   `~/.config/agent/config.yaml` for global defaults
8. Config structure mirrors the library `Config` struct:
   ```yaml
   provider: openai-compat  # or anthropic
   base_url: http://localhost:1234/v1
   api_key: ""               # optional for local
   model: qwen3.5-7b
   max_iterations: 20
   session_log_dir: .agent/sessions
   ```
9. Environment variable overrides: `AGENT_BASE_URL`, `AGENT_API_KEY`,
   `AGENT_MODEL`, `AGENT_PROVIDER`
10. CLI flags override config file and env vars

#### Session Commands

11. `ddx-agent log` — list recent sessions (patterned on `ddx agent log`)
12. `ddx-agent log <session-id>` — show full session detail
13. `ddx-agent replay <session-id>` — human-readable conversation replay
14. `ddx-agent usage` — per-model token and cost summary (patterned on
    `ddx agent usage`)

#### DDx Harness Integration

15. When invoked as `ddx-agent` by DDx (`ddx agent run --harness=agent`), the CLI
    accepts prompt via stdin or final argument (matching DDx's `PromptMode`)
16. Output includes structured JSON with token usage for DDx to parse

### Non-Functional Requirements

- **Startup time**: < 50ms to first LLM request (no heavy initialization)
- **Binary size**: Single static binary, reasonable size (< 20MB)
- **Zero required config**: Works with defaults if LM Studio is running on
  localhost:1234 with a model loaded

## Edge Cases and Error Handling

- **No config file**: Use defaults (localhost:1234, first available model)
- **Provider not reachable**: Clear error message with URL, exit code 1
- **Prompt too large for model context**: Let the provider error propagate
- **Ctrl+C during execution**: Clean shutdown, write session.end to log

## Success Metrics

- `ddx-agent -p "Read main.go and tell me the package name"` works end-to-end
  with LM Studio
- `ddx-agent replay` accurately reproduces any completed session
- `ddx-agent usage` produces correct cost summary
- DDx can invoke `ddx-agent` as a harness and parse the result

## Constraints and Assumptions

- The CLI is intentionally minimal — it's a showcase, not a feature-rich app
- No TUI, no interactive mode, no REPL. Just `-p` and session commands.
- Config reader is CLI-specific; the library has no config file opinions

## Dependencies

- **Other features**: All P0 features (FEAT-001 through FEAT-005)
- **PRD requirements**: P0-12

## Out of Scope

- Interactive/REPL mode (use pi or claude for that)
- Shell completions, man pages
- Plugin or extension system
- Color output or rich terminal formatting
