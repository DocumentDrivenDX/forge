# Design Plan: Model-First Routing

**Date**: 2026-04-10
**Status**: CONVERGED
**Refinement Rounds**: 5

## Problem Statement

The current routing surface makes users name a `backend` pool before they can
route across equivalent providers. That is the wrong abstraction for the common
case. Users usually know the model they want, not an internal pool name. For a
setup like `bragi`, `vidar`, `grendel`, and `openrouter`, the desired command
is:

```bash
ddx-agent run --model qwen3.5-27b "Some nice prompt"
```

and the resolver should choose the best available configured provider that can
serve that model. The design should preserve the runtime boundary
(`agent.Run()` still receives one concrete provider), but replace backend-first
configuration and CLI behavior with model-first routing. It must also preserve
the DDx boundary: DDx routes across harnesses, while embedded `ddx-agent`
continues the inner provider-selection step once DDx selects the embedded
harness.

## Requirements

### Functional

1. The preferred CLI entrypoint for an execution run is
   `ddx-agent run [flags] <prompt>`.
2. The preferred routing inputs are `--model` and `--model-ref`, not
   `--backend`.
3. The configuration surface supports model routes keyed by the requested model
   or canonical target, with one or more provider candidates per route.
4. A model route may express provider-specific concrete model overrides when
   different providers require different upstream model strings.
5. Direct `--provider` selection remains supported for exact operator control.
6. `--model-ref` continues to resolve through the shared model catalog before
   route selection.
7. `--model` remains an explicit concrete model request that bypasses catalog
   policy but still participates in routing.
8. Selection prefers healthy candidates in the highest-priority tier and uses a
   deterministic tie-break rule within that tier.
9. Route failover may advance to the next candidate only for provider-side or
   transport-side failures, not for prompt or tool-schema mistakes that would
   fail identically on all candidates.
10. The chosen provider, requested model input, resolved canonical target,
    concrete model, and any failover attempts are recorded in result/session
    artifacts and telemetry.
11. Existing `backends`, `default_backend`, and `--backend` inputs remain as
    deprecated compatibility surfaces during migration and emit warnings.
12. DDx must be able to depend on this routing contract for the embedded
    harness while exposing a parallel intent-first routing experience across
    codex, claude, opencode, pi, cursor, and other harnesses.

### Non-Functional

1. The routing layer must preserve the simple runtime boundary:
   `agent.Run()` still receives exactly one provider per attempt.
2. Routing state must be deterministic and replayable across runs.
3. Availability checks must not impose a hard dependency on active probe calls
   before every request.
4. The migration path must not break current named-provider users.
5. The boundary with DDx must stay explicit and explainable.

### Constraints

1. Providers remain the concrete transport/auth unit.
2. Prompt presets remain unrelated to routing.
3. The shared model catalog remains the authority for aliases, profiles, and
   canonical targets.
4. The CLI must stay thin; routing intelligence belongs in config/resolution
   code, not ad hoc flag parsing.
5. DDx receives only model intent and embedded-routing attribution facts; it
   must not need provider candidate lists to use the embedded harness.

## Architecture Decisions

### Decision 1: Make the public routing surface model-first

- **Question**: What should users name when they want the resolver to pick a
  provider?
- **Alternatives**:
  - Keep named backend pools and improve documentation.
  - Add provider capability declarations only and infer routing implicitly.
  - Key routing configuration by requested model/canonical target.
- **Chosen**: Key routing by requested model/canonical target.
- **Rationale**: This matches operator intent and lets users ask for
  `qwen3.5-27b` directly without inventing an extra route label.

### Decision 2: Keep providers, catalog, and routing as distinct layers

- **Question**: Where should transport, policy, and selection live?
- **Alternatives**:
  - Fold model policy into provider config.
  - Replace providers with a new route object end-to-end.
  - Keep providers concrete, catalog authoritative for policy, and routes as a
    selection layer above providers.
- **Chosen**: Keep the three-layer split.
- **Rationale**: The separation is already defensible; only the user-facing
  routing surface is wrong.

