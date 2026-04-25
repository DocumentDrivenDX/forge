---
ddx:
  id: ADR-005
  depends_on:
    - CONTRACT-003
    - SD-005
---
# ADR-005: Smart Routing Replaces `model_routes`

| Date | Status | Deciders | Related | Confidence |
|------|--------|----------|---------|------------|
| 2026-04-25 | Proposed | DDX Agent maintainers | `CONTRACT-003`, `SD-005` | Medium |

## Context

SD-005 currently makes `model_routes:` the resolution surface: users hand-author per-tier candidate lists in YAML, the CLI re-reads them and synthesizes a `RouteDecision` injected through `ServiceExecuteRequest.PreResolved`, and the service treats that as authoritative. The block exists to coordinate same-tier failover among local LM Studio hosts and to keep the routing engine from stripping configured candidates whose discovery probe is failing.

This is two failure modes welded together:

1. **Configurable failover in user YAML** is the wrong surface. The model catalog already knows which models occupy each tier (`code-economy`/`code-medium`/`code-high`/`smart`), provider config already lists endpoints, and the routing engine already scores `(harness, provider, model)` candidates with cost / latency / capability inputs. Forcing users to also write `model_routes:` makes them coordinate three sources of truth that the service could coordinate itself.

2. **The CLI synthesis path is leaky.** `cmd/agent/main.go:474-487` builds a `RouteDecision{Reason: "cli configured route"}`, threads it through `ServiceExecuteRequest.PreResolved`, and overwrites the request's `Provider`/`Model`/`Harness` fields — which the contract claims `PreResolved` mode ignores. The mechanism only exists because the routing engine strips configured candidates on probe failure; without that strip, the CLI synthesis would not be needed.

Two adjacent observed behaviors confirm the design is wrong:

- When all configured local providers are down (`vidar`, `grendel` 502/timeout), the engine returns "all tiers exhausted — no viable provider found" instead of falling forward to a healthy subscription harness (`claude-max`, `codex-pro`). The user's expectation is automatic fallback when quota allows.
- The `adaptive min-tier` heuristic locks out the `cheap` tier after a low trailing-window success rate (observed: 0.06 over 17 attempts), and the lockout never recovers because no cheap-tier attempts run to refresh the signal.

The shape we want: providers are transport, the catalog is policy, the routing engine decides per request based on liveness, prompt characteristics, and a cost/latency/capability score. Users do not maintain a routing table.

## Decision

Replace the `model_routes`-driven resolution surface with deterministic smart routing.

### 1. Auto-selection rules

`Execute` auto-fills the route only when the caller pinned nothing (`Profile`, `Model`, `ModelRef`, `Provider` all empty). Explicit pins always win — no heuristic overrides them. Default profile is `smart`.

Auto-selection signals are deterministic and already available:

- `EstimatedPromptTokens` — prompt size in tokens. Used to filter candidates whose context window cannot hold the prompt.
- `RequiresTools` — whether the request enables tool calls. Used to filter providers/types whose `SupportsTools()` is false.
- `Reasoning` — caller's reasoning request. Used to filter providers whose support level is below the request.

These existed in `internal/routing.Request` already (`internal/routing/engine.go:15`); the gap is that public `RouteRequest`/`ServiceExecuteRequest` did not surface them, so service-side smart routing was blind. ADR adds them to the public surface (see CONTRACT-003 update).

No prose-heuristic complexity classifier. Token count plus `RequiresTools` is the entire signal in this round.

### 2. Routing decision

Per request:

1. **Build the candidate set** = every catalog `(provider, model)` whose tier ≥ caller's profile target and whose provider is configured.
2. **Filter by liveness** via `HealthCheck`. Drop providers whose latest probe failed. If the filter empties the set, escalate the tier ceiling once (e.g. `code-medium` → `code-high`) and retry. If escalation also empties the set, surface a precise "no live provider for tier ≥ X" error — not "tiers exhausted."
3. **Filter by capability**: drop candidates whose context window < `EstimatedPromptTokens`, whose `SupportsTools()` is false when `RequiresTools` is true, or whose reasoning support is below the request.
4. **Score each survivor** using the existing engine scoring (`internal/routing/score.go`): quality score from catalog/benchmark, cost penalty (with subscription quota ramp already implemented at `service_routing.go:593`), latency penalty from per-(provider,model) success/latency stats, recent-success bonus.
5. **Dispatch top-1**, return the full ranked candidate trace in the routing decision event so callers can see why candidates 2..N lost.
6. **On failure rotate** within the same tier; only escalate the tier when the same-tier set is exhausted. Record outcome to update per-(provider,model) stats. **Replaces the per-tier trailing-window adaptive min-tier** (which was too coarse — locked the cheap tier out forever after 17 failed attempts because no cheap attempts could refresh the signal).

