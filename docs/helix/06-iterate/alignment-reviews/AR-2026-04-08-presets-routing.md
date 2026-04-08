# Alignment Review: presets-and-routing

**Review Date**: 2026-04-08
**Scope**: DDx agent-wrapper prompt presets and multi-backend routing alignment with agent itself
**Status**: complete
**Review Epic**: agent-f3acdd05
**Primary Governing Artifact**: docs/helix/01-frame/features/FEAT-004-model-routing.md

## Scope and Governing Artifacts

### Scope

- Prompt preset support in agent runtime and CLI/config surface
- Multi-backend routing / round-robin behavior added in the DDx agent wrapper
- Planning alignment between PRD, FEAT-004, SD-003, SD-005, and current implementations

### Governing Artifacts

- `docs/helix/01-frame/prd.md:130-157,279-282`
- `docs/helix/01-frame/features/FEAT-004-model-routing.md:15-20,44-50,59-66`
- `docs/helix/02-design/solution-designs/SD-003-system-prompts.md:153-207`
- `docs/helix/02-design/solution-designs/SD-005-provider-config.md:24-67,82-107,146-185`
- `prompt/presets.go:13-128`
- `prompt/presets_test.go:10-45`
- `config/config.go:40-53,65-105,189-245`
- `cmd/ddx-agent/main.go:42-48,112-127,149-156,241-360`
- External implementation evidence from DDx wrapper: `../ddx/cli/internal/agent/agent.go:30-31,78-80,162-250`
- External tests from DDx wrapper: `../ddx/cli/internal/agent/agent_test.go:303-355`

## Intent Summary

- **Vision**: DDX Agent should be the canonical embeddable runtime; DDx wrapper conveniences that prove broadly useful should converge back into agent rather than live as undocumented wrapper-only behavior.
- **Requirements**: PRD marks system prompt composition as implemented (`prd.md:130-132`) and keeps multi-provider round robin as a phase-2 routing strategy (`prd.md:156-157,279-280`).
- **Features / Stories**: SD-003 defines built-in prompt presets and the prompt preset API (`SD-003-system-prompts.md:153-207`). FEAT-004 defines a phase-2 routing feature around multiple providers with round robin, failover, and temporary health skipping (`FEAT-004-model-routing.md:44-50`).
- **Architecture / ADRs**: SD-005 keeps multi-provider selection at the config/CLI layer: `agent.Run()` still receives one `Provider`; config resolves names to a provider instance (`SD-005-provider-config.md:146-185`).
- **Technical Design**: DDX Agent today implements built-in prompt presets and named providers. DDx adds a second concept — named model/back-end pools with `endpoints[]` and `strategy` — that agent does not yet define canonically.
- **Test Plans**: Presets are unit-tested in agent (`prompt/presets_test.go:10-45`). DDx wrapper routing behavior is also unit-tested externally (`../ddx/cli/internal/agent/agent_test.go:303-355`). DDX Agent has no equivalent routing tests because the feature does not exist there yet.
- **Implementation Plans**: Presets are implemented in agent; routing remains split between planned agent artifacts and wrapper-local DDx behavior.

## Planning Stack Findings

| Finding | Type | Evidence | Impact | Review Issue |
|---------|------|----------|--------|-------------|
| Prompt presets are fully specified in SD-003 and implemented in agent, but the user-facing config/docs surface is not propagated into SD-005 or README | stale | `SD-003-system-prompts.md:153-207`; `config/config.go:52-70`; `cmd/ddx-agent/main.go:47-48,149-156`; `README.md:131-139` | Users can use presets, but canonical config docs still imply the older flat config story and omit `preset` | agent-e226510e |
| FEAT-004 phase 2 describes ordered providers with per-request failover and health tracking, while SD-005 defines only named provider resolution and no provider-pool abstraction | contradiction | `FEAT-004-model-routing.md:44-50`; `SD-005-provider-config.md:146-185` | DDX Agent has no single canonical design for how round-robin should actually be configured or where it should live | agent-b9e612dc |
| DDx wrapper introduced a concrete abstraction (`models` + `endpoints[]` + `strategy`) that is closer to SD-005's config-layer philosophy than FEAT-004's provider-failover wording, but agent artifacts do not acknowledge it | missing-link | `../ddx/cli/internal/config/types.go:36-53`; `../ddx/cli/internal/agent/agent.go:162-250` | Wrapper behavior is useful but non-canonical; adopting it into agent as-is would currently bypass the planning stack | agent-b9e612dc |

## Implementation Map

- **Topology**: DDX Agent implements prompt presets in `prompt/`, config loading in `config/`, and CLI dispatch in `cmd/ddx-agent/`. DDx adds wrapper-only resolution logic in `../ddx/cli/internal/agent/agent.go`.
- **Entry Points**:
  - DDX Agent prompt preset runtime: `prompt.NewFromPreset()` (`prompt/presets.go:124-127`)
  - DDX Agent CLI preset selection: `--preset` and top-level config `Preset` (`cmd/ddx-agent/main.go:47-48,149-156`; `config/config.go:52-70`)
  - DDX Agent provider selection: named `providers` + `--provider` (`config/config.go:218-245`; `cmd/ddx-agent/main.go:112-127`)
  - DDx wrapper routing: `resolveAgentConfig()` + `selectEndpoint()` (`../ddx/cli/internal/agent/agent.go:162-250`)
