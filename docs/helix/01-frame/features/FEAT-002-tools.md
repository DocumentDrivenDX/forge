---
ddx:
  id: FEAT-002
  depends_on:
    - helix.prd
---
# Feature Specification: FEAT-002 — Tool Set

**Feature ID**: FEAT-002
**Status**: Draft
**Priority**: P0
**Owner**: Forge Team

## Overview

Forge provides a minimal, pi-style tool set — read, write, edit, bash — that
the LLM uses to interact with the filesystem and shell. Tools are the agent's
hands. This implements PRD P0 requirement 2.

## Problem Statement

- **Current situation**: Each agent CLI implements its own tools with different
  semantics (Claude Code has ~20 tools, pi has 4-7, codex has its own set).
- **Pain points**: Tool behavior varies across agents. DDx can't predict what
  file operations an agent will perform or constrain them.
- **Desired outcome**: A small, well-defined tool set with consistent behavior
  that Forge controls and DDx can audit.

## Requirements

### Functional Requirements

#### read

1. Accepts: path (string), offset (int, optional), limit (int, optional)
2. Resolves path relative to working directory
3. Returns file contents as string
4. Supports line offset and limit for partial reads
5. Returns error if file does not exist

#### write

7. Accepts: path (string), content (string)
8. Creates parent directories if they don't exist
9. Overwrites existing file entirely
10. Returns bytes written

#### edit

12. Accepts: path (string), old_string (string), new_string (string)
13. Reads file, finds old_string, replaces with new_string
14. Fails if old_string is not found in the file
15. Fails if old_string appears more than once (ambiguous edit)
16. Writes modified content back to file
17. Returns success/failure with context

#### bash

18. Accepts: command (string), timeout_ms (int, optional, default 120000)
19. Executes command via `sh -c` in the working directory
20. Captures stdout, stderr, exit code
21. Kills process on timeout, returns partial output with timeout error
22. Kills process on context cancellation

### Non-Functional Requirements

- **Security**: Forge assumes it runs in a sandbox. File paths outside the
  working directory are allowed but logged. No path validation boundary.
- **Performance**: File operations complete in <10ms for files under 1MB.
  Bash tool adds <5ms overhead beyond the command's own execution time.
- **Reliability**: Tools never panic. All errors are returned as structured
  tool results that the model can interpret.

## Edge Cases and Error Handling

- **Symlink chains**: Resolve symlinks fully, log final target path
- **Binary file read**: Return error indicating binary content detected
- **Empty file write**: Allow (creates empty file)
- **Edit with empty old_string**: Reject (would match everything)
- **Bash command produces >1MB output**: Truncate with "[truncated]" marker
- **Bash command is interactive (reads stdin)**: Provide /dev/null as stdin

## Success Metrics

- All four tools pass acceptance tests with both local and cloud models
- All file operations are logged with resolved paths
- Bash timeout reliably kills runaway processes

## Constraints and Assumptions

- No network-access tool (bash can do network operations, but there's no
  dedicated fetch/curl tool — keep the surface area small)
- Tools are not extensible in P0. Custom tools are a P2 concern.

## Dependencies

- **Other features**: FEAT-001 (agent loop calls tools)
- **PRD requirements**: P0-2

## Out of Scope

- Grep/find tools (P2)
- File watching or filesystem events
- Tool permission management (all tools are available; the caller controls
  scope via working directory)
- MCP tool integration