### Decision 2A: Preserve the DDx embedding boundary

- **Question**: What does DDx need to know about inner routing?
- **Alternatives**:
  - Expose provider candidate lists and let DDx participate.
  - Keep DDx at the harness layer and surface only requested intent plus
    outcome attribution.
- **Chosen**: Keep DDx at the harness layer.
- **Rationale**: DDx already mirrors similar routing across non-agent harnesses
  and should not re-implement embedded-provider logic.

### Decision 3: Use passive availability and failover, not probe-on-every-run

- **Question**: How should "available backend" selection work?
- **Alternatives**:
  - Probe every candidate before each run.
  - Never track health; only do pre-run deterministic selection.
  - Use passive health: try the preferred candidate, record failures, and avoid
    recently failed candidates for a cooldown window.
- **Chosen**: Passive health with bounded failover.
- **Rationale**: It matches the user's intent without adding startup latency or
  probe-only failure modes.

### Decision 4: Keep `--provider` as the explicit escape hatch

- **Question**: How should operators force one concrete transport?
- **Alternatives**:
  - Remove explicit provider selection entirely.
  - Keep `--provider` and let it bypass route selection.
- **Chosen**: Keep `--provider`.
- **Rationale**: It preserves precise control for debugging, auth-specific
  runs, and compatibility.

### Decision 5: Deprecate backend pools instead of removing them abruptly

- **Question**: How should the repo migrate from `backend` to model routes?
- **Alternatives**:
  - Hard break immediately.
  - Keep both surfaces indefinitely.
  - Add model routes, map legacy backend inputs into them during migration, and
    document deprecation.
- **Chosen**: Deprecate with compatibility.
- **Rationale**: The existing release already shipped backend pools; removal
  should be deliberate and observable.

## Interface Contracts

### CLI

Preferred:

```bash
ddx-agent run --model qwen3.5-27b "Some nice prompt"
ddx-agent run --model-ref code-fast "Implement this function"
ddx-agent run --provider grendel --model qwen3.5-27b "Investigate this test"
```

Compatibility:

```bash
ddx-agent -p "Some nice prompt"
ddx-agent -p "Some nice prompt" --backend code-fast-local
```

DDx integration:

- DDx may call the embedded harness with a model ref or exact pin.
- DDx does not name `model_routes`, provider candidates, or health state.
- Embedded `ddx-agent` returns routing attribution facts that DDx can log and
  display alongside its cross-harness routing evidence.

Routing precedence:

1. If `--provider` is set, build that provider directly.
2. Else if `--model` is set, resolve a model route by requested model.
3. Else if `--model-ref` is set, resolve through the catalog, then resolve the
   corresponding model route.
4. Else if a default model ref or model route is configured, use it.
5. Else fall back to the default provider path.

### Configuration

```yaml
model_catalog:
  manifest: ~/.config/agent/models.yaml

providers:
  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
    api_key: lmstudio
  vidar:
    type: openai-compat
    base_url: http://vidar:1234/v1
    api_key: lmstudio
  grendel:
    type: openai-compat
    base_url: http://grendel:1234/v1
    api_key: lmstudio
  openrouter:
    type: openai-compat
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}

routing:
  default_model_ref: code-fast
  health_cooldown: 30s

model_routes:
  qwen3.5-27b:
    strategy: priority-round-robin
    candidates:
      - provider: bragi
        model: qwen3.5-27b
        priority: 100
      - provider: vidar
        model: qwen3.5-27b
        priority: 100
      - provider: grendel
        model: qwen3.5-27b
        priority: 100
      - provider: openrouter
        model: qwen/qwen3.5-27b
        priority: 10
```

Compatibility config:

- `backends` and `default_backend` continue to load for one migration window.
- Legacy backend definitions are translated into internal model routes at load
  time and produce a deprecation warning.

### Result, Session, and Telemetry

The routing layer records:

- requested model input (`--model` or resolved `--model-ref`)
- requested model ref when present
- selected provider
- selected route key (model route key, not arbitrary backend label)
- resolved concrete model
- attempted providers in order
- failover count