- **Test Surfaces**:
  - DDX Agent prompt preset tests: `prompt/presets_test.go:10-45`
  - DDX Agent config tests for named providers / env expansion / headers: `config/config_test.go:17-224`
  - DDx wrapper routing tests: `../ddx/cli/internal/agent/agent_test.go:303-355`
- **Unplanned Areas**:
  - DDX Agent has no native notion of DDx's model/back-end pools.
  - DDX Agent CLI model override path currently mutates a local copy and rebuilds from the original config entry (`cmd/ddx-agent/main.go:123-127`), which is a fragile base for future routing/profile work.

## Acceptance Criteria Status

| Story / Feature | Criterion | Test Reference | Status | Evidence |
|-----------------|-----------|----------------|--------|----------|
| SD-003 | Built-in prompt preset API exists and can build prompts from stable preset names | `prompt/presets_test.go:10-45` | SATISFIED | `SD-003-system-prompts.md:153-207`; `prompt/presets.go:13-128` |
| SD-003 + CLI surface | DDX Agent CLI can select a prompt preset from flags/config | no dedicated CLI test | SATISFIED | `config/config.go:52-70`; `cmd/ddx-agent/main.go:47-48,149-156` |
| FEAT-004 phase 2 | Config specifies multiple providers and requests are distributed round-robin across them | none in agent | UNIMPLEMENTED | `FEAT-004-model-routing.md:44-50`; agent implementation only selects one named provider via `BuildProvider()` (`config/config.go:218-229`) |
| FEAT-004 phase 2 | Failed providers are skipped and provider health is tracked | none in agent | UNIMPLEMENTED | `FEAT-004-model-routing.md:47-50`; no failover/health logic exists in agent config or CLI |

## Gap Register

| Area | Classification | Planning Evidence | Implementation Evidence | Resolution Direction | Review Issue | Notes |
|------|----------------|-------------------|--------------------------|----------------------|-------------|-------|
| Prompt preset runtime | ALIGNED | `SD-003-system-prompts.md:153-207` | `prompt/presets.go:13-128`; `prompt/presets_test.go:10-45`; `cmd/ddx-agent/main.go:149-156` | code-to-plan complete | agent-e226510e | Preset runtime and API are in good shape. |
| Prompt preset config/docs propagation | STALE_PLAN | SD-003 defines presets as first-class behavior, and agent implementation exposes `Config.Preset` / `--preset` | `config/config.go:52-70`; `cmd/ddx-agent/main.go:47-48,149-156`; `README.md:131-139`; `SD-005-provider-config.md:24-67,146-185` | plan-to-code | agent-e226510e | The implementation is ahead of SD-005 and README. |
| Backend routing design | DIVERGENT | `FEAT-004-model-routing.md:44-50`; `SD-005-provider-config.md:146-185` | DDx wrapper uses model/back-end pools and strategies: `../ddx/cli/internal/config/types.go:36-53`; `../ddx/cli/internal/agent/agent.go:222-250` | decision-needed | agent-b9e612dc | DDX Agent needs one canonical routing abstraction and terminology that does not overload “preset”. |
| Backend routing implementation in agent | INCOMPLETE | `prd.md:156-157,279-280`; `FEAT-004-model-routing.md:44-50` | DDX Agent only supports named providers and direct selection: `config/config.go:218-245`; `cmd/ddx-agent/main.go:112-127,241-360` | code-to-plan (after design reconciliation) | agent-b9e612dc | DDx wrapper proves demand, but agent itself still lacks the feature. |

### Quality Findings

| Area | Dimension | Concern | Severity | Resolution | Issue |
|------|-----------|---------|----------|------------|-------|
| CLI routing path | maintainability | `cmd/ddx-agent/main.go:123-127` sets `pc.Model = *model` on a copy, then rebuilds from the unchanged config entry. Any future model-profile/back-end-pool work should remove this fragility. | medium | quality-improvement | agent-0f588c06 |

## Traceability Matrix

| Vision Item | Requirement | Feature / Story | Architecture / ADR | Solution / Technical Design | Test Reference | Implementation Plan | Code Status | Classification |
|-------------|-------------|-----------------|--------------------|-----------------------------|----------------|---------------------|-------------|----------------|
| DDX Agent should support caller-controlled system prompting without wrapper-only hacks | PRD P1-1 system prompt composition (`prd.md:130-132`) | SD-003 presets | CLI/library split | `SD-003-system-prompts.md:153-207` | `prompt/presets_test.go:10-45` | Implemented | Prompt presets exist in runtime and CLI/config | ALIGNED / STALE_PLAN for docs |
| DDX Agent should eventually distribute requests across multiple backends | PRD P2 routing (`prd.md:156-157,279-280`) | FEAT-004 phase 2 | SD-005 says routing stays at config/CLI layer | `FEAT-004-model-routing.md:44-50`; `SD-005-provider-config.md:146-185` | none in agent; wrapper tests in DDx | Not yet converged | Wrapper has a local solution; agent does not | DIVERGENT / INCOMPLETE |

