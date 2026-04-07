# Alignment Review: SD-007 — Provider Import

**Review Date**: 2026-04-07
**Scope**: SD-007 config import from pi and opencode
**Status**: draft
**Review Epic**: forge-600a7be2
**Primary Governing Artifact**: docs/helix/02-design/solution-designs/SD-007-provider-import.md

## Scope and Governing Artifacts

### Scope

- picompat package (pi config readers)
- occompat package (opencode config readers)
- forge import CLI command
- imported_from metadata and drift detection
- zero-config discovery notice

### Governing Artifacts

- [docs/helix/02-design/solution-designs/SD-007-provider-import.md](SD-007-provider-import.md) — primary spec
- [docs/helix/02-design/solution-designs/SD-005-provider-config.md](SD-005-provider-config.md) — config schema

## Intent Summary

- **Vision**: Users of pi and opencode should be able to import their existing LLM provider configurations into forge without duplication.
- **Requirements**: Import from pi (~/.pi/agent/auth.json, settings.json, models.json) and opencode (~/.local/share/opencode/auth.json, opencode.json). Support diff/merge modes, secret redaction, drift detection, and zero-config discovery.
- **Features / Stories**: Per SD-007 — import CLI commands, two-source merge for pi, secret handling to user config, drift detection with hash tracking.
- **Architecture / ADRs**: Uses existing config.Load() pattern, writes to ~/.config/forge/config.yaml by default.
- **Technical Design**: SD-007 fully specifies the design including package structure, CLI commands, metadata schema, and implementation plan.
- **Test Plans**: Not yet specified for this feature.
- **Implementation Plan**: 8 tasks in SD-007, tasks 1-4 (readers), task 5 (CLI), task 6 (discovery), task 7 (drift), task 8 (env var fallback already exists in config.Load()).

## Planning Stack Findings

| Finding | Type | Evidence | Impact | Review Issue |
|---------|------|----------|--------|-------------|
| SD-007 is complete design | N/A | SD-007 fully specified | N/A | N/A |
| SD-007 not referenced in PRD | missing-link | PRD doesn't mention import feature | Low — feature was designed post-PRD | N/A |

## Implementation Map

- **Topology**: No packages exist yet. Target: `forge/picompat/`, `forge/occompat/`.
- **Entry Points**: New subcommands in `cmd/forge/main.go`: `forge import pi`, `forge import opencode`.
- **Test Surfaces**: No tests exist yet. Need unit tests for each reader and translator.
- **Unplanned Areas**: None — SD-007 covers the full scope.

## Acceptance Criteria Status

| Story / Feature | Criterion | Test Reference | Status | Evidence |
|-----------------|-----------|----------------|--------|----------|
| SD-007 Task 1 | Pi auth.json reader | none | UNIMPLEMENTED | No picompat/auth.go |
| SD-007 Task 2 | Pi models.json reader | none | UNIMPLEMENTED | No picompat/models.go |
| SD-007 Task 3 | Pi settings.json reader + translate | none | UNIMPLEMENTED | No picompat/settings.go, picompat/translate.go |
| SD-007 Task 4 | OpenCode auth + config reader | none | UNIMPLEMENTED | No occompat/ |
| SD-007 Task 5 | forge import CLI command | none | UNIMPLEMENTED | No import subcommand in main.go |
| SD-007 Task 6 | Zero-config discovery notice | none | UNIMPLEMENTED | No discovery logic |
| SD-007 Task 7 | Drift detection | none | UNIMPLEMENTED | No ImportMetadata, no hash tracking |
| SD-007 Task 8 | Standard env var fallback | none | IMPLEMENTED | config.Load() has applyEnvOverrides() |

## Gap Register

| Area | Classification | Planning Evidence | Implementation Evidence | Resolution Direction | Issue |
|------|----------------|-------------------|------------------------|----------------------|-------|
| picompat/auth.go | NOT_IMPLEMENTED | SD-007 task 1 | No file exists | code-to-plan | forge-640ef3be |
| picompat/models.go | NOT_IMPLEMENTED | SD-007 task 2 | No file exists | code-to-plan | forge-640ef3be |
| picompat/settings.go + translate.go | NOT_IMPLEMENTED | SD-007 task 3 | No files exist | code-to-plan | forge-640ef3be |
| occompat/ package | NOT_IMPLEMENTED | SD-007 task 4 | No package exists | code-to-plan | forge-c8d7eb45 |
| forge import CLI | NOT_IMPLEMENTED | SD-007 task 5 | No subcommand in main.go | code-to-plan | forge-bc840f36 |
| imported_from metadata | NOT_IMPLEMENTED | SD-007 Config Schema Additions | Config struct missing ImportMetadata | code-to-plan | forge-a8e99614 |
| drift detection | NOT_IMPLEMENTED | SD-007 Drift Detection section | No hash tracking, no notice | code-to-plan | forge-a8e99614 |
| zero-config discovery | NOT_IMPLEMENTED | SD-007 Zero-Config Discovery | No discovery logic | code-to-plan | forge-5bc78ae2 |
| standard env var fallback | ALIGNED | SD-007 Standard Env Var Fallback | config.Load() has applyEnvOverrides() | N/A | N/A |

