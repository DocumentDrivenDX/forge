---
ddx:
  id: plan-2026-04-10-terminal-bench-evidence-grade-comparison
  created: 2026-04-10
  depends_on:
    - SD-009
    - benchmark-baseline-2026-04-08
---

# Plan: Evidence-Grade Terminal-Bench Comparison

## Purpose

This plan turns the benchmark pipeline established by SD-008/SD-009 into a
credible before/after experiment for evaluating whether ForgeCode-inspired
changes improved ddx-agent's standing on Terminal-Bench.

The benchmark harness, patch tool, navigation tools, benchmark preset, and
task-tracking work are already implemented. The missing work is experimental
rigor: a real subset, one fixed harness, fixed scoring, pinned SHAs, and a
comparison memo derived from artifacts rather than anecdotes.

## Experiment Contract

The experiment claims:

> Under a fixed harness, fixed task subset, and fixed runtime/model config,
> ddx-agent after the ForgeCode-inspired changes performs better than
> ddx-agent before those changes on Terminal-Bench.

This claim is valid only if:

1. The subset uses real Terminal-Bench IDs.
2. The same harness/reporting/scoring path drives both sides.
3. The scoring rules are defined before execution.
4. The before/after SHAs are pinned before the run.
5. Provider/model/prompt/runtime settings are fixed and recorded.

## Execution Order

1. **Real subset manifest**
   Create `scripts/benchmark/task-subset-v2.yaml` with real Terminal-Bench IDs
   and a documented task selection rule.

2. **Commit-independent harness**
   Update `scripts/benchmark/run_benchmark.sh` and `scripts/benchmark/harbor_agent.py`
   so one benchmark harness can evaluate arbitrary ddx-agent binaries from
   different SHAs without changing the runner itself.

3. **Evidence-grade reporting**
   Extend the report schema so it records actual runtime metadata and enough raw
   pointers to compute the declared metrics from preserved artifacts.

4. **Scoring definitions**
   Implement scoring rules for:
   - resolved-task rate
   - clarification-question rate
   - shell anti-pattern rate
   - structured-edit success rate

5. **Pinned comparison inputs**
   Record `before_sha`, `after_sha`, subset version, benchmark config, runtime,
   provider route, exact model, and preset/system prompt in one checked-in place.

6. **Benchmark runs**
   Run the benchmark with the same harness and config on both SHAs.

7. **Comparison memo**
   Write a benchmark memo with a before/after metric table, task-level changes,
   and explicit threats to validity.

## Required Artifacts

- `scripts/benchmark/task-subset-v2.yaml`
- benchmark runner/config updates in `scripts/benchmark/`
- scoring implementation for comparison metrics
- before report artifact
- after report artifact
- comparison memo in `docs/helix/02-design/`

## Threats To Validity

- **Task selection bias**: avoid hand-picked "likely win" tasks
- **Harness drift**: the runner and scoring path must be identical across both sides
- **Model variance**: if only one run per SHA is feasible, call that out explicitly
- **Subset drift**: any subset version change creates a new baseline

## Deliverable Standard

The work is complete only when another engineer can inspect the committed
subset/config, rerun both SHAs through the same harness, and reproduce the
comparison memo from the preserved artifacts.