#### Pipeline order

Steps 1–6 above describe the user-visible flow. The implementation collapses them into the engine's two phases:

**In `routing.Resolve` (`internal/routing/engine.go`):** build candidate set → apply inline gates (liveness via provider cooldown, capability via `EstimatedPromptTokens` / `RequiresTools` / `Reasoning`, subscription gate via `SubscriptionOK`, harness allowlist) → score eligible candidates with cost, latency, capability, and quota signals (subscription quota above the warning threshold applies a **score penalty**, not a cost-amount mutation) → rank and tie-break by cost.

**In `service.ResolveRoute` (`service_routing.go`):** wrap the engine with profile-tier escalation when the engine returns `ErrNoLiveProvider`. Catalog tier filtering and profile ceiling enforcement happen in the engine's inline gates as part of candidate construction; cross-tier escalation lives at the service layer because it loops `routing.Resolve` over successive ladder profiles.

#### Escalation ladder

When same-tier candidates are exhausted (all filtered or all scored ineligible), `service.ResolveRoute` walks the profile tier ladder defined by the `routing.ProfileEscalationLadder` constant (`internal/routing/engine.go`). The ladder is `cheap → standard → smart` and is **one-way upward only** — escalation past `smart` returns `ErrNoLiveProvider` with no fallback down. Profiles not present in this ladder (custom profiles, `local`, `offline`, `air-gapped`) do not escalate. The ladder is not catalog-driven in this release.

### 3. Per-(provider, model) success/latency

