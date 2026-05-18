# Parity Diff Allowlist

This document defines JSON paths in cell reports that are permitted to diverge between the Go runner baseline and bash runner candidate without failing parity. Each entry lists the JSON path (dot notation) and rationale.

## Execution Context (Timestamp & Identity)

- `cell_id` — Timestamp-based cell identifier generated at execution time; differs between runs due to execution time. Allowlisted because the cell content (not identity) is the signal.
- `started_at` — Execution start timestamp; differs due to different host environments and execution order.
- `finished_at` — Execution end timestamp; differs due to different host environments and execution duration.

## Duration & Performance Metrics

- `duration_*` (e.g., `duration_seconds`, `duration_wall_seconds`) — Execution duration metrics are host/environment-dependent and not a signal for parity. Allowlisted because the metric itself is meaningful, but comparing across different environments is not valid.
- `wall_seconds` — Total wall-clock time; environment-dependent. Allowlisted as a performance metric, not a correctness signal.

## Session & Artifact Paths

- `session.*` — Session directories and paths within the session artifact. These are host-specific and ephemeral; allowlisted because the session content (if any) is captured elsewhere.
- `artifacts.*` — Maps artifact types to paths; paths are host/execution-specific. Allowlisted because the artifact *types* are meaningful, not the paths themselves.
- `output_dir` — Output directory path on the host; host-specific and ephemeral. Allowlisted in Go runner baseline (not present in bash runner).

## Host-Specific Paths

- Any path containing `/tmp/` — Host-specific temporary directories. Allowlisted because they are environment artifacts, not cell logic.
- Any path containing container/job-specific mounts (e.g., `/var/tmp/`, `/sandbox/`) — Allowlisted for the same reason.

## Bash Runner New Fields

The following fields are new in the bash runner and have no Go runner counterpart; they are allowlisted as bash-runner-specific additions:

- `harbor_runner_image_digest` — Digest of the docker image that executed the cell; identifies the task execution environment, not the cell logic.
- `task_executor_version` — Version of the task executor inside the harbor container; allowlisted as a bash runner metadata field.
- `env_redacted` — Environment variables passed to the agent (with secrets masked); allowlisted as bash runner introspection, not a correctness signal.
- `result` — Bash runner wraps cell results in a result object; Go runner spreads the same data at the top level. Allowlisted because both contain the same semantic outcome.
- `model_server.commit` — Commit hash of the model server (if present); allowlisted as environment metadata.
- `attempt_of` — Attempt number if the cell was retried; allowlisted as execution metadata.
- `superseded_by` — Cell ID of the superseding cell (if this cell was superseded); allowlisted as execution metadata.

## Go Runner Only Fields

The following fields appear in the Go runner baseline but not in bash runner output; they are allowlisted as runner-specific:

- `adapter_module` — Go runner internal field; not present in bash runner.
- `harbor_agent` — Go runner internal field; not present in bash runner.
- `profile_id` — Duplicated profile identifier; bash runner only includes full profile object.
- `profile_path` — Host-specific path to profile YAML; not meaningful for parity.
- `profile_snapshot` — String snapshot of profile; redundant with full profile object.
- `fiz_tools_version` — Go runner internal versioning; bash runner uses different versioning scheme.
- `grading_outcome` — Go runner field; bash runner uses `result.outcome` instead.
- `process_outcome` — Go runner field; not present in bash runner.
- `input_tokens` — Go runner field (null in baseline); bash runner may include in `result.usage`.
- `output_tokens` — Go runner field (null in baseline); bash runner may include in `result.usage`.
- `cached_input_tokens` — Go runner field (null in baseline); bash runner may include in `result.usage`.
- `retried_input_tokens` — Go runner field (null in baseline); bash runner does not track separately.
- `tool_calls` — Go runner field (null in baseline); bash runner may include in `result` or `artifacts`.
- `tool_call_errors` — Go runner field (null in baseline); bash runner may include in `result`.
- `turns` — Go runner field (null in baseline); bash runner may include in `result`.
- `category` — Go runner task metadata; not present in bash runner (implicit from dataset).
- `difficulty` — Go runner task metadata; not present in bash runner (implicit from dataset).
- `exit_code` — Go runner field (0 in baseline); bash runner does not track this way.
- `pricing_source` — Go runner internal; bash runner tracks pricing differently.
- `runtime_props` — Go runner runtime properties (extracted data); bash runner uses different structure.
- `sampling_used` — Go runner sampling record; bash runner may not record.
- `reward` — Explicit reward field in Go runner; bash runner includes in `result.reward`.

## Field Renames & Refactors

- `cost_usd` (Go) vs `cost_usd_at_run_time` (Bash) — Field rename for clarity; allowlisted because both represent cost metrics.
- `dataset` — Go includes full path (e.g., `terminal-bench/terminal-bench-2-1`), bash includes version in separate field (`terminal-bench-2-1`); allowlisted because the effective dataset is derivable from both.
- `dataset_version` — Go runner field; bash runner may not include explicit version. Allowlisted as Go-only field.
- `harness` — Go runner metadata field; not present in bash runner. Allowlisted.
- `rep` — Format differs: Go uses numeric (e.g., `1`), bash uses string format (e.g., `"1/3"`); allowlisted because both represent repetition information.
- `command` — Differs between runners: Go runs a simple shell command, bash runs the actual fiz command wrapper. Allowlisted because this is expected runner behavior difference.
- `profile` — Object structure may differ between runners (different ordering, potentially different sub-fields). Allowlisted because core profile content (provider, limits, sampling) is expected to match semantically.

## Final Status & Grading

- `final_status` — Should be consistent between runners; NOT allowlisted (must match).
- `invalid_class` — Should be consistent between runners; NOT allowlisted (must match).
- (In bash runner) `result.reward` — Should match Go runner `reward` field; NOT allowlisted (must match).

## Core Report Fields (NOT Allowlisted)

The following fields **must match** (or match semantically) between Go and bash runner reports. Divergences in these fields indicate a parity failure:

- `task_id` — The task being executed. Must be identical.
- `framework` — The benchmark framework (always `terminal-bench`). Must be identical.
- `final_status` — Outcome status (`graded_pass`, `graded_fail`, etc.). Go runner has this at top-level; bash runner may have it in `result.status` or `result.final_status`.
- `invalid_class` — Invalidity classification (if applicable). Must be identical.

## Semantic Equivalence (Allowlisted Because Relocated)

These fields must be semantically equivalent but may be relocated or reformatted:

- **Reward/Score**: (Go) `reward` vs (Bash) `result.reward` — Allowlisted because it's the same value in different locations.
- **Cost**: (Go) `cost_usd` vs (Bash) `cost_usd_at_run_time` — Allowlisted because field is renamed.
- **Profile**: Allowlisted because object structure may differ (ordering, redundant fields removed) but core content is equivalent.
- **Rep**: Allowlisted because format differs but semantically represents the same repetition info.

---

**How diff.sh Uses This List**: The diff.sh script maintains an internal allowlist of JSON paths (in regex or exact-match form) that match these categories. Any divergence at a path not on the allowlist causes diff.sh to exit non-zero and report the offending path.
