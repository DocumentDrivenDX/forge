# Design Plan: Model Catalog Distribution, Refresh Workflow, and Effort-Tier Policy

**Date**: 2026-04-10
**Status**: CONVERGED
**Refinement Rounds**: 4

## Problem Statement

DDX Agent has the right ownership boundary for shared model policy, but the
current catalog is still incomplete as an operational system:

- the embedded manifest is stale and too small to serve as a real shared policy
  source
- the only implemented refresh path is a manual local override file
- there is no versioned published manifest channel outside normal binary
  releases
- the current manifest shape is too narrow for the intended cross-surface
  policy tiers because one logical tier may map to different concrete model
  families and different effort defaults on different surfaces

The immediate product need is to support a current coding-policy baseline with
three stable levels:

- `code-high`: `opus-4.6` / `gpt-5.4` at `high` effort
- `code-medium`: `sonnet-4.6` / `gpt-5.4-mini` at `medium` effort
- `code-economy`: `haiku-5.5` at `medium` effort and `qwen3.5-27b` at `high`
  effort

The broader system goal is to make the catalog publishable and updateable on a
faster cadence than `ddx-agent` binary releases while preserving the existing
runtime constraint that ordinary request execution does not fetch policy from
the network.

## Requirements

### Functional

- Keep `modelcatalog/catalog/models.yaml` as the canonical source manifest in
  this repo.
- Publish a versioned, immutable manifest bundle independently of binary
  releases whenever catalog policy changes.
- Publish a stable channel pointer so `ddx-agent`, DDx, and operators can fetch
  the latest approved manifest without guessing artifact names.
- Preserve the embedded manifest snapshot in `ddx-agent` releases for offline
  deterministic behavior.
- Add an explicit local update workflow so operators can install or refresh a
  local manifest file without editing YAML by hand.
- Keep the runtime request path offline-by-default. A normal `Run()` or
  `ddx-agent run ...` invocation must never fetch the manifest from the
  network.
- Allow DDx and `ddx-agent` to consume the same published manifest file.
- Evolve the manifest schema so one logical policy tier can project to
  different concrete models and effort defaults per surface.
- Refresh the starter catalog so it exposes current code-oriented tiers:
  `code-high`, `code-medium`, and `code-economy`.
- Preserve compatibility aliases during migration:
  - `smart -> code-high`
  - `fast -> code-medium`
  - `cheap -> code-economy`

### Non-Functional

- Published manifests must be deterministic, content-addressable, and checksum
  verifiable.
- A stale or missing external manifest must not break ordinary runtime use; the
  embedded snapshot remains the safe fallback.
- The distribution path should be simple enough for DDx to mirror locally or in
  CI without needing to parse GitHub HTML or binary release assets.
- Schema evolution must be explicit and versioned so older binaries can reject
  unsupported manifest shapes cleanly.

### Constraints

- `agent.Run()` still receives one resolved concrete provider per attempt.
- HELIX continues to emit intent only; it does not fetch or own manifest
  distribution.
- DDx remains the cross-harness router. Embedded `ddx-agent` owns only embedded
  provider selection and embedded-surface catalog projection.

## Architecture Decisions

### Decision 1: Separate source manifest from published catalog bundle

- **Question**: How should catalog policy be distributed faster than binary
  releases?
- **Alternatives**:
  - rely only on the embedded manifest
  - require users to copy the repo YAML manually
  - publish a versioned catalog bundle from manifest-only changes
- **Chosen**: keep the repo YAML as source of truth and publish a versioned
  catalog bundle plus a stable channel pointer from a dedicated catalog
  workflow
- **Rationale**: the source manifest remains reviewable in git, while consumers
  get a stable machine-readable distribution path decoupled from binary
  releases

### Decision 2: Keep network access out of the run path

- **Question**: Should `ddx-agent run` auto-fetch catalog updates?
- **Alternatives**:
  - implicit fetch during normal runs
  - explicit fetch/install command
  - no fetch support at all
- **Chosen**: explicit fetch/install via catalog-management commands only
- **Rationale**: request execution must stay deterministic and offline-safe;
  policy refresh is an operator action, not a side effect of a prompt

### Decision 3: Treat catalog targets as stable policy targets, not only vendor-family identities

- **Question**: How should one logical code tier map across Anthropic,
  OpenAI-compatible, Codex, Claude Code, and embedded-local surfaces?
