# Design Plan: Rename DDX Agent to DDX Agent

**Date**: 2026-04-08
**Status**: DRAFT
**Refinement Rounds**: 1

## Problem Statement

The project is currently named "DDX Agent" across code, module metadata, CLI
surfaces, install/update flows, website content, demos, and HELIX artifacts.
The new product identity is:

- product name: `agent`, properly `ddx agent`
- GitHub repository: `DocumentDrivenDX/agent`
- CLI binary: `ddx-agent`
- Go module: `github.com/DocumentDrivenDX/agent`

This is not a single string replacement. The current codebase mixes at least
five different naming surfaces:

- Go package and import path identity (`package agent`,
  `github.com/DocumentDrivenDX/agent`)
- CLI identity (`ddx-agent`, `cmd/ddx-agent`, installer, updater, release asset names)
- config and filesystem identity (`.agent`, `~/.config/agent`, `~/.cache/agent`)
- environment variable identity (`AGENT_PROVIDER`, `AGENT_MODEL`, etc.)
- consumer-specific model-catalog surfaces (`agent.openai`, `agent.anthropic`)

The rename must be staged so runtime breakage is easy to attribute and public
surface decisions are made explicitly rather than via accidental search/replace.

## Current State

### Baseline Health

- `git status --short` is clean
- `go build ./cmd/ddx-agent` succeeds
- `go test ./...` succeeds in the root module
- current Git remote is `https://github.com/DocumentDrivenDX/agent.git`

### Inventory Summary

- files containing `ddx-agent`/`DDX Agent`/`AGENT`: 98
- code-oriented files outside HELIX docs and top-level prose: 69
- HELIX docs containing the current name: 23
- website files containing the current name: 8
- explicit module/import references to `github.com/DocumentDrivenDX/agent`: 67
- config/env/path references such as `.agent`, `AGENT_*`, `.cache/agent`,
  `/agent`, or `cmd/ddx-agent`: 94 matches in runtime/docs/install surfaces

### High-Impact Rename Surfaces

1. Root API package
   - `agent.go`, `loop.go`, `stream.go`, `stream_consume.go`, `pricing.go`
   - all tests and dependent packages import `github.com/DocumentDrivenDX/agent`
   - root package name is `ddx-agent`

2. CLI package and command path
   - `cmd/ddx-agent/main.go`
   - `cmd/ddx-agent/update.go`
   - tests under `cmd/ddx-agent/*.go`
   - `Makefile` builds `./cmd/ddx-agent`

3. Config, storage, and env naming
   - `config/config.go`
   - `.agent/config.yaml`
   - `~/.config/agent/config.yaml`
   - `.agent/sessions`
   - `~/.cache/agent/latest-version.json`
   - `AGENT_PROVIDER`, `AGENT_BASE_URL`, `AGENT_API_KEY`, `AGENT_MODEL`

4. Distribution and update flow
   - `install.sh`
   - `cmd/ddx-agent/update.go`
   - release repo constant `DocumentDrivenDX/agent`
   - binary names such as `agent-darwin-arm64`

5. Shared model catalog surface names
   - `modelcatalog/catalog.go`
   - `modelcatalog/catalog/models.yaml`
   - current surface keys: `agent.openai`, `agent.anthropic`

6. Public documentation and website
   - `README.md`
   - `CONTRIBUTING.md`
   - `website/go.mod`
   - website content and demo casts under `website/`
   - demo scripts under `demos/`

7. Governing artifacts and historical docs
   - `AGENTS.md`
   - `docs/helix/`
   - solution designs and PRD still describe the system as DDX Agent

## Resolved Naming Decisions

### Decision 1: Repository owner remains `DocumentDrivenDX`

Current remote:

- `DocumentDrivenDX/agent`

Confirmed target:

- `DocumentDrivenDX/agent`

This is a repository rename only. The owner string is unchanged. This affects:

- `go.mod`
- README install paths
- website links
- installer and updater download URLs
- release asset naming and redirect expectations

### Decision 2: Public Go module follows the repository path

Confirmed module:

- `github.com/DocumentDrivenDX/agent`

This is the obvious low-friction Go module rename from the current module path
(`github.com/DocumentDrivenDX/agent`). It avoids vanity import setup and keeps
`go get` and `go install` behavior conventional.

## Decisions Required Before Full Migration

### Decision 3: Confirm whether config/env surfaces should be renamed now

The request explicitly renames the product, repo, CLI, and module. It strongly
implies renaming the remaining public surfaces, but those need confirmation for
the exact target forms:

- `.agent` -> `.agent` or `.ddx-agent`
- `AGENT_*` -> `AGENT_*` or `DDX_AGENT_*`
- `agent.openai` / `agent.anthropic` -> `agent.openai` / `agent.anthropic`

These are user-visible compatibility breaks and should not be inferred
silently.

### Decision 4: Backward compatibility policy

Need an explicit answer for whether the first rename pass should:

- preserve old config/env names as deprecated aliases for one release, or
- break immediately and replace all `ddx-agent` surfaces in one shot

This affects config loading, update/install messaging, docs, and tests.

## Recommended Migration Order

### Phase 1: Runtime identity

- rename root package references from `ddx-agent` to `agent`
- rename module/import path references
- rename `cmd/ddx-agent` to the new CLI path
- update build targets and tests

Goal: the code compiles and tests pass under the new package/module/CLI names.

### Phase 2: External runtime surfaces

- update installer and self-update constants
- update binary naming and `which` lookup logic
- update config directory, cache directory, session log directory, and env vars
- decide whether legacy aliases remain temporarily supported

Goal: install, update, config load, and session storage all use the new
product identity.

### Phase 3: Shared catalog and prompt surfaces

- rename `agent.openai` and `agent.anthropic` surface keys if desired
- rename default prompt preset if desired
- update test fixtures and catalog manifests

Goal: user-facing model policy matches the new product name.

### Phase 4: Docs, demos, and website

- update README, CONTRIBUTING, website content, and install snippets
- update recorded demo text, demo scripts, and cast asset paths
- update website module path and canonical URLs

Goal: published docs no longer instruct users to install or run `ddx-agent`.

### Phase 5: Governing artifacts

- update active architecture/design docs to the new product name
- decide whether historical artifact IDs such as `SD-001-agent-core.md` are
  preserved for audit history or renamed
- update `AGENTS.md` conventions after code moves settle

Goal: the artifact stack reflects the current product name without erasing
historical traceability.

## Recommended Guardrails During Implementation

- do not do a blind global replacement first; stage runtime code before
  historical docs
- keep one verification loop after each phase:
  - `go test ./...`
  - `go build ./cmd/<new-cli>`
- after runtime renames land, run targeted grep checks for residual
  code-path references:
  - module/import path references
  - CLI command examples
  - config/env/path literals
  - release repo constants

## Execution Checklist

- confirm env/config/catalog naming targets
- rename runtime code and tests
- rename CLI/install/update surfaces
- rename docs/website/demos
- run `go test ./...`
- run `go build ./cmd/<new-cli>`
- run final grep to isolate intentionally preserved historical `ddx-agent` mentions

## Proposed First Implementation Pass

Once the unresolved naming decisions are confirmed, the first code pass should
focus only on the runtime and distribution surfaces:

- `go.mod`
- root package declarations/imports
- `cmd/ddx-agent/` and its tests
- `config/`
- `install.sh`
- `Makefile`
- updater/release constants

That yields a working renamed binary before touching the larger body of docs and
historical HELIX material.
