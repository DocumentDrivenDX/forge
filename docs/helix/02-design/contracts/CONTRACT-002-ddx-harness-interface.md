# CONTRACT-002: DDx Harness Interface Contract

**Status:** Draft  
**Owner:** DDX Agent maintainers  
**Related:** [FEAT-004](../../01-frame/features/FEAT-004-model-routing.md), [SD-005](../solution-designs/SD-005-provider-config.md)

## Purpose

This contract defines the stable Go interfaces that external DDx callers may
implement to extend or override ddx-agent behavior without forking or patching
routing internals. It describes the hook surface, stability guarantees, and
expected semantics for each interface.

The primary use case is DDx embedding ddx-agent as a library and injecting
quota-aware or policy-aware logic into the routing pipeline.

## Scope Boundary

This contract covers:

- Go interfaces exported or usable by external callers of ddx-agent internals
- Behavioral guarantees and invariants each implementation must satisfy
- Nil-safety contract for all hook points (nil means no-op, not panic)
- Planned extensions and their current stability status

This contract does **not** define:

- Config file schemas (see SD-005)
- Provider wire protocols or HTTP payloads
- OTel telemetry surface (see CONTRACT-001)
- Internal types not intended for external use

## Interface Definitions

### Section 1: CandidateScorer

**Status:** Shipped (agent-f5a6b7c8)  
**Location:** `cmd/ddx-agent/routing_scorer.go`

```go
// CandidateScorer allows external callers to overlay scores on top of the
// smart routing composite score. DDx uses this to factor in quota availability
// without forking the routing logic.
//
// Score returns an adjusted score for a candidate. The baseScore is the
// composite score already computed from reliability, performance, load, cost,
// and capability. The scorer may return the baseScore unchanged, clamp it to
// zero (e.g., quota exhausted), or boost it (e.g., priority quota).
//
// If the scorer returns a negative value, the candidate is treated as
// unhealthy and excluded from the ordering.
type CandidateScorer interface {
    Score(provider, model string, baseScore float64) float64
}

// CandidateScorerFunc is a func adapter for CandidateScorer.
type CandidateScorerFunc func(provider, model string, baseScore float64) float64

func (f CandidateScorerFunc) Score(provider, model string, baseScore float64) float64 {
    return f(provider, model, baseScore)
}
```

#### Semantics

| Return value | Effect |
|---|---|
| Equal to baseScore | Candidate order unchanged relative to its composite score |
| Greater than baseScore | Candidate boosted; may rank above higher-base-score peers |
| Zero | Candidate included but ranked at the bottom |
| Negative | Candidate marked unhealthy and excluded from the ordering |

#### Nil-safety

When no scorer is provided (`nil`), `buildSmartRoutePlan` behaves exactly as
before this interface existed. No external scorer is required and the default
routing logic is unchanged.

#### Injection point

`buildSmartRoutePlan` accepts a `scorer CandidateScorer` parameter. CLI callers
pass `nil`. Library callers may pass any conforming implementation.

The scorer is applied after the composite score (reliability + performance +
load + cost + priority) is computed for each healthy candidate. It is called
once per healthy candidate in the scoring pass.

#### Guarantees

- The scorer is never called for candidates already marked unhealthy before the
  scoring pass (cooldown exclusions, probe failures, config errors).
- A scorer returning a negative value sets `Healthy = false` and
  `Reason = "excluded by scorer"` on the candidate.
- The scorer is called with the resolved model string (post-probe), not the
  requested model alias.

## Section 7: Planned Extensions

The table below tracks hook points that are planned or under discussion.
Entries marked **shipped** have a stable interface and location. Entries marked
**planned** have no committed API yet.

| Hook | Status | Interface / Notes |
|---|---|---|
| `CandidateScorer` | **shipped** (agent-f5a6b7c8) | `Score(provider, model string, baseScore float64) float64` — negative return excludes candidate |
| Session event hook | planned | Callback for each `agent.Event` emitted during a run |
| Tool filter hook | planned | Allow callers to restrict or augment the tool set per-run |
| Cost cap hook | planned | Callback to abort or warn when projected cost exceeds a threshold |
| Provider health override | planned | Allow callers to inject health state without touching the filesystem |

## Compatibility Rules

### Nil is always safe

Every hook point in this contract MUST be nil-safe. Passing `nil` for any
interface parameter MUST produce identical behavior to the pre-hook baseline.

### Panics are a caller bug

A scorer implementation that panics is a caller bug, not a harness bug. The
harness does not recover panics from external scorers.

### Score values are unclamped

The harness does not clamp scorer output before applying it. Scorers that
return values outside `[0, 1]` will affect sort order. Callers should
constrain their return values unless they intentionally want extreme ordering.

### No concurrency guarantee

The scorer is called from a single goroutine during the scoring pass.
Implementations that share state with other goroutines are responsible for
their own synchronization.

## References

- [FEAT-004: Model Routing](../../01-frame/features/FEAT-004-model-routing.md)
- [SD-005: Provider Config](../solution-designs/SD-005-provider-config.md)
- [CONTRACT-001: OTel Telemetry Capture](./CONTRACT-001-otel-telemetry-capture.md)
