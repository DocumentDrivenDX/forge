# Agent-vs-Pi Harness Parity Tracker

Append-only summary across all dated `AR-YYYY-MM-DD-agent-vs-pi-*` measurements. Goal: track our native agent harness against pi on shared backings until parity is achieved on real coding tasks.

Governing bead: [`agent-f43d1ed2`](../../.ddx/beads.jsonl). Depends on the methodology established in [`AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md`](AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md).

## Match criterion

Declare **matched** when **all** of the following hold across **3 consecutive measurements**:

1. **Success rate.** Agent paired-success rate ≥ pi paired-success rate.
2. **Cost-per-success.** Agent cost-per-success ≤ **1.2 ×** pi cost-per-success.

Once matched, taper cadence (monthly check-ins instead of weekly).

## Headline column: agent-vs-pi delta

The `Δ` column is success-rate (agent − pi) in **percentage points**. Goal: Δ → 0 or positive, **with stability** across runs. A single positive Δ followed by a regression doesn't count.

## Schedule

Currently **manual on demand**. Per the bead scope, a recurring `/schedule` cadence is deferred until **N ≥ 2 measurements** exist — need data to confirm methodology before automating. After N=2, switch to weekly Monday morning runs that produce a new dated AR doc and append a row here.

## Results

| Date | Backing model | N | Agent success | Pi success | Δ (pp) | Mean cost ratio (agent/pi) | Winner | AR doc |
|------|---------------|---|---------------|------------|--------|----------------------------|--------|--------|
| _pending_ | openrouter qwen/qwen3.6-plus | 1 | _pending_ | _pending_ | _pending_ | _pending_ | _pending_ | (run in flight 2026-04-28) |

The first row is the run kicked off while this doc lands — paired beadbench preflight against `agent-openrouter-qwen36plus` and `pi-openrouter-qwen36plus` arms on the `agent-beadbench-preflight` task. Will be filled in upon completion.

Once the prior dated AR doc ([AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36](AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md)) §3-5 land with their per-task table + aggregates, that measurement appears as a separate row (different backing — local omlx, not openrouter).

## How to add a row

1. **Run the paired benchmark.** From repo root:

    ```bash
    python3 scripts/beadbench/run_beadbench.py \
      --arm agent-<harness-pair-prefix>-<backing> \
      --arm pi-<harness-pair-prefix>-<backing> \
      --task agent-beadbench-preflight
    ```

   Each arm should pin the same model on the same provider + same surface. Ensure both arms' providers are reachable before starting (`/v1/models` returns 200 on each).

2. **Inspect the report.** The benchmark writer emits `benchmark-results/beadbench/run-<ts>/report.json`. For each arm, extract:
   - `status` (success / execution_failed / timeout)
   - `cost_usd` (when present — local arms may report 0)
   - `tokens.total`
   - `verify.status` (pass / fail / skipped)

3. **Compute aggregates.**
   - **Success rate** = (verified-pass count) / (paired runs). With N=1 paired runs, this is 0.0 or 1.0.
   - **Cost-per-success** = sum(cost_usd over successes) / (success count). When cost is 0 or unknown, mark cost ratio as `n/a` and document why.
   - **Δ (pp)** = (agent success rate − pi success rate) × 100.

4. **Write a dated AR doc.** Path: `docs/research/AR-YYYY-MM-DD-agent-vs-pi-<backing-shorthand>.md`. Mirror the structure of [AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md](AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md): §1 methodology, §2 provider config evidence, §3 per-task table, §4 aggregates + winner, §5 top-3 gaps.

5. **Append the row here.** One row per measurement, in chronological order. Reference the AR doc by basename in the last column.

6. **Re-evaluate the match criterion.** If three consecutive measurements satisfy both clauses (agent success ≥ pi, cost ratio ≤ 1.2×), declare matched and taper cadence.

## Backing-model rotation

First several measurements use **openrouter qwen/qwen3.6-plus** per the initial bead `agent-3663e287`. This is the same backing pi defaults to for non-Anthropic flows on this LAN, so it isolates harness behavior from provider-side variation.

After matching is established for the qwen3.6-plus backing, optionally extend to additional shared backings to test whether the gap is model-dependent:

- `claude-opus-4-6` (if pi exposes it via Anthropic credentials)
- `gpt-5.5` (if pi exposes it via OpenAI credentials)
- `Qwen3.6-27B-MLX-8bit` on omlx (covered by AR-2026-04-26 separately)
- `qwen/qwen3.6-27b` on bragi LM Studio (added via beadbench arms today)

Each new backing starts a separate row series. Don't conflate backings within a run.

## Cross-references

Existing dated AR docs in chronological order:

- [AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md](AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md) — paired vidar omlx Qwen3.6-27B-MLX-8bit study; methodology + pi-config evidence complete (§1-2), per-task table + aggregates pending (§3-5).

Append new entries above, newest first.
