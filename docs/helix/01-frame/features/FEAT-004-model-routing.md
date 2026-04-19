---
ddx:
  id: FEAT-004
  depends_on:
    - helix.prd
    - FEAT-003
---
# Feature Specification: FEAT-004 — Shared Model Catalog, Model-First Routing, and Provider Configuration

**Feature ID**: FEAT-004
**Status**: Draft
**Priority**: P0 (named providers), P1 (shared catalog), P2 (model routes)
**Owner**: DDX Agent Team

## Overview

DDX Agent keeps the runtime boundary deliberately simple: `agent.Run()` receives
one resolved `Provider`. Model policy and routing happen above that boundary in
the config/CLI layer and in a reusable agent-owned model catalog.

This feature therefore has three related but separate responsibilities:

- **Providers** — concrete transport/auth definitions
- **Shared model catalog** — agent-owned aliases, tiers/profiles, canonical
  policy targets, and deprecation metadata
- **Model routes** — optional provider-selection policy keyed by requested
  model or canonical target

Prompt presets stay separate from all three. `preset` already means system
prompt behavior and must not be reused for model policy or routing.

## Terminology

- **Provider** — a concrete backend configuration: type, credentials, base URL,
  headers, and an optional default pinned model
- **Model catalog** — agent-owned policy/data describing model families,
  aliases, tiers/profiles, canonical policy targets, and
  deprecation/staleness status
- **Manifest** — the structured model-catalog data file maintained separately
  from Go logic and consumed by the catalog package
- **Model reference** — a user-facing name resolved through the catalog, such
  as an alias or tier/profile
- **Canonical target** — the stable policy target the catalog wants a given
  reference to resolve to; one target may project to different concrete models
  and reasoning defaults on different consumer surfaces
- **Model route** — a routing entry keyed by requested model or canonical
  target that chooses among one or more concrete providers for a run
- **Route candidate** — one concrete provider option within a model route, with
  optional provider-specific concrete model override and priority
- **Prompt preset** — system prompt selection (`preset`, `--preset`); unrelated
  to model policy and routing

## Problem Statement

- **Current situation**: DDX Agent can select one named provider directly, while
  callers and orchestrators still carry duplicated or mismatched routing assumptions above
  it.
- **Pain points**: Prompt presets already occupy the `preset` naming surface,
  provider configs currently mix transport and model concerns, and the shipped
  `backend` UX forces operators to invent labels when what they actually know
  is the model they want.
- **Desired outcome**: DDX Agent becomes the reusable source of truth for model
  aliases, tiers/profiles, canonical policy targets, deprecations, and embedded
  provider-selection policy, while callers keep cross-harness orchestration and
  HELIX keeps stage intent only.

## Requirements

### Functional Requirements

#### Phase 1 (P0): Named Providers

1. `Config` specifies named providers with type (`openai-compat` or
   `anthropic`), base URL, API key, and optional headers.
2. A provider may carry a pinned default model for explicit direct selection,
   but provider config is not the canonical source of alias/profile policy.
3. The CLI can select a provider directly by name.
4. All requests go to the resolved provider. If it fails, the request fails.
5. No fallback or retry across providers in phase 1 (retries within a single
   provider are handled by FEAT-003).
6. The provider used is recorded in the `Result`.

#### Phase 1B (P1): Shared Model Catalog and Manifest

7. DDX Agent owns a reusable shared model catalog separate from provider configs
   and prompt presets.
8. The catalog represents:
   - model families
   - aliases
   - tiers/profiles (for example `code-high`, `code-medium`, `code-economy`,
     with compatibility aliases such as `smart`, `fast`, `cheap`)
   - canonical policy targets
   - per-model entries for every concrete model eligible for a tier
   - ordered tier candidate lists that can contain multiple concrete models
   - deprecated or stale targets with replacement metadata
   - consumer-surface mappings where a canonical target needs different
     concrete strings and may carry different reasoning defaults for different
     downstream integrations
   - provider-specific concrete surface IDs on model entries, so a single tier
     can choose among Anthropic, OpenAI-compatible, Codex, or Claude Code model
     strings without duplicating cost and benchmark metadata
   - per-model reasoning capability metadata, including supported named values,
     numeric maximums, and named-to-token maps when a provider/model cannot
     derive safe limits from live metadata
9. Catalog data is stored in a structured manifest maintained separately from
   Go logic inside the agent repo.
