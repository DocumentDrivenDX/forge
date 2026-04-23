# Beadbench

Beadbench compares DDx `execute-bead` performance across harness/model arms on
frozen historical beads. It is separate from the Terminal-Bench runner under
`scripts/benchmark/`.

## What It Measures

Each task pins:

- a local project root
- a bead id
- the base revision before the known successful implementation
- the known-good revision
- a verifier command

The runner clones the source project into a disposable sandbox, reopens the
historical bead inside that sandbox, commits the tracker-only reopen, and runs:

```bash
ddx agent execute-bead <bead-id> --from <base-rev> --no-merge --json ...
```

The source repository and source tracker are never mutated.

## Smoke

Validate command generation without invoking any agents:

```bash
python3 scripts/beadbench/run_beadbench.py --dry-run --limit-tasks 2 --limit-arms 2
```

Run a narrow real baseline:

```bash
python3 scripts/beadbench/run_beadbench.py \
  --task helix-build-selector-readiness \
  --arm codex-gpt54 \
  --timeout-seconds 1800
```

Slice the expanded matrix before launching expensive runs:

```bash
python3 scripts/beadbench/run_beadbench.py --dry-run \
  --tier smoke --arm-tier core

python3 scripts/beadbench/run_beadbench.py --dry-run \
  --project axon --difficulty medium --arm claude-sonnet46

python3 scripts/beadbench/run_beadbench.py --dry-run \
  --category rust-auth --arm-tier frontier
```

Results are written to `benchmark-results/beadbench/run-<timestamp>-<pid>/report.json`.

Probe model-side reasoning controls before local-model evidence runs:

```bash
python3 scripts/beadbench/probe_reasoning_controls.py \
  --arm agent-vidar-omlx-qwen36-27b
```

The probe records whether each provider accepts the control field, separates
`reasoning_content`, and visibly suppresses thinking when reasoning is turned
off. Results are written to
`benchmark-results/beadbench/reasoning-probe-*.json`.

Current local evidence: on 2026-04-23, Vidar OMLX
`Qwen3.6-27B-MLX-8bit` and `Qwen3.6-35B-A3B-4bit` accepted both the legacy
`thinking` map and Qwen controls, but only Qwen
`enable_thinking`/`thinking_budget` changed observable behavior. `qwen_off`
returned a short direct answer while the no-control and `thinking`-map probes
filled the response with visible thinking text.

The same probe run found that Vidar OMLX `gpt-oss-20b-MXFP4-Q8` emits
`reasoning_content` by default but has no known budget/off control in this
matrix, and Bragi LM Studio `qwen/qwen3.6-35b-a3b` timed out for every tested
control shape at 60 seconds, including `qwen_off`.

## Evidence Rules

- Evidence-grade claims require at least three repetitions per task/arm.
- Single-run results are diagnostic only.
- Provider and harness infrastructure failures are reported separately from
  verifier failures.
- Reasoning control is part of the capability matrix: each local-model arm must
  declare an effort and have probe evidence for the wire format that enforces it.
- Changing `manifest-v1.json` task ids, base revisions, or verifier commands
  creates a new benchmark version.
- Use `tier=smoke` tasks for fast harness sanity checks, `tier=core` for the
  regular comparison set, `tier=extended` for hard/expensive complexity labels,
  and `tier=expensive` only when the required external environment is ready.

## Initial Arms

The manifest includes the requested comparison shape plus controls:

- embedded/native agent via OpenRouter GPT-5.4
- Codex GPT-5.4
- Pi with Qwen 3.6 27B-class pin
- native agent pinned to Vidar OMLX Qwen 3.6 on port 1235
- native agent pinned to Bragi LM Studio Qwen 3.6 on port 1234
- lower-effort local Qwen arms for reasoning-budget sensitivity
- Sonnet and Opus arms for hard-task separability
- GPT-5.4 Mini, Gemini Flash, and OpenRouter Haiku cost controls
- Vidar OMLX GPT-OSS 20B without Qwen reasoning controls as a non-Qwen local
  model research arm

Validate exact model strings with `ddx agent capabilities <harness>` before
evidence-grade runs.
