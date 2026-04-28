---
ddx:
  id: external-benchmarks
  bead: agent-6d6ae2f6
  created: 2026-04-27
---

# External Benchmark Adapters

This document describes the pattern for adding external benchmark suites
(TerminalBench today; SWE-bench / AgentBench / others tomorrow) to the
`cmd/bench` runner. The goal is to publish numbers comparable to what
pi / codex / claude-code report, by using each upstream benchmark's own
rubric — not a homegrown re-implementation of it.

## Design principles

1. **Use the upstream grader unmodified.** The whole point of an external
   benchmark is the published rubric; reimplementing it would produce
   incomparable scores. Adapters translate our agent surface into the
   upstream harness contract (input format, harness-output paths) and let
   the upstream verifier compute the verdict.

2. **Submodule, not vendor.** Task corpora are large, churn upstream, and
   are sometimes licensed differently. Pin a commit via git submodule
   under `scripts/benchmark/external/<benchmark>/`. Never copy tasks into
   the agent tree.

3. **Frozen subset for routine runs.** The full corpus is too expensive
   for every release. Each adapter ships a 20–50 task subset under
   `scripts/beadbench/external/<benchmark>-subset.json`. Inclusion
   criteria are documented in the file's top-level `_comment` field.
   Bumping the subset version requires a fresh baseline run.

4. **Separation of concerns.**
   - `internal/benchmark/external/<benchmark>/` — Go adapter package
     that parses task format, builds `ServiceExecuteRequest`, and
     captures harness events into the upstream's expected output shape.
   - `cmd/bench` — CLI glue (`--external=<name>`) that loads the
     subset, drives the adapter, writes per-run results.
   - `scripts/benchmark/external/<benchmark>/` — submodule with the
     upstream task corpus.
   - `scripts/benchmark/<runner>.sh` — optional wrapper that invokes
     the upstream verifier (e.g. `harbor run ...` for TerminalBench).

5. **Don't fake passing tests.** When the upstream verifier is unavailable
   (no Docker, etc.) the adapter writes the harness output and reports
   `status: ran` with no `reward` field. Only when an upstream verifier
   has dropped a `reward.txt` does the adapter report a graded outcome.

## Pattern (per benchmark)

| File / dir                                                      | Role                                                                    |
| --------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `internal/benchmark/external/<bench>/doc.go`                    | What the adapter does and (importantly) does *not* do                   |
| `internal/benchmark/external/<bench>/task.go`                   | Parse upstream task definitions into a Go `Task` type                   |
| `internal/benchmark/external/<bench>/plan.go`                   | Build `agent.ServiceExecuteRequest` + per-task timeout                  |
| `internal/benchmark/external/<bench>/trajectory.go`             | Fold `harnesses.Event` stream into upstream's expected output schema    |
| `internal/benchmark/external/<bench>/grader.go`                 | Read upstream verifier's verdict files; never reimplement scoring       |
| `cmd/bench/external_<bench>.go`                                 | CLI orchestration: subset → adapter → service.Execute → results.json    |
| `scripts/benchmark/external/<bench>/`                           | Git submodule pinned to a known-good upstream commit                    |
| `scripts/beadbench/external/<bench>-subset.json`                | Frozen subset of 20–50 task IDs with selection rule                     |
| `benchmark-results/<bench>/<run-id>/`                           | Per-run output (created by the runner; gitignored)                      |
| `docs/research/external-benchmark-baseline-<date>.md`           | Baseline matrix (one per published number)                              |

## TerminalBench reference implementation

Bead `agent-6d6ae2f6` introduced this. Highlights:

- **Task format.** TerminalBench tasks are a directory containing
  `task.yaml`, `tests/test_outputs.py`, a `Dockerfile`, and a solution
  script. Our adapter reads `task.yaml` for the instruction text and
  timeouts; the Dockerfile and tests stay upstream.
- **Output contract.** Harbor's grader expects ATIF v1.4 trajectory at
  `<out>/logs/agent/trajectory.json` and reads reward from
  `<out>/logs/verifier/reward.txt`. The adapter writes the former and
  reads the latter; it never produces reward.
- **Submodule.** `scripts/benchmark/external/terminal-bench-2/` pinned at
  `53ff2b87d621bdb97b455671f2bd9728b7d86c11`. (Note: the upstream URL
  drifted from `terminal-bench` → `terminal-bench-2`; SD-008 §1 records
  the audit. We use TB2 because it is the actively-graded dataset.)
- **Subset.** 20 tasks under `scripts/beadbench/external/termbench-subset.json`,
  same IDs as the evidence-grade comparison subset
  (`scripts/benchmark/task-subset-v2.yaml`) plus 5 cheap easy tasks for
  smoke runs.
- **Invocation.**

  ```sh
  ddx-agent-bench run \
    --external=termbench \
    --external-harness=ddx-agent \
    --external-model=openrouter/qwen/qwen3.6-plus \
    --external-max-tasks=3
  ```

- **Grading.** After the bench command finishes, run
  `scripts/benchmark/run_benchmark.sh` to invoke Harbor's verifier
  (Docker required; see SD-008 §5). The verifier drops reward.txt under
  each task's output directory; a re-run of `ddx-agent-bench run
  --external=termbench` then surfaces the reward in `results.json`.

## Adding a new benchmark

1. Open a sister bead (don't bundle multiple benchmarks in one PR).
2. Audit the upstream task format and grader contract; record findings
   in a new SD-### document.
3. Add the submodule under `scripts/benchmark/external/<name>/` pinned
   to a specific commit.
4. Implement the adapter package mirroring the TerminalBench layout above.
5. Define the frozen subset with documented inclusion criteria.
6. Wire `cmd/bench --external=<name>`.
7. Run a 3–5 task baseline on at least three arms and write up
   `docs/research/external-benchmark-baseline-<date>.md`.

## What stays out of scope

- Internal beadbench is unaffected — `--external=...` is opt-in.
- We do not republish upstream task content; only adapter glue and
  results.
- We do not score with our own metrics on top of external rewards
  unless explicitly requested. The published external rubric is the
  signal; layering anything on top changes the comparison.
