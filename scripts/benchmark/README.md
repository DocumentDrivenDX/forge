# DDX Agent Benchmark Scripts

This directory contains scripts and config for running ddx-agent under
Terminal-Bench/Harbor and capturing reproducible benchmark baselines.

---

## Prerequisites

```bash
# Terminal-Bench dataset
harbor dataset pull terminal-bench/terminal-bench-2

# Docker (local runtime)
docker info  # must succeed
```

If `harbor` is not already installed, `smoke_run.sh` and `run_benchmark.sh`
now install it automatically with `uv tool install harbor`.

---

## Smoke Run (adapter validation)

Validates the Harbor adapter works end-to-end on a single task (~2 min).

```bash
# With Anthropic API key
ANTHROPIC_API_KEY=sk-... ./scripts/benchmark/smoke_run.sh

# With OpenRouter
OPENROUTER_API_KEY=sk-or-... ./scripts/benchmark/smoke_run.sh
```

**Passing criterion**:
- ddx-agent exits 0
- `trajectory.json` is valid JSON with ≥ 1 step
- `reward.txt` exists (0 or 1 — both valid for smoke)

---

## Full Benchmark Run (baseline capture)

Runs the committed 15-task subset and emits a machine-readable report.

```bash
ANTHROPIC_API_KEY=sk-... ./scripts/benchmark/run_benchmark.sh
```

The report is written to `benchmark-results/report-<TIMESTAMP>.json` and
contains git SHA, agent version, model, provider, per-task outcomes, and
the aggregate resolved-task rate.

### Commit-independent runs

You can point the same harness at a prebuilt ddx-agent binary instead of
rebuilding the current checkout:

```bash
DDX_AGENT_BINARY=/path/to/ddx-agent-linux-amd64 \
DDX_AGENT_SHA=<commit-under-test> \
./scripts/benchmark/run_benchmark.sh
```

This is the required path for evidence-grade before/after comparison across
multiple agent SHAs.

### Dry-run harness validation

Use dry-run mode to validate the staged binary/config and recorded metadata
without invoking Harbor:

```bash
DDX_BENCH_DRY_RUN=1 \
DDX_AGENT_BINARY=/path/to/ddx-agent-linux-amd64 \
DDX_BENCH_PROVIDER_MODEL=qwen/qwen3.6-plus \
./scripts/benchmark/run_benchmark.sh
```

### Comparing two baselines

```bash
# Capture baseline before a change
ANTHROPIC_API_KEY=sk-... ./scripts/benchmark/run_benchmark.sh
# ... make changes ...
# Capture new run
ANTHROPIC_API_KEY=sk-... ./scripts/benchmark/run_benchmark.sh

# Compare (jq example)
jq '{resolved_rate: .summary.resolved_task_rate, tasks: [.tasks[] | {id, outcome}]}' \
  benchmark-results/report-*.json
```

---

## Task Subset

The evidence-grade 15-task benchmark subset is defined in `task-subset-v2.yaml`.
`task-subset-v1.yaml` remains in the repo as the historical placeholder manifest
from the original benchmark design and should not be used for before/after claims.
**Do not modify task IDs without updating the version field and filing a bead.**

Subset version policy: see SD-009 §3.

## Evidence-Grade Comparison Config

The pinned comparison controls live in `evidence-grade-comparison.env`. That file
freezes the intended `before` and `after` SHAs for the original benchmark-change
window plus the shared dataset, subset, runtime, preset, and provider/model route.
The only per-run inputs that should change are:

- `DDX_AGENT_BINARY`
- `DDX_AGENT_SHA` (set to either `DDX_BENCH_BEFORE_SHA` or `DDX_BENCH_AFTER_SHA`)

---

## Harbor Adapter

`harbor_agent.py` is the `BaseInstalledAgent` Python adapter that Harbor uses
to install and run ddx-agent inside each task container. It handles:

1. **`install()`** — copies the `linux/amd64` binary and writes a provider config
2. **`run()`** — invokes `ddx-agent --json --preset benchmark -p "<task>"`
3. **`populate_context_post_run()`** — converts session JSONL to ATIF v1.4 trajectory

The adapter's provider and prompt behavior are controlled by benchmark env vars
supplied by the runner, including:

- `DDX_BENCH_PROVIDER_NAME`
- `DDX_BENCH_PROVIDER_TYPE`
- `DDX_BENCH_PROVIDER_MODEL`
- `DDX_BENCH_PROVIDER_BASE_URL`
- `DDX_BENCH_PROVIDER_API_KEY_ENV`
- `DDX_BENCH_PROVIDER_HEADERS_JSON`
- `DDX_BENCH_PRESET`
- `DDX_BENCH_SYSTEM_APPEND`

**Build the linux/amd64 binary before running**:

```bash
GOOS=linux GOARCH=amd64 go build -o scripts/benchmark/ddx-agent-linux-amd64 ./cmd/agent
```

### Credential injection

The adapter passes `ANTHROPIC_API_KEY` and `OPENROUTER_API_KEY` from the host
into the container via `get_env()`. A minimal config file is written during
`install()` that references `${ANTHROPIC_API_KEY}` — ddx-agent's config loader
expands env vars at load time.

If the expected key env var is unset, the benchmark scripts now fall back to the
matching provider entry in the host ddx-agent config (`.agent/config.yaml` or
`~/.config/agent/config.yaml`) and export the discovered `api_key` before
invoking Harbor.

---

## Thresholds (from SD-009 §5)

| Metric | Regression floor | Aspirational |
|--------|-----------------|-------------|
| Resolved-task rate | ≥ 55% | ≥ 70% |
| Clarification-question rate | < 10% | < 5% |
| Shell anti-pattern rate | < 30% of bash calls | < 10% |
| Structured-edit success rate | ≥ 70% | ≥ 90% |

---

## References

- `SD-008-terminal-bench-integration.md` — Harbor/Terminal-Bench integration audit
- `SD-009-benchmark-mode.md` — Benchmark mode, metrics, thresholds
- `benchmark-baseline-2026-04-08.md` — Initial baseline characterization