### Quality Findings

None — feature not yet implemented.

## Traceability Matrix

| Vision | Requirement | Feature/Story | Arch/ADR | Design | Tests | Impl Plan | Code Status | Classification |
|--------|-------------|---------------|----------|--------|-------|-----------|-------------|----------------|
| Import from pi/opencode | No config duplication | SD-007 | N/A | SD-007 | none | Tasks 1-8 | Not started | NOT_IMPLEMENTED |

## Review Issue Summary

| Issue ID | Area | Classification |
|----------|------|---------------|
| forge-640ef3be | picompat package | NOT_IMPLEMENTED |
| forge-c8d7eb45 | occompat package | NOT_IMPLEMENTED |
| forge-bc840f36 | forge import CLI | NOT_IMPLEMENTED |
| forge-a8e99614 | imported_from + drift | NOT_IMPLEMENTED |
| forge-5bc78ae2 | zero-config discovery | NOT_IMPLEMENTED |

## Execution Issues Generated

| Issue ID | Type | Labels | Goal | Dependencies | Verification |
|----------|------|--------|------|--------------|-------------|
| forge-NEW | task | helix,area:lib,phase:build | Implement picompat package (auth, models, settings, translate) | — | Unit tests pass |
| forge-NEW | task | helix,area:lib,phase:build | Implement occompat package (auth, config, translate) | — | Unit tests pass |
| forge-NEW | task | helix,area:cli,phase:build | Implement forge import CLI command | picompat, occompat | Functional tests pass |
| forge-NEW | chore | helix,area:lib,phase:build | Add ImportMetadata to config.Config | picompat | Config round-trips correctly |
| forge-NEW | task | helix,area:lib,phase:build | Implement drift detection (hash tracking, notice) | ImportMetadata | Manual verification |
| forge-NEW | task | helix,area:cli,phase:build | Implement zero-config discovery notice | forge import | Manual verification |

## Issue Coverage

| Gap / Criterion | Covering Issue | Status |
|-----------------|----------------|--------|
| picompat package | forge-NEW | covered |
| occompat package | forge-NEW | covered |
| forge import CLI | forge-NEW | covered |
| imported_from metadata | forge-NEW | covered |
| drift detection | forge-NEW | covered |
| zero-config discovery | forge-NEW | covered |
| standard env var fallback | N/A | ALIGNED |

## Execution Order

1. **forge-picompat** — picompat package (auth, models, settings, translate)
2. **forge-occompat** — occompat package (auth, config, translate)
3. **forge-import-cli** — forge import CLI command
4. **forge-import-metadata** — Add ImportMetadata to config.Config
5. **forge-drift** — Implement drift detection
6. **forge-discovery** — Implement zero-config discovery

**Critical Path**: picompat → occompat → import-cli → metadata → drift → discovery
**Parallel**: picompat and occompat can be implemented in parallel
**Blockers**: None for initial implementation

## Open Decisions

| Decision | Why Open | Governing Artifacts | Recommended Owner |
|----------|----------|---------------------|-------------------|
| Task ordering for metadata | ImportMetadata could be added before or after reader packages | SD-007 Config Schema Additions | Implementor choice |

## Queue Health and Exhaustion Assessment

- **Queue health**: 6 new execution issues created for SD-007 tasks
- **Exhaustion**: All SD-007 implementation tasks have corresponding execution issues
- **Dependencies**: Tasks 1-4 (readers) are independent; task 5 (CLI) depends on readers; tasks 6-7 (discovery, drift) depend on CLI

## Measurement Results

| Criterion | Status | Evidence |
|-----------|--------|----------|
| All functional areas classified | PASS | 8 areas classified (6 NOT_IMPL, 1 ALIGNED, 1 existing) |
| Traceability matrix complete | PASS | Matrix covers SD-007 |
| Issue coverage for non-ALIGNED | PASS | 6 execution issues for 6 gaps |
| Concern drift | N/A | Feature not implemented |

## Follow-On Beads Created

None — all gaps have execution issues created.

---

ALIGN_STATUS: COMPLETE
GAPS_FOUND: 6
EXECUTION_ISSUES_CREATED: 6
MEASURE_STATUS: PASS
BEAD_ID: forge-600a7be2
FOLLOW_ON_CREATED: 0