10. DDX Agent ships an embedded snapshot of that manifest and may also load an
    external manifest override so policy/data can update independently of code
    releases where practical.
11. DDX Agent publishes versioned catalog manifests outside normal binary
    releases and exposes a stable machine-readable channel pointer so operators
    and callers can refresh policy faster than the binary release cadence.
12. Catalog refresh is explicit. Ordinary request execution must not fetch
    remote manifest data.
13. The DDX Agent CLI and any caller can resolve a model reference through the catalog to a
    concrete model string appropriate for the chosen consumer surface.
14. Explicit concrete model pins remain supported and intentionally bypass the
    catalog when a caller wants exact control.
15. Ownership split is explicit:
    - agent owns model catalog data/policy and provider selection inside the
      embedded runtime
    - callers own cross-harness orchestration and guardrails
    - HELIX owns stage intent only
16. The catalog uses `reasoning_default` for model-reasoning policy.
17. `reasoning_default` is a single scalar using the same value grammar as the
    public CLI/config/API `reasoning` field: `auto`, `off`, `low`, `medium`,
    `high`, supported extended values such as `minimal`, `xhigh` / `x-high`,
    and `max`, or numeric values such as `0`, `2048`, and `8192`.
18. Catalog defaults are tiered by expected capability and cost:
    - Below-smart tiers (`cheap`, `fast`, `standard`, `code-economy`, and
      `code-medium`) default to `reasoning=off`; this explicitly includes
      local/economy Qwen-family targets.
    - Smart tiers (`smart` and `code-high`) default to `reasoning=high`.
    - Explicit caller `reasoning` always wins over tier defaults, including
      supported requests above high such as `xhigh`, `x-high`, or `max`, and
      explicit numeric values.
19. Catalog candidates for numeric-only reasoning providers must publish
    per-model maximums or named-value maps unless the provider can derive safe
    limits from live metadata. The router must fail clearly on explicit
    unsupported or over-limit requests and may drop only auto/default reasoning
    controls for unsupported candidates.
20. Manifest schema v4 stores cost, context, benchmark, OpenRouter ID, and
    surface model strings on top-level `models` entries. Target entries retain
    tier policy only: family, aliases, status/replacement metadata,
    `context_window_min`, `swe_bench_min`, ordered `candidates`, and
    `surface_policy`.

#### Phase 2A (P2): Model Routes

21. `Config` may specify model routes keyed by requested model or canonical
    target, distinct from prompt presets and direct provider names.
22. A model route resolves to:
    - one route key equal to the requested model or canonical target
    - one or more provider candidates
    - optional provider-specific concrete model overrides
    - one selection strategy
23. Supported phase-2A strategies are:
    - `priority-round-robin` — use the highest-priority healthy tier and rotate
      within that tier between requests
    - `ordered-failover` — prefer candidates in configured order and advance
      only when the current candidate is unavailable
24. Model-route resolution happens in the config/CLI layer. `agent.Run()`
    still receives one concrete `Provider` per attempt.
25. The selected concrete provider, requested model input, resolved model
    reference, route key, and resolved concrete model are recorded in the
    `Result`.
26. Existing `backends`, `default_backend`, and `--backend` surfaces are
    deprecated compatibility inputs during migration and must emit warnings.

#### Phase 2B (P2, later): Health Tracking and Passive Failover

27. DDX Agent may track recent failures and temporarily back off unhealthy
    candidates using a bounded cooldown window.
28. A failed provider candidate may be skipped for the current request and a
    later candidate attempted only for transport/auth/upstream availability
    failures.
29. Prompt-shape, tool-schema, or other deterministic request errors must not
    trigger cross-provider failover.
30. Callers continue to pass only model intent (`model_ref` or exact pin) into the
    embedded harness. Callers must not duplicate inner provider-selection logic.

### Non-Functional Requirements

- **Simplicity**: library users can still pass a concrete `agent.Provider`
  directly with no YAML, catalog, or routing machinery.
- **Clarity**: prompt presets, provider config, model policy, and provider
  routing each use distinct terminology.
- **Boundary safety**: Callers may depend on agent-owned routing for the embedded
  harness, but they only name harness intent and never reproduce provider
  candidate logic.
- **Updateability**: rapidly changing model policy/data can be refreshed via an
  external manifest without requiring every consumer to wait for a new Go
  release.
- **Compatibility**: named-provider configuration remains valid; catalog and
  model-route features are additive.

