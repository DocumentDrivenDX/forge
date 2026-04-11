---
ddx:
  id: SD-009
  bead: agent-82042311
  created: 2026-04-08
  depends_on:
    - SD-008   # Terminal-Bench/Harbor integration audit
    - benchmark-baseline-2026-04-08
---

## Reference Implementations

- **ForgeCode** (patch-based editing): `https://github.com/antinomyhq/forgecode`
  - Core patch logic: `crates/forge_services/src/tool_services/fs_patch.rs`
  - Clone locally: `git clone --depth 1 https://github.com/antinomyhq/forgecode.git /tmp/forgecode-study`
  - Key approach: search-and-replace with exact match, line-ending normalization,
    multiple operation types (replace, prepend, append, swap), snapshot coordination.
  - Clone locally: `git clone https://github.com/antinomyhq/forgecode`
# Solution Design: SD-009 — DDX Agent Benchmark Mode and Terminal-Bench Evaluation Plan

**Bead**: agent-82042311 (Specify ddx-agent benchmark mode and Terminal-Bench evaluation plan)
**Type**: Design spec
**Status**: Complete — grounded in SD-008 (interface audit) and the 2026-04-08 baseline

---

## Summary

This document specifies how ddx-agent is evaluated under Terminal-Bench/Harbor,
defines the benchmark-mode runtime/preset, commits a fixed benchmark task subset,
defines a smoke-task workflow, and sets measurable metrics with thresholds grounded
in the baseline characterization captured in `benchmark-baseline-2026-04-08.md`.

---

## 1. Terminal-Bench / Harbor Integration Path for DDX Agent

See SD-008 for the full audit. Summary for this spec:

**Integration type**: `BaseInstalledAgent` — ddx-agent is installed as a static
`linux/amd64` binary inside each Terminal-Bench task container. A Python adapter
(tracked in bead `agent-a3ce467a`) handles install, invocation, and ATIF trajectory
conversion.

**Invocation in container**:
```bash
ddx-agent --json --preset benchmark -p "<task_instruction>"
```

**Exit contract**:
- Exit code 0 = agent attempted the task (Harbor reads reward from test suite)
- Exit code non-zero = trial failure (Harbor marks task as failed)
- Terminal-Bench verifier runs `pytest` against the modified workspace after exit

**No interface changes needed to the ddx-agent CLI** for the basic installed-agent path.
The `--preset benchmark` preset is a new addition (bead `agent-5f35fdeb`) that suppresses
interactive behavior and reduces shell anti-patterns.

---

## 2. Benchmark-Mode Runtime / Preset Decision

### Decision: Add a `benchmark` preset; no separate binary

**Rationale**: The 2026-04-08 baseline (T6) showed two categories of behavior that
need tuning for unattended evaluation:

1. **Shell anti-patterns** (ls, find) for directory exploration when structured tools exist
2. **Edit tool format confusion** once in 6 tasks

These are prompt-level issues, not architectural ones. A dedicated `benchmark` system
prompt preset addresses both without introducing a separate binary or build variant.

**What the `benchmark` preset adds over `codex`**:

| Behavior | codex | benchmark |
|----------|-------|-----------|
| Discourages ls/find/cat for navigation | No | Yes — explicit rule |
| Edit tool format reminder | Implicit | Explicit example |
| No clarification questions | Not stated | Stated explicitly |
| Non-interactive completion | Implied | Stated: never ask, always attempt |
| Tool-first navigation | Implied | Explicit preference order |

**What stays the same**: Tool set, iteration limit, provider config, JSON output format.
The `--json` flag is still required separately (harness flag, not preset behavior).

**Implementation bead**: `agent-5f35fdeb` (Add benchmark-mode preset and non-interactive
completion behavior)

---

## 3. Fixed Benchmark Task Subset

### Subset Commitment

The benchmark subset is versioned and committed to the repo as a YAML manifest at:
`scripts/benchmark/task-subset-v1.yaml`

This file pins specific Terminal-Bench task IDs. The set is small enough to run in CI
or manually, but representative enough to detect regressions.

**v1 subset** (15 tasks, grouped by capability area):

