---
ddx:
  id: FEAT-004
  depends_on:
    - helix.prd
    - FEAT-003
---
# Feature Specification: FEAT-004 — Shared Model Catalog, Backend Routing, and Provider Configuration

**Feature ID**: FEAT-004
**Status**: Draft
**Priority**: P0 (named providers), P1 (shared catalog), P2 (backend pools)
**Owner**: DDX Agent Team

## Overview

DDX Agent keeps the runtime boundary deliberately simple: `agent.Run()` receives one
resolved `Provider`. Model policy and routing happen above that boundary in the
config/CLI layer and in a reusable agent-owned model catalog.

This feature therefore has three related but separate responsibilities:

- **Providers** — concrete transport/auth definitions
- **Shared model catalog** — agent-owned aliases, tiers/profiles, canonical
  targets, and deprecation metadata
- **Backend pools** — optional routing targets that choose one provider before
  a run

Prompt presets stay separate from all three. `preset` already means system
prompt behavior and must not be reused for model policy or backend routing.

## Terminology

- **Provider** — a concrete backend configuration: type, credentials, base URL,
  headers, and an optional default pinned model
- **Model catalog** — agent-owned policy/data describing model families,
  aliases, tiers/profiles, canonical targets, and deprecation/staleness status
- **Manifest** — the structured model-catalog data file maintained separately
  from Go logic and consumed by the catalog package
- **Model reference** — a user-facing name resolved through the catalog, such
  as an alias or tier/profile
- **Canonical target** — the current concrete model/version the catalog wants a
  given reference to resolve to for a specific consumer surface
- **Backend pool** — a logical routing target that chooses among one or more
  concrete providers for a run and may attach a model reference
- **Prompt preset** — system prompt selection (`preset`, `--preset`); unrelated
  to model policy and routing

## Problem Statement

- **Current situation**: DDX Agent can select one named provider directly, while
  DDx and HELIX still carry duplicated model policy outside agent.
- **Pain points**: Prompt presets already occupy the `preset` naming surface,
  provider configs currently mix transport and model concerns, and rapidly
  changing model release data is too volatile to live only in hardcoded Go
  tables.
- **Desired outcome**: DDX Agent becomes the reusable source of truth for model
  aliases, tiers/profiles, canonical targets, and deprecations, while keeping
  harness orchestration in DDx and stage intent in HELIX.

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
   - tiers/profiles (for example `smart`, `fast`, `cheap`)
   - canonical current targets
   - deprecated or stale targets with replacement metadata
   - consumer-surface mappings where a canonical target needs different concrete
     strings for different downstream integrations
9. Catalog data is stored in a structured manifest maintained separately from
   Go logic inside the agent repo.
10. DDX Agent ships an embedded snapshot of that manifest and may also load an
    external manifest override so policy/data can update independently of code
    releases where practical.
11. DDX Agent CLI and DDx can resolve a model reference through the catalog to a
    concrete model string appropriate for the chosen consumer surface.
12. Explicit concrete model pins remain supported and intentionally bypass the
    catalog when a caller wants exact control.
13. Ownership split is explicit:
    - agent owns model catalog data/policy
    - DDx owns harness/provider orchestration and guardrails
    - HELIX owns stage intent only

#### Phase 2A (P2): Backend Pools

14. `Config` may specify named backend pools distinct from prompt presets.
15. A backend pool resolves to:
    - one model reference or pinned concrete model
    - one or more provider references
    - one selection strategy
16. Supported phase-2A strategies are:
    - `round-robin` — rotate across providers between requests
    - `first-available` — always pick the first configured provider
17. Backend-pool resolution happens in the config/CLI layer. `agent.Run()`
    still receives one concrete `Provider`.
18. The selected concrete provider, resolved model reference, and resolved
    concrete model are recorded in the `Result`.
19. If the selected provider fails, the request fails. Phase 2A does not retry
    the same request against another provider.

#### Phase 2B (P2, later): Failover and Health Tracking

20. DDX Agent may add request-level failover for backend pools after phase 2A is
    in use and measured.
21. If enabled, a failed provider may be skipped for the current request and a
    later provider attempted.
22. DDX Agent may track recent failures and temporarily back off unhealthy
    providers, but this is explicitly deferred until after phase 2A.

### Non-Functional Requirements

- **Simplicity**: library users can still pass a concrete `agent.Provider`
  directly with no YAML, catalog, or routing machinery.
- **Clarity**: prompt presets, provider config, model policy, and backend
  routing each use distinct terminology.
- **Updateability**: rapidly changing model policy/data can be refreshed via an
  external manifest without requiring every consumer to wait for a new Go
  release.
- **Compatibility**: named-provider configuration remains valid; catalog and
  backend-pool features are additive.

## Edge Cases and Error Handling

- **Unknown provider name**: config resolution returns an error before the run.
- **Unknown backend pool name**: config resolution returns an error before the
  run.
- **Unknown model reference**: catalog resolution returns an error before the
  run.
- **Deprecated or stale model reference**: resolution returns metadata that the
  caller may surface as a warning or block according to policy.
- **Manifest missing or unreadable**: fall back to the embedded snapshot unless
  the caller explicitly required the external manifest.
- **Backend pool with one provider**: valid; behaves like explicit indirection.
- **Backend pool with empty provider list**: invalid configuration.
- **Selected provider not reachable**:
  - Phase 1: return error immediately
  - Phase 2A: return error immediately after that provider is selected
  - Phase 2B: may attempt the next provider if failover is implemented
- **All providers fail (phase 2B)**: return an error containing each attempt.

## Success Metrics

- Named-provider config works with LM Studio, Ollama, OpenRouter, and
  Anthropic.
- DDx can consume agent-owned catalog data without maintaining duplicate alias
  and profile tables.
- Prompt preset docs and model-policy docs stay terminology-safe and do not
  overload `preset`.
- Backend-pool routing, when implemented, distributes requests deterministically
  according to the documented strategy.

## Dependencies

- **Other features**: FEAT-003 (providers)
- **PRD requirements**: P0-3, P1-1, P1-10, P2-4

## Out of Scope

- Smart routing (task classification, context-length-based selection)
- Cost-based routing (pick cheapest model automatically)
- Concurrent multi-model execution (that's DDx quorum)
- Automatic model-quality escalation from local to cloud
- Model hosting or lifecycle management
- HELIX stage-to-model resolution logic