## Edge Cases and Error Handling

- **Unknown provider name**: config resolution returns an error before the run.
- **Unknown model route**: config resolution returns an error before the run.
- **Unknown model reference**: catalog resolution returns an error before the
  run.
- **Deprecated or stale model reference**: resolution returns metadata that the
  caller may surface as a warning or block according to policy.
- **Manifest missing or unreadable**: fall back to the embedded snapshot unless
  the caller explicitly required the external manifest.
- **Model route with one candidate**: valid; behaves like explicit indirection.
- **Model route with empty candidate list**: invalid configuration.
- **Selected provider not reachable**:
  - Phase 1: return error immediately
  - Phase 2A: return error immediately after that candidate is selected
  - Phase 2B: may attempt the next candidate if failover is implemented
- **All candidates fail (phase 2B)**: return an error containing each attempt.

## Success Metrics

- Named-provider config works with LM Studio, Ollama, OpenRouter, and
  Anthropic.
- Callers can consume agent-owned catalog data without maintaining duplicate alias
  and profile tables.
- Prompt preset docs and model-policy docs stay terminology-safe and do not
  overload `preset`.
- Model-route routing, when implemented, selects provider candidates
  deterministically and records the actual provider used.

## Acceptance Criteria

| ID | Criterion | Suggested Verification |
|----|-----------|------------------------|
| AC-FEAT-004-01 | Direct named-provider resolution selects the configured provider before the run starts, and unknown provider names fail during config/CLI resolution rather than inside `agent.Run()`. | `go test ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-02 | Model references resolve through the embedded or external manifest to the correct consumer-surface model string and per-surface reasoning metadata, and missing references/surfaces fail deterministically before the run. | `go test ./internal/modelcatalog ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-03 | Deprecated or stale model references are rejected by default, surface replacement metadata, and can be explicitly allowed only when the caller opts in. | `go test ./internal/modelcatalog ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-04 | An explicit concrete `--model` or provider-level pin bypasses catalog policy for that run while leaving catalog-backed resolution unchanged for other runs. | `go test ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-05 | Model routes keyed by requested model or canonical target choose provider candidates deterministically for `priority-round-robin` and `ordered-failover`, reject empty/unknown routes before the run, and preserve direct-provider override behavior. | `go test ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-06 | Passive failover advances only on provider-side availability failures, records the attempt chain, and returns an aggregated routing error when every candidate fails. | `go test ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-07 | The selected concrete provider, requested model input, resolved model reference, route key, and resolved concrete model are recorded in the run result and session artifacts so callers and downstream analytics can attribute the actual embedded-provider choice without reproducing the route logic. | `go test ./cmd/ddx-agent ./internal/session ./...` |
| AC-FEAT-004-08 | Deprecated `backends`, `default_backend`, and `--backend` inputs still resolve during the migration window, emit a deprecation warning, and map to the same provider choice as the equivalent model-route configuration. | `go test ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-09 | Catalog publication produces an immutable versioned manifest bundle plus a stable channel pointer, and ordinary request execution never fetches remote manifest data implicitly. | `go test ./internal/modelcatalog ./cmd/ddx-agent ./...` |
| AC-FEAT-004-10 | The starter shared catalog publishes `code-high`, `code-medium`, and `code-economy` policy tiers with compatibility aliases `smart`, `fast`, and `cheap`, and projects the current concrete model/reasoning pairs onto supported surfaces. Below-smart tiers default to `reasoning=off`; smart/code-high defaults to `reasoning=high`; explicit caller values win when supported. | `go test ./internal/modelcatalog ./internal/config ./cmd/ddx-agent ./...` |
| AC-FEAT-004-11 | Manifest schema v4 uses top-level concrete `models` entries and target-level ordered `candidates`; pricing, OpenRouter refresh, context windows, and benchmarks are model-scoped while target entries remain tier policy. v3 manifests load through a compatibility upgrade path. | `go test ./internal/modelcatalog ./...` |

## Dependencies

- **Other features**: FEAT-003 (providers)
- **PRD requirements**: P0-3, P1-1, P1-10, P2-4

## Out of Scope

- Smart routing (task classification, context-length-based selection)
- Cost-based routing (pick cheapest model automatically)
- Concurrent multi-model execution (multi-harness quorum is a caller concern)
- Automatic model-quality escalation from local to cloud
- Model hosting or lifecycle management
- HELIX stage-to-model resolution logic
