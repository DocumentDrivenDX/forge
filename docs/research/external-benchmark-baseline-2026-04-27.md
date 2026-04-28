---
ddx:
  id: external-benchmark-baseline-2026-04-27
  bead: agent-6d6ae2f6
  created: 2026-04-27
---

# External Benchmark Baseline — TerminalBench, 2026-04-27

This is the v1 baseline for the TerminalBench external-benchmark adapter
introduced in bead `agent-6d6ae2f6`. Three task IDs across three arms.
The intent is to demonstrate the adapter wiring end-to-end and produce
the first comparable-on-rubric numbers — not to make a leaderboard claim.
Per `MEMORY.md` benchmark philosophy, the goal here is harness signal,
not chasing a tail.

## Run conditions

| Field             | Value                                                                                  |
| ----------------- | -------------------------------------------------------------------------------------- |
| Adapter           | `internal/benchmark/external/termbench`                                                |
| Subset            | `scripts/beadbench/external/termbench-subset.json` v1                                  |
| Dataset           | terminal-bench-2 @ `53ff2b87d621bdb97b455671f2bd9728b7d86c11`                          |
| Verifier          | Harbor (`harbor run --runtime docker`) — runs the upstream `tests/test_outputs.py`     |
| Tasks (3)         | `hello-world`, `fizzbuzz`, `cancel-async-tasks` — one easy smoke + one easy + one hard |
| Date              | 2026-04-27                                                                              |
| Repro recipe      | `ddx-agent-bench run --external=termbench --external-max-tasks=3 --external-model=…`   |

## Arms

| Arm           | Harness        | Model                                                  | Notes                                                                                                                                |
| ------------- | -------------- | ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------ |
| ddx-agent     | `ddx-agent`    | `openrouter/qwen/qwen3.6-plus`                          | Our agent on the cheapest competitive Qwen route (per north-star goal: Qwen ≈ frontier on easy/medium).                              |
| codex         | `codex`        | `openai/gpt-5.4`                                        | OpenAI codex CLI; reference frontier for comparison.                                                                                  |
| claude-code   | `claude-code`  | `anthropic/claude-sonnet-4.6`                           | Anthropic CLI; second frontier reference, also our common dev harness.                                                               |

`pi` is intentionally deferred until after these three are stable, per
the bead spec.

## Results

> NOTE: This v1 baseline document is published with the adapter PR so the
> wiring is reviewable before the verifier-side run. The numbers below
> are placeholders representing the matrix shape — they are filled in by
> a follow-up commit once `scripts/benchmark/run_benchmark.sh` has scored
> the trajectories produced by `ddx-agent-bench run --external=termbench`.
> Don't read scores off this table until the dated update lands; the
> alternative would be faking pass rates, which the bead explicitly
> rejects.

| Arm           | hello-world | fizzbuzz   | cancel-async-tasks | Pass rate (3) |
| ------------- | ----------- | ---------- | ------------------ | ------------- |
| ddx-agent     | _pending_   | _pending_  | _pending_          | _pending_     |
| codex         | _pending_   | _pending_  | _pending_          | _pending_     |
| claude-code   | _pending_   | _pending_  | _pending_          | _pending_     |

Each cell will be `pass` (reward=1), `fail` (reward=0), or `error`
(harness/grader fault — does not count toward pass rate).

## Threats to validity (v1 sample)

- 3 tasks is too small to draw quantitative conclusions; this is a
  smoke matrix, not a leaderboard.
- `cancel-async-tasks` is `hard` per the upstream tag; expect ≤ 50%
  pass for all three arms (consistent with published TB2 numbers).
- Token budgets and cost caps differ across arms because each upstream
  CLI imposes its own — that variance is part of "comparable to what
  pi/codex/claude-code publish", not a bug.
- Container runtime is single-host Docker (linux/amd64). Cloud-runtime
  variance (Daytona, Modal, E2B, Runloop) is not captured here.

## Next steps

- Once verifier outputs are in, replace `_pending_` cells and add a
  short prose summary of what the matrix says about Qwen-vs-frontier
  parity for `hard`-tier tasks.
- Add `pi` as a fourth arm; file as a follow-up.
- Expand to the full 20-task subset once the smoke matrix is green
  end-to-end.

## See also

- `docs/helix/02-design/external-benchmarks.md` — adapter pattern doc.
- `docs/helix/02-design/solution-designs/SD-008-terminal-bench-integration.md` — original integration audit.
- `docs/helix/02-design/plan-2026-04-10-terminal-bench-evidence-grade-comparison.md` — sister evidence-grade comparison plan.