- **Alternatives**:
  - require one canonical target to be the same vendor family everywhere
  - keep vendor-family targets and move policy tiers outside the catalog
  - allow canonical targets to represent stable policy tiers with
    surface-specific concrete projections
- **Chosen**: canonical targets may represent stable policy tiers such as
  `code-high`, `code-medium`, and `code-economy`
- **Rationale**: the shared catalog exists to express cross-surface policy.
  Requiring the same vendor family everywhere would make the catalog too weak
  for the actual DDx/DDX Agent routing model

### Decision 4: Add per-surface policy metadata to the manifest

- **Question**: Where do effort defaults and related routing hints live?
- **Alternatives**:
  - keep effort entirely outside the catalog
  - attach one global effort value to each target
  - attach optional per-surface policy metadata alongside each surface mapping
- **Chosen**: add optional per-surface policy metadata
- **Rationale**: `code-economy` already needs different effort defaults across
  surfaces (`haiku-5.5` medium, `qwen3.5-27b` high), so a single target-level
  effort field is insufficient

### Decision 5: Publish an immutable version plus a mutable channel pointer

- **Question**: What should consumers fetch?
- **Alternatives**:
  - mutable latest file only
  - immutable versions only
  - immutable versions plus channel metadata
- **Chosen**: publish both
- **Rationale**: immutable versions are required for reproducibility; mutable
  channel pointers are required for operational simplicity

## Interface Contracts

### Published Bundle Layout

The published catalog distribution should expose:

```text
catalog/
  stable/
    index.json
    models.yaml
    models.sha256
  versions/
    2026-04-10.1/
      index.json
      models.yaml
      models.sha256
```

`index.json`:

```json
{
  "schema_version": 2,
  "catalog_version": "2026-04-10.1",
  "channel": "stable",
  "published_at": "2026-04-10T00:00:00Z",
  "manifest_path": "models.yaml",
  "manifest_sha256": "...",
  "min_agent_version": "0.2.0",
  "notes": "Refresh code tiers to 2026-04 policy baseline"
}
```

Phase 1 distribution can use GitHub Pages or another static HTTP host under
project control. The design requirement is a stable machine-readable URL, not a
specific hosting brand.

### Local Config Surface

Existing local override remains:

```yaml
model_catalog:
  manifest: ~/.config/ddx-agent/models.yaml
```

No runtime network URL is added to ordinary request config in phase 1.

### CLI Surface

Add explicit catalog-management commands:

```bash
ddx-agent catalog show
ddx-agent catalog check --channel stable
ddx-agent catalog update --channel stable
ddx-agent catalog update --version 2026-04-10.1
```

Behavior:

- `catalog show` reports manifest source, schema version, catalog version, and
  high-level tier mappings
- `catalog check` compares the local/embedded manifest against the published
  channel metadata
- `catalog update` downloads the chosen published manifest, verifies checksum,
  writes it to the configured local manifest path, and never mutates the
  embedded snapshot

### DDx Consumption Boundary

- DDx may keep consuming the embedded library catalog by default.
- DDx should also be able to read the same externally installed manifest file
  when the operator wants faster policy refresh.
- DDx must not maintain a second independent model-policy table once this
  distribution path is complete.

## Data Model

### Manifest Schema Evolution

The current manifest version is too small for cross-surface effort-aware tiers.
Move to a versioned schema that preserves current fields and adds optional
per-surface policy metadata.

Illustrative shape:

```yaml
version: 2
generated_at: 2026-04-10T00:00:00Z
catalog_version: 2026-04-10.1

profiles:
  code-high:
    target: code-high
  smart:
    target: code-high
  code-medium:
    target: code-medium
  fast:
    target: code-medium
  code-economy:
    target: code-economy
  cheap:
    target: code-economy

targets:
  code-high:
    family: coding-tier
    status: active
    aliases: [high]
    surfaces:
      agent.anthropic: opus-4.6
      claude-code: opus-4.6
      agent.openai: gpt-5.4
      codex: gpt-5.4
    surface_policy:
      agent.anthropic:
        reasoning_default: high
      claude-code:
        reasoning_default: high
      agent.openai:
        reasoning_default: high
      codex:
        reasoning_default: high

  code-medium:
    family: coding-tier
    status: active
    aliases: [medium]
    surfaces:
      agent.anthropic: sonnet-4.6
      claude-code: sonnet-4.6
      agent.openai: gpt-5.4-mini
      codex: gpt-5.4-mini
    surface_policy:
      agent.anthropic:
        reasoning_default: medium
      claude-code:
        reasoning_default: medium
      agent.openai:
        reasoning_default: medium
      codex:
        reasoning_default: medium

  code-economy:
    family: coding-tier
    status: active
    aliases: [economy]
    surfaces:
      agent.anthropic: haiku-5.5
      claude-code: haiku-5.5
      agent.openai: qwen3.5-27b
    surface_policy:
      agent.anthropic:
        reasoning_default: medium
      claude-code:
        reasoning_default: medium
      agent.openai:
        reasoning_default: high
```