```yaml
# scripts/benchmark/task-subset-v1.yaml
# Fixed benchmark subset for ddx-agent — v1 (2026-04-08)
# Do not modify without updating the version and filing a bead.
version: "1"
captured: "2026-04-08"
dataset: terminal-bench/terminal-bench-2

tasks:
  # File navigation and read (2 tasks)
  - id: tb2-read-and-describe
    category: navigation
    rationale: Baseline read capability; should always pass
  - id: tb2-find-and-summarize
    category: navigation
    rationale: Multi-file reading without bash anti-patterns

  # Targeted edits (3 tasks)
  - id: tb2-rename-symbol
    category: edit
    rationale: Covers edit tool single-occurrence correctness
  - id: tb2-add-function-signature
    category: edit
    rationale: Edit with import addition (multi-edit batching)
  - id: tb2-fix-type-error
    category: edit
    rationale: Edit with type signature mismatch

  # Error handling and guards (2 tasks)
  - id: tb2-add-error-guard
    category: edit
    rationale: Add guard clauses (mirrors baseline T3)
  - id: tb2-propagate-error
    category: edit
    rationale: Multi-site edit: add error return through call chain

  # Test writing (2 tasks)
  - id: tb2-write-unit-test
    category: test
    rationale: Write a test case from scratch
  - id: tb2-fix-failing-test
    category: test
    rationale: Diagnose + fix a failing test (multi-round)

  # Shell / bash (2 tasks)
  - id: tb2-rewrite-script
    category: bash
    rationale: Bash refactoring; shell anti-pattern detection
  - id: tb2-automate-task
    category: bash
    rationale: Write a new shell script from spec

  # Multi-file navigation (2 tasks)
  - id: tb2-cross-file-refactor
    category: navigation
    rationale: Read multiple files, apply consistent edits
  - id: tb2-trace-call-chain
    category: navigation
    rationale: Navigate a call chain without bash exploration

  # Compound (2 tasks)
  - id: tb2-implement-feature
    category: compound
    rationale: Read + edit + test in one task (hardest category)
  - id: tb2-debug-and-fix
    category: compound
    rationale: Identify and fix a bug via bash + edit
```

**Note on task IDs**: The IDs above are representative placeholders that map to the
actual Terminal-Bench v2 task catalog. The `agent-a3ce467a` bead that implements the
adapter will validate and pin the real task IDs from the live dataset.

**Evidence-grade comparison subset**: The first real-ID manifest is
`scripts/benchmark/task-subset-v2.yaml`. It is the correct subset for any
before/after claim. `task-subset-v1.yaml` is retained only as a historical design
artifact from the initial benchmark plan.

### Subset Versioning Policy

- Subset is frozen once pinned. Threshold regressions must be investigated, not
  worked around by swapping tasks.
- New subset versions (`v2`, `v3`) may be created when tasks are deprecated by Terminal-Bench,
  but are treated as a new baseline (fresh characterization run required).
- Expanding the subset requires a new bead and threshold calibration.

---

## 4. Smoke-Task Workflow

The smoke workflow validates that the ddx-agent adapter runs to completion on a single
task before a full benchmark run. It should take under 2 minutes.

### Smoke-Run Steps

```bash
# Step 1: Build linux/amd64 binary
GOOS=linux GOARCH=amd64 go build -o dist/ddx-agent-linux-amd64 ./cmd/ddx-agent

# Step 2: Run one task from the fixed subset
harbor run \
  --dataset terminal-bench/terminal-bench-2 \
  --agent ddx-agent \
  --task-id tb2-read-and-describe \
  --runtime docker \
  --env ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY}"

# Step 3: Verify
TRIAL=$(ls -t ~/.harbor/jobs/*/trials/ | head -1 | xargs dirname)
cat "${TRIAL}/verifier/reward.txt"         # expect: 0 or 1 (both are valid; 1 is pass)
cat "${TRIAL}/agent/trajectory.json" | python3 -m json.tool > /dev/null  # valid JSON
echo "Smoke run complete"
```

**Passing criterion for smoke run**:
- ddx-agent exits with code 0 (no harness crash)
- `trajectory.json` is valid JSON with at least 1 step
- `reward.txt` exists (contains `0` or `1`)

A smoke run that produces `reward.txt = 0` is not a failure of the smoke workflow —
it means the agent didn't solve the task, which is separate from the harness working.

### Smoke Task Registration (scripts/benchmark/)

The adapter and smoke workflow scripts live in `scripts/benchmark/`:

```
scripts/benchmark/
├── harbor_agent.py          # BaseInstalledAgent adapter (agent-a3ce467a)
├── task-subset-v1.yaml      # Historical placeholder subset from initial design
├── task-subset-v2.yaml      # Real-ID evidence-grade comparison subset
├── smoke_run.sh             # Smoke run script
└── README.md                # How to run benchmarks
```

---

## 5. Metrics and Thresholds (Grounded in Baseline)

### Primary Metrics

| Metric | Definition | Collection |
|--------|------------|------------|
| Resolved-task rate | Fraction of subset tasks where `reward.txt = 1` | Harbor job results |
| Clarification-question rate | Fraction of trials where agent output contains a question before making tool calls | Trajectory analysis |
| Shell anti-pattern rate | Fraction of bash tool calls that are navigation anti-patterns (ls, find, cat) | Trajectory analysis |
| Structured-edit success rate | Fraction of edit tool invocations that succeed (non-error) | Trajectory analysis |

### Thresholds (Grounded in Baseline)

The 2026-04-08 baseline on 6 tasks with `claude-haiku-4-5` via OpenRouter:

| Metric | Baseline (6-task pilot) | v1 Regression Floor | Aspirational Target |
|--------|------------------------|--------------------|--------------------|
| Resolved-task rate | 100% (6/6 simple tasks) | ≥ 55% on v1 subset | ≥ 70% |
| Clarification-question rate | 0% | < 10% | < 5% |
| Shell anti-pattern rate | 50% of bash calls (T6 only) | < 30% of bash calls | < 10% |
| Structured-edit success rate | 75% (3/4 attempts) | ≥ 70% | ≥ 90% |

**Why these thresholds**:

- **55% resolved-task floor**: The pilot used simple tasks; Terminal-Bench v2 is harder.
  The PRD target is 70% for routine tasks on local models. 55% is the "something is
  severely broken" floor; 70% is the goal.
- **< 10% clarification rate**: Terminal-Bench tasks are non-interactive. The pilot
  showed 0%; the 10% floor accounts for edge cases on ambiguous instructions.
- **< 30% shell anti-pattern rate**: The pilot showed 50% (2/4 bash calls in T6 were
  anti-patterns), but all were navigation patterns that a benchmark-mode preset
  eliminates. After the preset is in place, this should reach < 10%. 30% is a floor
  for detecting regression before the preset is implemented.
- **≥ 70% edit success rate**: The 75% pilot value included one format-confusion failure.
  With the edit tool description clarified in the benchmark preset, this should reliably
  exceed 85%. 70% is the regression floor.

### Secondary Metrics (tracked but not thresholds)

| Metric | Collection | Purpose |
|--------|------------|---------|
| Avg wall-clock time per task | Harbor trial_result.json | Detect runaway loops |
| Avg tool calls per resolved task | Trajectory analysis | Efficiency tracking |
| Avg input tokens per task | Trajectory analysis | Cost projection |
| Task timeout rate | Harbor | Detect iteration limit issues |

---

## 6. Micro-Evals That Gate Regressions

These unit-level evals run in CI without Harbor and catch common failure modes:

| Micro-eval | What it tests | Passing criterion |
|------------|--------------|-------------------|
| Edit format correctness | Agent uses `old_string`/`new_string` not `edits[]` | edit tool succeeds on first attempt for simple rename |
| No-bash file read | Agent uses read tool, not bash ls/cat, to inspect a known file | 0 bash calls on pure read task |
| Error recovery | Agent recovers from an edit tool returning "not found" error | Task resolves within 2 extra turns |
| Clarification gate | Agent does not ask a question when the task is unambiguous | No `?` in first output on simple task |

Micro-evals are run via the existing virtual/dictionary provider (bead `agent-483477c7`)
to avoid live model costs.

---

## 7. Evidence-Grade Comparative Protocol

The original SD-009 deliverables established the benchmark harness, fixed subset
shape, baseline characterization, and benchmark-critical tools. A credible claim
that ForgeCode-inspired changes improved ddx-agent's Terminal-Bench standing
requires a stricter comparative protocol than the original pilot baseline.

### Comparative claim

The claim under test is:

> ForgeCode-inspired harness and tooling changes improved ddx-agent's measured
> performance on a fixed Terminal-Bench subset under one fixed harness and one
> fixed runtime/model configuration.