## Review Issue Summary

| Review Issue | Functional Area | Status | Key Findings | Recommended Direction |
|-------------|-----------------|--------|--------------|-----------------------|
| agent-e226510e | Prompt presets | complete | Runtime is aligned; docs/config artifacts lag implementation | plan-to-code |
| agent-b9e612dc | Backend routing | complete | Planning artifacts disagree on routing model; wrapper added a non-canonical but useful config-layer solution; agent lacks native implementation | decision-needed, then code-to-plan |

## Execution Issues Generated

| Issue ID | Type | HELIX Labels | Parent / Source | Goal | Dependencies | Verification |
|---------|------|--------------|-----------------|------|--------------|-------------|
| agent-e63dba18 | task | helix,phase:design,kind:spec,area:lib,area:cli | discovered from `agent-e226510e` | Document prompt preset config and CLI support in SD-005 and README | — | Docs show top-level `preset`, built-in preset names, and CLI usage consistently |
| agent-00a13fa0 | task | helix,phase:design,kind:spec,area:lib,area:cli | discovered from `agent-b9e612dc` | Reconcile FEAT-004 routing design with DDx wrapper backend pools | — | FEAT-004 and SD-005 converge on one routing model and terminology |
| agent-0f588c06 | task | helix,phase:build,kind:feature,area:lib,area:cli | discovered from `agent-b9e612dc` | Implement backend-pool resolution in agent config and CLI | blocked by `agent-00a13fa0` | Tests cover round-robin / first-available and resolved model/provider usage |

## Issue Coverage Verification

| Gap / Criterion | Covering Issue | Status |
|-----------------|---------------|--------|
| Prompt preset config/docs propagation | agent-e63dba18 | covered |
| Backend routing design divergence | agent-00a13fa0 | covered |
| Backend routing implementation missing in agent | agent-0f588c06 | covered |
| FEAT-004 phase-2 round-robin acceptance criteria not implemented | agent-00a13fa0, agent-0f588c06 | covered |
| CLI routing-path quality concern | agent-0f588c06 | covered |

## Execution Order

1. `agent-e63dba18` — update SD-005 and README so prompt preset behavior is actually canonical.
2. `agent-00a13fa0` — settle the routing design and terminology before touching code.
3. `agent-0f588c06` — implement the chosen routing abstraction in agent config/CLI and add tests.

**Critical Path**: `agent-00a13fa0` → `agent-0f588c06`

**Parallel**: `agent-e63dba18` can proceed immediately in parallel with `agent-00a13fa0`.

**Blockers**: `agent-0f588c06` is blocked by `agent-00a13fa0`.

## Open Decisions

| Decision | Why Open | Governing Artifacts | Recommended Owner |
|----------|----------|---------------------|-------------------|
| Should agent phase 2 routing canonically adopt DDx's config-layer endpoint pools, or should FEAT-004 keep a true provider-failover/health-tracking model and treat DDx's approach as only a temporary wrapper convenience? | FEAT-004 and SD-005 currently point in different directions, and the wrapper already embodies a simpler path | `FEAT-004-model-routing.md:44-50`; `SD-005-provider-config.md:146-185`; `../ddx/cli/internal/agent/agent.go:222-250` | DDX Agent maintainers / design owner |
| What terminology should agent use so prompt presets and backend-selection presets are not conflated? | `preset` already means prompt preset in agent runtime and CLI | `SD-003-system-prompts.md:153-207`; `config/config.go:52-70`; `../ddx/cli/internal/config/types.go:47-53` | DDX Agent maintainers / design owner |

## Queue Health and Exhaustion Assessment

- **Queue health**: good. Every non-ALIGNED finding has a concrete follow-up bead.
- **Exhaustion**: no uncovered gaps remain for this scope.
- **Recommended first set**: start `agent-e63dba18` and `agent-00a13fa0` in parallel; hold implementation until the routing design lands.

## Measurement Results

| Criterion | Status | Evidence |
|-----------|--------|----------|
| All functional areas classified | PASS | 4 scoped areas classified in the Gap Register |
| Traceability matrix covers in-scope artifacts | PASS | Matrix maps presets and routing from PRD → feature/design → code |
| Every non-ALIGNED gap has execution coverage | PASS | `agent-e63dba18`, `agent-00a13fa0`, `agent-0f588c06` cover all gaps |
| Concern drift checked | PASS | No go-std/testing concern drift observed in reviewed code paths |

## Follow-On Beads Created

- agent-e63dba18
- agent-00a13fa0
- agent-0f588c06

ALIGN_STATUS: COMPLETE
GAPS_FOUND: 3
EXECUTION_ISSUES_CREATED: 3
MEASURE_STATUS: PASS
BEAD_ID: agent-c97e405e
FOLLOW_ON_CREATED: 3