Notes:

- Catalog targets are now policy tiers, not promises of same-family vendor
  models across every surface.
- Per-surface effort is advisory routing metadata, not a direct replacement for
  an explicit caller-supplied effort override.
- Exact concrete model pins still bypass the catalog.

## Update Workflow

### Source Workflow

1. Edit `modelcatalog/catalog/models.yaml`.
2. Run catalog validation tests.
3. Merge manifest changes independently of unrelated runtime code where
   practical.
4. Dedicated workflow publishes the manifest bundle and stable channel pointer.

### Consumer Workflow

1. Operator or DDx runs `ddx-agent catalog check`.
2. If a newer manifest is desired, run `ddx-agent catalog update`.
3. The command writes the verified manifest to the configured local path.
4. Future runs resolve catalog refs from that local manifest; if the file is
   missing or unreadable, the embedded manifest remains the fallback unless the
   caller explicitly requires the external file.

## Error Handling

- **Published index unavailable**: `catalog check` / `catalog update` returns a
  network error; ordinary request execution remains unaffected.
- **Checksum mismatch**: reject the downloaded manifest and keep the previous
  local file.
- **Unsupported schema version**: reject the downloaded manifest with a clear
  compatibility error and keep the previous local file.
- **Configured local path missing**: ordinary runtime resolution falls back to
  the embedded snapshot unless strict external mode is requested.
- **Target missing a surface projection**: fail surface resolution
  deterministically for that consumer.

## Security

- Published manifests contain policy only and no secrets.
- The update command must verify a published checksum before replacing the local
  manifest.
- Stronger artifact signing can be layered later, but checksum validation is the
  phase-1 minimum.
- Runtime request execution must never fetch remote policy implicitly.

## Test Strategy

- **Unit**:
  - schema-v2 manifest parsing and validation
  - per-surface policy decoding
  - schema-version rejection
  - checksum verification
- **Integration**:
  - `ddx-agent catalog check` against a fixture index
  - `ddx-agent catalog update` installs a manifest to the configured local path
  - runtime resolution prefers the installed local manifest over the embedded
    snapshot
- **E2E**:
  - DDx and `ddx-agent` resolve the same catalog refs from the same external
    manifest file
  - embedded runs continue to function when the network is absent but the local
    manifest is already installed

## Implementation Plan

### Dependency Graph

1. Evolve governing artifacts to authorize published catalog bundles and
   effort-aware policy tiers.
2. Extend `modelcatalog` schema and resolver for schema v2 and per-surface
   policy metadata.
3. Add catalog publication workflow and channel metadata.
4. Add explicit `ddx-agent catalog` management commands.
5. Refresh the embedded manifest to the new code-tier baseline.
6. Update DDx consumption to prefer the installed shared manifest and remove
   stale builtin fallback behavior.

### Issue Breakdown

- publish versioned catalog bundles independently of binary releases
- add schema-v2 modelcatalog support with per-surface policy metadata
- add `ddx-agent catalog show/check/update`
- refresh the embedded manifest to `code-high`, `code-medium`, and
  `code-economy`
- update DDx to consume the same installed manifest path

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Catalog targets as policy tiers confuse older documentation that described vendor-family identities | M | M | Update FEAT-004, SD-005, PRD, and DDx routing docs together |
| Published manifest moves too quickly for older binaries | M | H | Include schema version and `min_agent_version`, reject incompatible updates cleanly |
| Operators expect ordinary runs to self-update policy | M | M | Keep update explicit and document the boundary clearly |
| DDx continues to carry a stale builtin catalog after agent publishes shared manifests | H | H | Add a DDx follow-up bead as part of the same planning pass |

## Observability

- `catalog show` and model-resolution logs should report:
  - manifest source
  - schema version
  - catalog version
  - resolved profile/target
  - resolved surface projection
- `catalog update` should log the installed version, checksum, source URL/path,
  and replacement outcome.
