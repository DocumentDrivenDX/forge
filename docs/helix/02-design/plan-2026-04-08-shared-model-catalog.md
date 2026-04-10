# Design Plan: Shared Model Catalog and Updateable Model Manifest

**Date**: 2026-04-08
**Status**: CONVERGED
**Refinement Rounds**: 5

## Problem Statement

DDX Agent now owns prompt presets, but model policy is still duplicated outside the
repo in DDx and HELIX-adjacent tooling. That creates three problems:

- rapidly changing model release data is copied into multiple repos
- prompt presets and model policy risk colliding on the same naming surface
- downstream tools cannot share one authoritative source for aliases,
  tiers/profiles, canonical targets, and deprecations

The goal is to make agent the reusable owner of model policy while keeping
runtime orchestration responsibilities separate: agent owns catalog data and
resolution rules, DDx owns cross-harness orchestration and guardrails, and
HELIX owns only stage intent.

## Requirements

### Functional

- Provide a agent-owned Go package for loading and resolving a shared model
  catalog.
- Store model policy in a structured manifest maintained separately from Go
  logic inside the agent repo.
- Represent aliases, model families, tiers/profiles, canonical current targets,
  and deprecated/stale entries.
- Support consumer-specific surface mappings so one canonical target can resolve
  to different concrete strings for agent OpenAI-compatible providers, agent
  Anthropic providers, Codex, Claude Code, or future consumers.
- Ship an embedded manifest snapshot with agent releases and allow an optional
  external manifest override path.
- Allow direct pinned model strings to bypass the catalog when a caller needs
  exact control.
- Keep prompt presets and model catalog references on distinct config/CLI
  surfaces.

### Non-Functional

- Runtime use must not require network access.
- Resolution must be deterministic and side-effect free.
- The package should remain small, Go-stdlib friendly, and unit-testable beside
  the implementation.
- External manifest updates should not require changes to unrelated runtime
  code.

### Constraints

- DDX Agent remains an embeddable library; `agent.Run()` still accepts one concrete
  `Provider`.
- Backward compatibility with today's provider config and `--model` override
  path must be preserved.
- DDx and non-agent harnesses need catalog data without pulling agent runtime
  orchestration into their own execution loops.

## Architecture Decisions

### Decision 1: Separate package and manifest from provider config

- **Question**: Where should agent model policy live?
- **Alternatives**:
  - keep hardcoded Go maps in provider/config packages
  - let each downstream repo keep its own tables
  - create a dedicated package plus manifest
- **Chosen**: create a dedicated `modelcatalog/` package backed by a structured
  manifest
- **Rationale**: provider config owns transport/auth; downstream repos should
  not duplicate volatile release-policy data; a dedicated package makes the
  ownership split explicit

### Decision 2: Embedded snapshot plus optional external override

- **Question**: How should consumers receive updates?
- **Alternatives**:
  - embedded manifest only
  - always require an external file
  - embed a release snapshot and optionally load a newer external file
- **Chosen**: embed a snapshot in the binary/module and permit an explicit
  external override path
- **Rationale**: embedded data keeps the library self-contained and deterministic
  by default; an override path lets DDx or operators update model policy on a
  faster cadence than agent code releases

### Decision 3: Canonical targets plus surface mappings

- **Question**: How does one catalog serve agent and non-agent consumers?
- **Alternatives**:
  - store only raw model IDs
  - duplicate per-consumer aliases with no shared canonical layer
  - store canonical targets with per-surface concrete strings
- **Chosen**: canonical targets with per-surface mappings
- **Rationale**: one logical model family may require different concrete strings
  across Anthropic, OpenAI-compatible endpoints, Codex, or Claude Code surfaces

### Decision 4: Profiles are policy references, not orchestration

- **Question**: Who maps `smart` / `fast` / `cheap` into a concrete run?
- **Alternatives**:
  - agent runtime chooses automatically
  - HELIX chooses concrete models directly
  - agent defines the shared profile references and DDx resolves them during
    harness orchestration
- **Chosen**: agent defines shared profile references; DDx resolves them while
  selecting harness and model intent
- **Rationale**: this preserves agent as policy owner without dragging harness
  orchestration into the runtime

### Decision 5: Deprecation metadata is part of the catalog contract

- **Question**: How should stale model references be handled?
- **Alternatives**:
  - no deprecation tracking
  - human-readable notes only
  - structured deprecation metadata with replacement guidance
- **Chosen**: structured metadata with status, replacement, and dates when known
- **Rationale**: DDx guardrails and operator tooling need machine-readable
  signals when a model reference is stale or deprecated

## Interface Contracts

### Go Package Surface

```go
package modelcatalog

type Surface string

const (
    SurfaceAgentOpenAI    Surface = "agent.openai"
    SurfaceAgentAnthropic Surface = "agent.anthropic"
    SurfaceCodex          Surface = "codex"
    SurfaceClaudeCode     Surface = "claude-code"
)

type LoadOptions struct {
    ManifestPath    string // optional external override; empty means embedded snapshot
    RequireExternal bool
}

type ResolveOptions struct {
    Surface         Surface
    AllowDeprecated bool
}

type Catalog struct { /* parsed manifest + indexes */ }

type ResolvedTarget struct {
    Ref             string
    Profile         string
    Family          string
    CanonicalID     string
    ConcreteModel   string
    Deprecated      bool
    Replacement     string
    ManifestSource  string
    ManifestVersion int
}

func Default() (*Catalog, error)
func Load(opts LoadOptions) (*Catalog, error)
func (c *Catalog) Resolve(ref string, opts ResolveOptions) (ResolvedTarget, error)
func (c *Catalog) Current(profile string, opts ResolveOptions) (ResolvedTarget, error)
```