### Required controls

Any before/after comparison MUST satisfy all of the following:

1. **Pinned task subset**: comparison runs use a real-ID subset manifest
   (`task-subset-v2.yaml` or later). Placeholder-ID manifests are not valid for
   evidentiary comparison.
2. **Pinned SHAs**: the exact `before_sha` and `after_sha` are chosen before the
   run and recorded in the benchmark artifact.
3. **One harness for both sides**: the benchmark runner, Harbor adapter, scoring
   code, and report schema are identical across the before/after runs. Do not
   compare "old agent + old runner" versus "new agent + new runner".
4. **Pinned runtime configuration**: provider route, exact model, preset/system
   prompt, tool surface, Harbor runtime, and dataset version are fixed across
   both sides and captured in a checked-in config artifact.
5. **Predeclared scoring rules**: metric definitions are written before the run,
   not inferred after inspecting results.

### Evidence-grade execution order

1. Create a real-ID benchmark subset manifest (`task-subset-v2.yaml`) and record
   the task selection rule.
2. Upgrade the benchmark harness so one runner can execute arbitrary ddx-agent
   binaries from different SHAs while preserving identical reporting and scoring.
3. Extend the report and scoring pipeline to capture actual runtime metadata and
   compute the declared comparison metrics.
4. Pin the comparison SHAs and checked-in benchmark config.
5. Run the benchmark on the `before_sha`.
6. Run the benchmark on the `after_sha`.
7. Compare the two reports and publish a memo from the recorded artifacts.

### Metric definitions for comparison runs

The comparison memo MUST report these metrics using predeclared scoring rules:

| Metric | Operational definition |
|--------|------------------------|
| Resolved-task rate | Fraction of tasks where Harbor reward is exactly `1` |
| Clarification-question rate | Fraction of trials where the first agent message before tool use asks the user for clarification or defers pending user input |
| Shell anti-pattern rate | Fraction of bash tool calls used for repository/file navigation when structured tools should suffice (`ls`, `find`, `cat`, ad hoc `grep`/`rg` discovery, similar shell-only exploration) |
| Structured-edit success rate | Fraction of structured edit/patch tool calls that return success rather than error |

The implementation MUST encode these definitions in scoring code or fixtures.
They are not left to memo-time interpretation.

### Validity notes

- Creating `task-subset-v2.yaml` is required because `task-subset-v1.yaml`
  contains representative placeholder IDs and is therefore not a valid
  benchmark subset for before/after evidence.
- If repeated runs are not feasible, the comparison memo MUST explicitly state
  that the results are single-run and therefore subject to model variance.
- If a subset version changes, the run establishes a new baseline and MUST NOT
  be compared numerically against older subset versions without that caveat.

---

## 8. Implementation Order

| Bead | Dependency | What it delivers |
|------|-----------|-----------------|
| `agent-a3ce467a` | SD-008 | Harbor Python adapter + smoke-run script |
| `agent-5f35fdeb` | This doc (§2) | `benchmark` preset |
| `agent-4dde1671` | This doc (§6) | Navigation tools + micro-evals |
| `agent-78c86322` | adapter + baseline | Automated baseline capture in CI |
| `agent-8e46e7e2` | This doc (§6) | Structured patch / exact-match edit evals |
| `agent-77d95bdc` | This doc (§6) | Task-tracking tools and planning evals |

---

## Open Questions

- [ ] **Real Terminal-Bench task IDs**: The `task-subset-v1.yaml` uses placeholder IDs.
  An evidence-grade comparison requires a new real-ID manifest (`task-subset-v2.yaml`)
  with a documented task selection rule.
- [ ] **Cloud runtime vs local Docker**: Harbor's cloud runtimes (Modal, Daytona) add
  latency. Initial evaluation should use local Docker (`--runtime docker`).
- [ ] **Local model evaluation**: The baseline used `claude-haiku-4-5` (cloud). A local
  model baseline (qwen3.5-27b via LM Studio) is needed to measure the "70% of routine
  tasks succeed on local 7B+" PRD success metric. This is a separate baseline run.
- [ ] **Commit-independent harness**: the benchmark runner and scoring path currently
  live in the agent repo and must be made commit-independent before a before/after
  comparison can be treated as evidence rather than mixed harness+agent drift.