DDx should only consume the attribution layer above, not the underlying route
candidate table.

## Data Model

### Config Types

- `RoutingConfig`
  - `DefaultModel`
  - `DefaultModelRef`
  - `HealthCooldown`
- `ModelRouteConfig`
  - `Strategy`
  - `Candidates`
- `RouteCandidateConfig`
  - `Provider`
  - `Model` (optional provider-specific concrete model override)
  - `Priority`

### Runtime State

Routing keeps lightweight per-route state for:

- round-robin counter within a priority tier
- recent provider failures
- cooldown expiration per provider candidate

This replaces the current per-backend counter files with route-oriented state.
DDx does not read or own this state.

## Error Handling

- **Unknown model route**: fail before `agent.Run()` with a clear CLI/config
  error.
- **Unknown provider in a route**: fail config validation before the run.
- **No candidates in a route**: invalid config.
- **No healthy candidates**: if every candidate is in cooldown, retry the best
  candidate once rather than failing closed forever on stale health state.
- **Transport/auth/upstream availability failure**: fail over to the next
  eligible candidate and record the attempt chain.
- **Prompt/tool-schema/request-shape error**: do not fail over; return the
  original error because another provider is unlikely to help.

## Security

- Credentials remain in provider config only.
- Health state stores only routing metadata, not prompts or secrets.
- Compatibility warnings must not print raw credentials from deprecated config.

## Test Strategy

- **Unit**: route resolution by model/model_ref, legacy backend translation,
  candidate ranking, cooldown expiry, tie-break rotation, failover
  classification.
- **Integration**: CLI `run --model ...` selects among multiple local providers
  and falls back to OpenRouter only when local candidates fail or are marked
  unhealthy.
- **E2E**: DDx can request a logical model name on the embedded harness and
  receive a run attributed to the concrete provider actually used, while DDx
  continues to mirror comparable intent-first routing across non-agent
  harnesses.

## Implementation Plan

### Dependency Graph

1. Evolve FEAT-004, FEAT-006, PRD, and SD-005/SD-002 to authorize model-first
   routing and `run`.
2. Add config/model-route types and legacy backend translation.
3. Replace backend resolution with route resolution and passive failover state.
4. Update CLI surface to prefer `run` and model-first flags.
5. Update session/result/telemetry attribution.
6. Add integration coverage and migration docs.

### Issue Breakdown

- Config/schema replacement
- Resolver and health/failover state
- CLI surface and compatibility warnings
- Result/session/telemetry attribution update
- Integration and migration coverage

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Legacy users rely on `--backend` heavily | M | M | Keep compatibility parsing and emit deprecation warnings before removal |
| Health classification causes the wrong failover behavior | M | H | Limit failover to transport/auth/upstream-availability failures and test each class deterministically |
| Model route keys drift from catalog canonical targets | M | M | Prefer canonical target keys and resolve `model_ref` before route lookup |
| Route config becomes another hidden abstraction | L | M | Keep route keys equal to requested model names or canonical targets; avoid arbitrary labels |

## Observability

- Emit routing warnings when deprecated backend config or CLI flags are used.
- Record route selection and failover attempts in session logs and OTel spans.
- Preserve selected provider and resolved model attribution for usage and cost
  reporting.

## Governing Artifacts

- [prd.md](/Users/erik/Projects/agent/docs/helix/01-frame/prd.md)
- [FEAT-004-model-routing.md](/Users/erik/Projects/agent/docs/helix/01-frame/features/FEAT-004-model-routing.md)
- [FEAT-006-standalone-cli.md](/Users/erik/Projects/agent/docs/helix/01-frame/features/FEAT-006-standalone-cli.md)
- [SD-002-standalone-cli.md](/Users/erik/Projects/agent/docs/helix/02-design/solution-designs/SD-002-standalone-cli.md)
- [SD-005-provider-config.md](/Users/erik/Projects/agent/docs/helix/02-design/solution-designs/SD-005-provider-config.md)