In-memory + TTL only this round (matches today's `service_route_attempts.go:13`). Persistent state across restarts is deferred until storage and warm-start behavior are designed. Key change vs. today: signal is keyed on `(provider, model)`, not on tier. A single bad model does not lock out its whole tier.

### 4. Subscription quota inputs

Claude/Codex/Gemini already publish quota signals via harness caches (`service_routing.go:335`). Cost ramping when ≥80% used already exists. Keep both unchanged.

OpenRouter and native HTTP providers do not publish live quota. Treat their cost as static catalog cost in this round; file a follow-up bead for live-quota plumbing on those providers but do not block this work on it.

### 5. `route-status` redesigned

Today `route-status` enumerates configured `model_routes` keys. Post-deletion it must report **eligible candidates for a requested intent or profile**, with score components (quality, cost, latency, success-rate, filter reason) per candidate, and the per-(provider,model) success/latency stats. Operators read it to answer "why did the router pick X?" — not to inspect their own YAML.

### 6. Delete

- `model_routes:` config block; its loader in `internal/config/config.go`; `ServiceConfig.ModelRouteConfig`/`ModelRouteNames`.
- `service_routing.go` model_routes short-circuit landed in `90d9b03` (revert).
- `ServiceExecuteRequest.PreResolved` and `RouteDecision`-as-input. `PreResolved` was specified for a dry-run-then-execute flow that has no current consumer; its only producer in the repo is the CLI synthesis at `cmd/agent/main.go:474-487`, which is itself part of the `model_routes` deletion. `ResolveRoute` remains as a public method (operator dashboard / debug surface), but its result is informational, not re-injectable.
- CLI `selection.RouteCandidates` and `cmd/agent/routing_provider.go` provider-construction wrappers.
- SD-005 D4–D7 (model-route surface). SD-005 rewritten from this ADR.

### 7. Keep

- `routing.default_model`, `routing.default_model_ref`, `routing.health_cooldown` config keys. These are useful defaults, not model_routes.
- `internal/modelcatalog` — source of truth for tier policy, cost, context, capability.
- `internal/routing` engine scoring — refactor input source, do not rewrite scoring.
- Provider adapters, `internal/reasoning`, the three session-log refactors landed earlier in this stack (`agent-7faa0edf`, `agent-b9bd700f`, `agent-99549438`).
- `--profile cheap|fast|smart`, `--model`, `--provider`, `--reasoning`, `--model-ref` CLI flags.

### 8. Backward compatibility

For one release: parse `model_routes:` if present, log a deprecation warning naming the offending config path, **honor the configured ordering**. Hard-erroring immediately is safer than silently ignoring (warn-and-ignore is the worst option — semantic drift). Remove the parser and the warning in the next release.

Add a `cmd/agent/service_boundary_test.go` structural check that fails if `internal/config` reintroduces `model_routes` parsing after the deprecation cycle ends.

## Consequences

### Positive

- One source of routing truth (catalog + provider config + engine), not three.
- Live-provider fallback works automatically: when local LM Studio hosts are down and subscription quota is available, requests route to `claude-max`/`codex-pro` without operator config.
- Per-(provider,model) signal recovers from transient failures; one bad model no longer locks out its tier indefinitely.
- `RouteCandidate` exposes structured score components, not a free-form `Reason` string. Operator debugging gets a real surface.
- Public `RouteRequest` exposes the prompt-aware inputs the engine already needed; service-side smart routing is no longer blind.

### Negative

- Removes a configurable failover surface. Power users who deliberately wire an ordered candidate list lose that knob. Mitigation: explicit `--provider <name>` and `--model <name>` pins remain; chaining failover by ordering candidates was already a workaround for the engine's probe-strip behavior, which this ADR fixes at the source.
- Public surface change to `RouteRequest`/`ServiceExecuteRequest` (new fields; one removed). Consumers re-bind.
- One-release deprecation window means operators with `model_routes:` configs do not get an immediate hard error. Acceptable trade-off vs. silent drift.

## Migration

Plan in three sharper beads (replacing the obsolete chain `agent-9d120ece`/`6dd4ad97`/`873081a9`/`8804194f`, which is canceled with note "superseded by ADR-005"):

1. **Public surface update** — add `EstimatedPromptTokens` / `RequiresTools` to `RouteRequest` and `ServiceExecuteRequest`; remove `ServiceExecuteRequest.PreResolved`; add structured score components to `RouteCandidate`; update CONTRACT-003. Revert `90d9b03`. Update SD-005 with the auto-selection section and deprecation note.

2. **Wire inputs + scoring + route-status** — plumb new `RouteRequest` fields from CLI through `Execute`; wire engine gates against them; expose score components in routing-decision events; redesign `route-status` to show eligible candidates per intent. Add per-(provider,model) success/latency keying. Replace per-tier adaptive min-tier with per-model signal.

3. **Config + CLI cleanup + deprecation** — delete `model_routes` parser and `ServiceConfig.ModelRouteConfig`; delete CLI `selection.RouteCandidates` synthesis and `routing_provider.go` provider-construction wrappers; add deprecation warning when parsing legacy config; add boundary test forbidding `model_routes` re-entry.

Beads in steps 2 and 3 can be parallelized across two workers; step 1 blocks both.

## Out of scope (deferred)

- Persistent EWMA across process restarts. In-memory + TTL is fine for this round; persistence + warm-start is its own design.
- ML-style prompt classification beyond `EstimatedPromptTokens`/`RequiresTools`. Ship deterministic smart routing first.
- Live quota plumbing for OpenRouter and native HTTP providers. Static catalog cost suffices in this round.
- Reviewer pipeline overflow fixes — tracked separately in upstream `ddx` repo (FEAT-022 + `ddx-021bd69b`); this repo's only related work is one bead in step 1 to tighten the success-final usage convention.

## Related

- `CONTRACT-003` — public service surface; updated in step 1.
- `SD-005` — provider/model/routing config; rewritten from this ADR.
- `internal/routing/engine.go` — existing scoring engine; input source refactored, scoring unchanged.
- `service_routing.go` — subscription quota cost ramp at line 593 stays; `90d9b03` short-circuit reverts.
- Upstream `ddx-021bd69b` — reviewer JSON verdict contract (sibling repo, separate fix path).
