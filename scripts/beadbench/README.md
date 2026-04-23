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

## Preflight

Before any real dispatch the runner validates the environment and the
selected tasks. Run preflight in isolation with:

```bash
python3 scripts/beadbench/run_beadbench.py --preflight
```

Preflight checks:

- `ddx agent execute-bead --help` advertises every flag beadbench sends
  (`--from`, `--no-merge`, `--json`, `--project`, `--harness`,
  `--provider`, `--model`, `--model-ref`, `--effort`, `--context-budget`).
- Each selected task's `project_root` is an existing git repository.
- Each task's `base_rev` and `known_good_rev` resolve to commits in that
  repository.
- Each task's `bead_id` is known to `ddx bead show` from within
  `project_root`.

A full run performs preflight automatically and refuses to dispatch when
any check fails. Use `--skip-preflight` to bypass (for offline debugging
only). Preflight output is written to `<run-dir>/preflight.json`.

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

## Timeout evidence

When `--timeout-seconds` trips before `execute-bead` returns, the runner still
records what it has:

- `stdout.txt` / `stderr.txt` hold the partial streams captured before the
  child was killed.
- `execute-result.json` is written whenever the trailing stdout contained a
  recoverable JSON object; its `preserve_rev`/`result_rev` (if any) is lifted
  into `result.timeout.preserve_rev`.
- `timeout-sandbox-state.json` records the sandbox HEAD, commits ahead of
  `base_rev`, `git status --short`, tracked diff names, and any
  `refs/execute-bead/preserve/*` refs left behind.
- `result.timeout.progress_class` buckets the run as `no_output`,
  `read_only_progress`, or `write_progress` so slow-but-healthy local-model
  runs can be told apart from stuck reasoning loops.

Execute-phase and verifier-phase timeouts are recorded separately and do
not overwrite each other:

- An execute-phase timeout sets `result.status=timeout`,
  `result.phases.execute.status=timeout`, and leaves
  `result.phases.verify.status=skipped`.
- A verifier-phase timeout keeps the execute status intact
  (`result.phases.execute.status=success`) and records
  `result.verify.status=timeout` /
  `result.phases.verify.status=timeout` with its own `duration_ms` and
  `timeout_seconds`. Verifier timeouts appear in the summary under
  `verified_timeout` rather than being counted as executable pass/fail.

Reports include per-arm `executable` counts (runs where the verifier
actually produced pass/fail) and `verified_pass_rate` computed over those
executable runs only, so dry-runs and unreachable verifiers do not dilute
the signal.

Fixture coverage lives in `scripts/beadbench/test_run_beadbench.py`
(`python3 scripts/beadbench/test_run_beadbench.py`).

Probe model-side reasoning controls before local-model evidence runs:

```bash
python3 scripts/beadbench/probe_reasoning_controls.py \
  --arm agent-vidar-omlx-qwen36-27b
```

The probe records whether each provider accepts the control field, separates
`reasoning_content`, and visibly suppresses thinking when reasoning is turned
off. Results are written to
`benchmark-results/beadbench/reasoning-probe-*.json`.

Each probe emits one progress line as soon as it returns, flushed
immediately, so a slow local arm can be distinguished from a stuck run
without waiting for the full arm to finish. For a quick local diagnostic
that only exercises the two Qwen controls most likely to drive reasoning
loops, pass `--probe` once per probe id:

```bash
python3 scripts/beadbench/probe_reasoning_controls.py \
  --arm agent-vidar-omlx-qwen36-27b \
  --probe qwen_off --probe qwen_budget_32
```

The selected probe ids are recorded under `probe_filter` in the generated
report JSON.

For LM Studio specifically, the probe now tests two distinct API surfaces:

- OpenAI-compatible `/v1/chat/completions`, which is the execute-bead surface
  because it supports custom tools, but where Qwen/GPT-OSS reasoning controls
  may be accepted without being honored by the loaded template.
- Native `/api/v1/chat`, which exposes LM Studio's documented
  `reasoning: off|low|medium|high|on` control and returns
  `stats.reasoning_output_tokens`, but is not currently the execute-bead
  surface because LM Studio documents custom tools on `/v1/chat/completions`
  and `/v1/responses`, not native chat.