### Config and CLI Surface

- Config gains an optional catalog block:

```yaml
model_catalog:
  manifest: ~/.config/agent/models.yaml
```

- CLI gains a catalog-oriented selector separate from prompt presets:

```bash
ddx-agent run --model-ref code-smart "review this diff"
ddx-agent run --model qwen3-coder-next "summarize"
ddx-agent run --model claude-sonnet-4-20250514 "use exact pin"
```

- `--model` remains a concrete override and bypasses catalog policy.
- `--model-ref` resolves aliases or profiles through the catalog.

### Consumer Boundary

- DDX Agent CLI uses the catalog to resolve `--model-ref` or model routes.
- DDx uses the catalog as a library dependency for harness/model resolution and
  warnings/guardrails.
- When DDx selects the embedded harness, it passes only model intent
  (`model_ref` or exact pin). Embedded `ddx-agent` continues provider
  selection internally.
- HELIX does not depend on the catalog at runtime; it emits stage intent such as
  `smart` or `fast`, and DDx maps that intent to a concrete harness/model pair.

## Data Model

### Manifest Shape

```yaml
version: 1
generated_at: 2026-04-08T00:00:00Z

profiles:
  code-smart:
    target: claude-sonnet-4
  code-fast:
    target: qwen3-coder-next
  cheap:
    target: qwen3-coder-next

targets:
  claude-sonnet-4:
    family: claude-sonnet
    aliases: [claude-sonnet, sonnet]
    status: active
    surfaces:
      agent.anthropic: claude-sonnet-4-20250514
      agent.openai: anthropic/claude-sonnet-4
      claude-code: sonnet

  qwen3-coder-next:
    family: qwen3-coder
    aliases: [qwen-coder-next, qwen3-coder]
    status: active
    surfaces:
      agent.openai: qwen/qwen3-coder-next

  claude-sonnet-3.7:
    family: claude-sonnet
    aliases: [claude-sonnet-3.7]
    status: deprecated
    replacement: claude-sonnet-4
    deprecated_at: 2026-04-08
    surfaces:
      agent.anthropic: claude-3-7-sonnet-20250219
```

### Resolution Rules

1. If the reference matches a profile, resolve the profile to its target.
2. Else if it matches a canonical target ID, use that target.
3. Else if it matches an alias, resolve to the owning canonical target.
4. Select the concrete model string for the requested consumer surface.
5. Return deprecation metadata with the resolved target.
6. Fail if no surface mapping exists for the chosen consumer.

### Storage and Update Workflow

- Canonical source file: `modelcatalog/catalog/models.yaml`
- Embedded release snapshot: bundled from that file into `modelcatalog/`
- Optional override file: configured by path in `.agent/config.yaml` or by a
  downstream consumer such as DDx
- Update workflow:
  1. edit `modelcatalog/catalog/models.yaml`
  2. run catalog validation tests
  3. commit manifest changes independently of unrelated runtime logic when
     possible
  4. consumers that want faster updates sync the external file on their own
     cadence

## Error Handling

- **Unknown reference**: return a typed resolution error listing the unknown
  reference.
- **Missing surface mapping**: return an error describing the unresolved
  consumer surface.
- **Deprecated target in strict mode**: return an error; otherwise return the
  resolved target with metadata so callers can warn.
- **Invalid manifest**: fail load/validation with file/field context.
- **External manifest missing**:
  - if `RequireExternal=false`, fall back to embedded snapshot
  - if `RequireExternal=true`, return an error

## Security

- The manifest contains no secrets; credentials remain in provider config.
- DDX Agent should not fetch manifests over the network at runtime.
- External manifest paths must be explicit and local to avoid surprising policy
  changes from implicit remote sources.

## Test Strategy

- **Unit**: manifest parsing, alias lookup, profile resolution, canonical target
  lookup, deprecation handling, embedded-fallback behavior
- **Integration**: config/CLI resolution from `--model-ref` or model routes
  into one concrete provider/model pair
- **E2E**: DDx consumes agent catalog data without its own duplicate model table

## Implementation Plan

### Dependency Graph

1. Evolve governing docs and ownership language.
2. Implement `modelcatalog/` and manifest validation/loading.
3. Add config/CLI plumbing for `model_ref` and optional manifest override.
4. Add DDx consumer integration after agent API stabilizes.

### Issue Breakdown

- `agent-63ba2a0f`: evolve PRD, FEAT-004, SD-005, SD-003, and architecture to
  authorize the shared catalog
- `agent-94b5d420`: capture the converged design in this plan document and
  linked artifacts
- `agent-66eef6fe`: build the package, manifest loader, and tests

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Catalog terminology collides with prompt presets again | M | H | Reserve `preset` for prompts and use `model_ref` / `profile` / `alias` for model policy |
| Consumer surfaces diverge too much for one catalog | M | M | Use canonical targets plus per-surface mappings instead of one raw string |
| External manifest drift breaks determinism | M | M | Keep embedded snapshot as the default and require explicit override paths |
| Deprecation policy becomes advisory only and gets ignored | M | M | Make deprecation structured and machine-readable so DDx can enforce guardrails |

## Observability

- Log the manifest source (`embedded` or explicit path) and manifest version in
  CLI/debug output when model resolution occurs.
- Surface deprecation warnings during config resolution before a run begins.
- Record resolved model reference, canonical target, and concrete model in
  session metadata once implementation lands.

## Governing Artifacts

- `docs/helix/01-frame/prd.md`
- `docs/helix/01-frame/features/FEAT-004-model-routing.md`
- `docs/helix/02-design/solution-designs/SD-003-system-prompts.md`
- `docs/helix/02-design/solution-designs/SD-005-provider-config.md`
- `docs/helix/02-design/architecture.md`