The LM Studio native probes are
`lmstudio_native_reasoning_off`, `lmstudio_native_reasoning_low`, and
`lmstudio_native_reasoning_high`. Treat a `recommended_wire_format` of
`lmstudio_native` as evidence that LM Studio can control reasoning on the
native endpoint, not as proof that the current agent tool loop can switch to
that endpoint without a separate tools-compatible design.

Current local evidence: on 2026-04-23, Vidar OMLX
`Qwen3.6-27B-MLX-8bit` and `Qwen3.6-35B-A3B-4bit` accepted both the legacy
`thinking` map and Qwen controls, but only Qwen
`enable_thinking`/`thinking_budget` changed observable behavior. `qwen_off`
returned a short direct answer while the no-control and `thinking`-map probes
filled the response with visible thinking text.

### Vidar OMLX `gpt-oss-20b-MXFP4-Q8` reasoning control: unsupported/unknown

The same probe run found that Vidar OMLX `gpt-oss-20b-MXFP4-Q8` emits
`reasoning_content` by default but has no known budget/off control in this
matrix. A 2026-04-23 live probe of the no-control arm
(`benchmark-results/beadbench/reasoning-probe-20260423T034159Z.json`) returned
HTTP 200 with `content_chars=0, reasoning_chars=88` at `max_tokens=48` and
`recommended_wire_format=unknown`: the assistant message carried only
`reasoning_content`, all completion tokens were consumed by reasoning, and no
visible answer remained under the budget.

Wire-format rationale:

- GPT-OSS is a Harmony-format model family (OpenAI `gpt-oss-20b`/`gpt-oss-120b`).
  Harmony expects a top-level `reasoning_effort` ∈ {`low`,`medium`,`high`} and
  does not accept Qwen's `enable_thinking` / `thinking_budget`, nor
  Anthropic-style `thinking: {type, budget_tokens}`.
- The OMLX provider is pinned to `ThinkingWireFormatQwen` with
  `StrictThinkingModelMatch=true`, so the native agent rejects any explicit
  reasoning request against `gpt-oss-20b-MXFP4-Q8` at serialization time rather
  than silently sending Qwen fields the template would ignore. This is enforced
  by `TestQwenReasoningSerializationRejectsNonQwenModels` in
  `internal/provider/openai/openai_test.go`; the arm-level manifest rationale
  therefore omits `effort` and does not attempt `ReasoningOff`.
- No GPT-OSS-specific wire format is wired into the provider because the probe
  has not yet confirmed that any effort-level knob measurably changes reasoning
  output on this OMLX build. Until a live probe reports
  `recommended_wire_format=gpt_oss_effort`, the arm is treated as
  `reasoning-control=unsupported/unknown`.

How to interpret benchmark failures on `agent-vidar-omlx-gptoss20b`:

- Treat an empty `content` with non-empty `reasoning_content` as the expected
  baseline, not a harness bug. The model answered in reasoning; the answer did
  not fit under the caller's `max_tokens` budget.
- Prefer evidence-grade arms with a verified reasoning control
  (`agent-vidar-omlx-qwen36-27b`, `agent-vidar-omlx-qwen36-35b`) when comparing
  local models against frontier baselines. The GPT-OSS arm is a research arm
  for non-Qwen OMLX behavior, not a substitute for a budgeted local-model arm.
- Extending the `probe_reasoning_controls.py` PROBES list with
  `gptoss_effort_{low,medium,high}` and `gptoss_reasoning_map_low` lets a
  future live run classify the model as `gpt_oss_effort` (honored),
  `gpt_oss_unsupported` (accepted but no behavioral change), or `unknown`
  (request rejected). Promote the arm to `reasoning-control=supported` only
  after a recorded probe reports `gptoss_effort_changes_reasoning=true`.

### Bragi LM Studio `qwen/qwen3.6-35b-a3b` reasoning control: operational blocker

A later live investigation (2026-04-23) against Bragi LM Studio on port 1234
showed the earlier "all timeouts" finding was an artifact of the probe's
45-second cap, not a server refusal. Raising the probe timeout to 120 seconds
makes every tested control shape return HTTP 200, including no control,
`thinking` map (both `budget_tokens=32` and `=0`), Qwen `enable_thinking=false
thinking_budget=0`, Qwen `enable_thinking=true thinking_budget=32`,
`chat_template_kwargs.enable_thinking=false`, OpenAI-style
`reasoning_effort=minimal`, and the `/no_think` prompt convention. In every
response the assistant message contains empty `content` and a populated
`reasoning_content`, and `usage.completion_tokens_details.reasoning_tokens`
equals the emitted completion tokens.

Server/version evidence (from `GET /api/v0/models/qwen/qwen3.6-35b-a3b`):

| field | value |
| --- | --- |
| `arch` | `qwen35moe` |
| `publisher` | `qwen` |
| `compatibility_type` | `gguf` |
| `quantization` | `Q4_K_M` |
| `state` | `loaded` |
| `max_context_length` | 262144 |
| `loaded_context_length` | 262144 |
| `capabilities` | `["tool_use"]` (no `"thinking"` / no reasoning budget capability) |

LM Studio HTTP response header reports `X-Powered-By: Express`; the model is
a vision-language GGUF served from `qwen35moe` at Q4_K_M.

Conclusion: the LM Studio build accepts every known reasoning-control wire
shape but does not forward any of them into this GGUF's chat template, so
reasoning is produced unconditionally and cannot be bounded from the
request. This is a model/template limitation, not a Qwen-family wire-format
bug — the Qwen controls are the correct per-family shape and are used for
LM Studio Qwen models regardless, so that `ReasoningOff` emits the intended
`enable_thinking=false, thinking_budget=0` disable signal for any LM Studio
Qwen build that does respect it. Until LM Studio or the GGUF template
changes, beadbench must treat Bragi `qwen/qwen3.6-35b-a3b` as a no-budget
arm: reasoning-control probes are expected to be `accepted=true` but
behaviorally no-op, and the beadbench tracker's `effort` label does not
change observable runtime reasoning. Use OMLX `Qwen3.6-27B-MLX-8bit` /
`Qwen3.6-35B-A3B-4bit` when enforced reasoning budgets are needed.

## Tasks invalid for model-comparison scoring

A task may carry `"model_comparison_valid": false` in `manifest-v1.json` when its
verifier is known to produce model-independent results (for example, the verifier
fixture is internally inconsistent, the acceptance command has a defect, or the
upstream project's state diverges from the pinned base revision). Such tasks
should be excluded from separability aggregates and leaderboard-style rollups;
the runner still executes them so harness-side regressions remain visible.

The flag is wired through `run_beadbench.py`: each result echoes
`model_comparison_valid`, and `summarize()` emits a `model_comparison` block
(alongside the top-level totals) that excludes flagged tasks and records the
`excluded_task_ids`. `print_summary` prints the separability pass rate and the
excluded ids on every run. Top-level counts still include every run so harness
regressions on flagged tasks remain visible.

Currently marked invalid:

- `helix-triage-blanket-priming` — the helix `tests/validate-skills.sh`
  mixed-ready-semantics block diffs `ddx bead ready --execution --json` against
  `validate_execution_ready_beads.execution_ready_beads` on a shared fixture; the
  two views disagree (`hx-in-progress-build` vs `hx-ready-epic`) regardless of
  what the agent edits, so both Codex and Sonnet execute-success runs fail the
  verifier identically (see
  `benchmark-results/beadbench/run-20260423T054306Z-3937255/report.json`). The
  task can be re-enabled once helix reconciles the CLI and validator views of
  the execution-ready surface.

  Under the `model_comparison` aggregate, both the Codex GPT-5.4 and Sonnet 4.6
  runs for this task id are dropped from separability totals — they currently
  appear only in the top-level executable/verified counts as harness evidence.
  The upstream fixture fix (reconciling `ddx bead ready --execution --json` with
  `validate_execution_ready_beads.execution_ready_beads`, or rewriting the
  expected-ids list to
  `['hx-in-progress-build', 'hx-ready-build', 'hx-ready-review', 'hx-ready-vague']`)
  lives in the helix repository and is intentionally deferred out of this bead's
  scope; flipping `model_comparison_valid` back to `true` is the re-enable step
  once that change lands.

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
